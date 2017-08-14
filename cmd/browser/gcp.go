// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/jwt"
	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
	iam "google.golang.org/api/iam/v1"
	servicemanagement "google.golang.org/api/servicemanagement/v1"
	storage "google.golang.org/api/storage/v1"
)

type gcpState struct {
	JWTConfig *jwt.Config
	ProjectID string
	Region    string
	Zone      string

	Instance string
	Bucket   string

	Hostname   string
	ServerUser string
}

func doGCP() error {
	b, err := ioutil.ReadFile("Test-3728260541fb.json")
	if err != nil {
		return err
	}
	s, err := gcpStateFromPrivateKeyJSON(b)
	if err != nil {
		return err
	}
	//if err := s.enableAPIs(); err != nil {
	//	return err
	//}
	return s.createInstance()
}

func gcpStateFromPrivateKeyJSON(b []byte) (*gcpState, error) {
	cfg, err := google.JWTConfigFromJSON(b, compute.CloudPlatformScope)
	if err != nil {
		return nil, err
	}
	projectID, err := serviceAccountEmailToProjectID(cfg.Email)
	if err != nil {
		return nil, err
	}
	return &gcpState{
		JWTConfig: cfg,
		ProjectID: projectID,
		Region:    "us-central1",
		Zone:      "us-central1-a",
	}, nil
}

func serviceAccountEmailToProjectID(email string) (string, error) {
	i := strings.Index(email, "@")
	if i < 0 {
		return "", fmt.Errorf("service account email %q has no @ sign", email)
	}
	const domain = ".iam.gserviceaccount.com"
	if !strings.HasSuffix(email, domain) {
		return "", fmt.Errorf("service account email %q does not have expected form", email)
	}
	return email[i+1 : len(email)-len(domain)], nil
}

func (s *gcpState) enableAPIs() error {
	client := s.JWTConfig.Client(context.Background())
	svc, err := servicemanagement.New(client)
	if err != nil {
		return err
	}
	apis := []string{
		"compute_component",  // For the virtual machine.
		"storage_api",        // For storage bucket.
		"iam.googleapis.com", // For creating a service account.
	}
	for _, api := range apis {
		if err := s.enableAPI(api, svc); err != nil {
			return err
		}
	}
	return nil
}

func (s *gcpState) enableAPI(name string, svc *servicemanagement.APIService) error {
	op, err := svc.Services.Enable(name, &servicemanagement.EnableServiceRequest{ConsumerId: "project:" + s.ProjectID}).Do()
	if err != nil {
		return err
	}
	for !op.Done {
		op, err = svc.Operations.Get(op.Name).Do()
		if err != nil {
			return err
		}
	}
	if op.Error != nil {
		return errors.New(op.Error.Message)
	}
	return err
}

func (s *gcpState) createInstance() error {
	client := s.JWTConfig.Client(context.Background())
	svc, err := compute.New(client)
	if err != nil {
		return err
	}

	// TODO: make these configurable?
	const (
		firewallName = "allow-https"
		firewallTag  = firewallName

		instanceName = "upspinserver"
		addressName  = instanceName
	)
	machineType := "zones/" + s.Zone + "/machineTypes/n1-standard-1"

	log.Println("create addr")
	// Create an address
	addr := &compute.Address{
		Description: "Public IP address for upspinserver",
		Name:        addressName,
	}
	op, err := svc.Addresses.Insert(s.ProjectID, s.Region, addr).Do()
	if err = okReason("alreadyExists", s.waitOp(svc, op, err)); err != nil {
		return err
	}
	addr, err = svc.Addresses.Get(s.ProjectID, s.Region, addressName).Do()
	if err != nil {
		return fmt.Errorf("getting addr: %v", err)
	}

	log.Println("create firewall")
	// Create a firewall to permit HTTPS connections.
	firewall := &compute.Firewall{
		Allowed: []*compute.FirewallAllowed{{
			IPProtocol: "tcp",
			Ports:      []string{"443"},
		}},
		Description:  "Allow HTTPS",
		Name:         firewallName,
		SourceRanges: []string{"0.0.0.0/0"},
		TargetTags:   []string{firewallTag},
	}
	op, err = svc.Firewalls.Insert(s.ProjectID, firewall).Do()
	if err = okReason("alreadyExists", s.waitOp(svc, op, err)); err != nil {
		return err
	}

	log.Println("create instance")
	// Create a firewall to permit HTTPS connections.
	// Create the instance.
	userData := cloudInitYAML
	instance := &compute.Instance{
		Description: "upspinserver instance",
		Disks: []*compute.AttachedDisk{{
			AutoDelete: true,
			Boot:       true,
			DeviceName: "upspinserver",
			InitializeParams: &compute.AttachedDiskInitializeParams{
				SourceImage: "projects/cos-cloud/global/images/family/cos-stable",
			},
		}},
		MachineType: machineType,
		Name:        instanceName,
		Tags:        &compute.Tags{Items: []string{firewallTag}},
		Metadata: &compute.Metadata{
			Items: []*compute.MetadataItems{{
				Key:   "user-data",
				Value: &userData,
			}},
		},
		NetworkInterfaces: []*compute.NetworkInterface{{
			AccessConfigs: []*compute.AccessConfig{{
				NatIP: addr.Address,
			}},
		}},
	}
	op, err = svc.Instances.Insert(s.ProjectID, s.Zone, instance).Do()
	return s.waitOp(svc, op, err)
}

func (s *gcpState) createServiceAccount() (email, privateKeyData string, err error) {
	client := s.JWTConfig.Client(context.Background())
	svc, err := iam.New(client)
	if err != nil {
		return "", "", err
	}

	name := "projects/" + s.ProjectID
	req := &iam.CreateServiceAccountRequest{
		AccountId: "upspinstorage",
		ServiceAccount: &iam.ServiceAccount{
			DisplayName: "Upspin Storage",
		},
	}
	acct, err := svc.Projects.ServiceAccounts.Create(name, req).Do()
	if isExists(err) {
		// This should be the name we need to get.
		// TODO(adg): make this more robust by listing instead.
		guess := name + "/serviceAccounts/upspinstorage@" + s.ProjectID + ".iam.gserviceaccount.com"
		acct, err = svc.Projects.ServiceAccounts.Get(guess).Do()
		if err != nil {
			return "", "", err
		}
	} else if err != nil {
		return "", "", err
	}

	name += "/serviceAccounts/" + acct.Email
	req2 := &iam.CreateServiceAccountKeyRequest{}
	key, err := svc.Projects.ServiceAccounts.Keys.Create(name, req2).Do()
	if err != nil {
		return "", "", err
	}
	return acct.Email, key.PrivateKeyData, nil
}

func (s *gcpState) createBucket(email, bucket string) error {
	client := s.JWTConfig.Client(context.Background())
	svc, err := storage.New(client)
	if err != nil {
		return err
	}

	_, err = svc.Buckets.Insert(s.ProjectID, &storage.Bucket{
		Acl: []*storage.BucketAccessControl{{
			Bucket: bucket,
			Entity: "user-" + email,
			Email:  email,
			Role:   "OWNER",
		}},
		Name: bucket,
		// TODO(adg): flag for location
	}).Do()
	if isExists(err) {
		// Bucket already exists.
		// TODO(adg): update bucket ACL to make sure the service
		// account has access. (For now, we assume that the user
		// created the bucket using this command and that the bucket
		// has the correct permissions.)
		return nil
	}
	return err
}

func isExists(err error) bool {
	if e, ok := err.(*googleapi.Error); ok && len(e.Errors) > 0 {
		for _, e := range e.Errors {
			if e.Reason != "alreadyExists" && e.Reason != "conflict" {
				return false
			}
		}
		return true
	}
	return false
}

func (s *gcpState) waitOp(svc *compute.Service, op *compute.Operation, err error) error {
	for err == nil && (op.Status == "PENDING" || op.Status == "RUNNING") {
		time.Sleep(1 * time.Second)
		switch {
		case op.Zone != "":
			op, err = svc.ZoneOperations.Get(s.ProjectID, s.Zone, op.Name).Do()
		case op.Region != "":
			op, err = svc.RegionOperations.Get(s.ProjectID, s.Region, op.Name).Do()
		default:
			op, err = svc.GlobalOperations.Get(s.ProjectID, op.Name).Do()
		}
	}
	return opError(op, err)
}

func opError(op *compute.Operation, err error) error {
	if err != nil {
		return err
	}
	if op == nil || op.Error == nil || len(op.Error.Errors) == 0 {
		return nil
	}
	return errors.New(op.Error.Errors[0].Message)
}

func okReason(reason string, err error) error {
	if e, ok := err.(*googleapi.Error); ok && len(e.Errors) > 0 {
		for _, e := range e.Errors {
			if e.Reason != reason {
				return err
			}
		}
		return nil
	}
	return err
}

const cloudInitYAML = `#cloud-config

users:
- name: upspin
  uid: 2000

runcmd:
- iptables -w -A INPUT -p tcp --dport 443 -j ACCEPT

write_files:
- path: /etc/systemd/system/upspinserver.service
  permissions: 0644
  owner: root
  content: |
    [Unit]
    Description=An upspinserver container instance
    Wants=gcr-online.target
    After=gcr-online.target
    [Service]
    Environment="HOME=/home/upspin"
    ExecStartPre=/usr/bin/docker-credential-gcr configure-docker
    ExecStart=/usr/bin/docker run --rm -u=2000 --volume=/home/upspin:/upspin -p=443:8443 --name=upspinserver gcr.io/upspin-containers/upspinserver:latest
    ExecStop=/usr/bin/docker stop upspinserver
    ExecStopPost=/usr/bin/docker rm upspinserver

runcmd:
- systemctl daemon-reload
- systemctl start upspinserver.service

`
