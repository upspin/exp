// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// TODO: tell the user to remove/deactivate the Owners service account once
// we're done with it. (Or maybe we can do this mechanically?)

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"upspin.io/flags"
	"upspin.io/subcmd"
	"upspin.io/upspin"

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

	APIsEnabled bool

	Region string
	Zone   string

	Storage struct {
		ServiceAccount string
		PrivateKeyData string
		Bucket         string
	}

	Server struct {
		IPAddr string

		Created bool

		KeyDir   string
		UserName upspin.UserName

		HostName string

		Configured bool
	}
}

func (s *gcpState) serverEndpoint() upspin.Endpoint {
	return upspin.Endpoint{
		Transport: upspin.Remote,
		NetAddr:   upspin.NetAddr(s.Server.HostName) + ":443",
	}
}

func (s *gcpState) serverConfig() *subcmd.ServerConfig {
	return &subcmd.ServerConfig{
		Addr:        upspin.NetAddr(s.Server.HostName),
		User:        s.Server.UserName,
		StoreConfig: s.storeConfig(),
	}
}

func (s *gcpState) storeConfig() []string {
	return []string{
		"backend=GCS",
		"defaultACL=publicRead",
		"gcpBucketName=" + s.Storage.Bucket,
		"privateKeyData=" + s.Storage.PrivateKeyData,
	}
}

func gcpStateFromFile() (*gcpState, error) {
	name := flags.Config + ".gcpState"
	b, err := ioutil.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var s gcpState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *gcpState) save() error {
	name := flags.Config + ".gcpState"
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(name, b, 0644)
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
	s := &gcpState{
		JWTConfig: cfg,
		ProjectID: projectID,
		Region:    "us-central1",
		Zone:      "us-central1-a",
	}
	if !s.APIsEnabled {
		if err := s.enableAPIs(); err != nil {
			return nil, err
		}
		s.APIsEnabled = true
	}
	if err := s.save(); err != nil {
		return nil, err
	}
	return s, nil
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

func (s *gcpState) create(bucketName string) error {
	if s.Storage.ServiceAccount == "" {
		email, key, err := s.createServiceAccount()
		if err != nil {
			return err
		}
		s.Storage.ServiceAccount = email
		s.Storage.PrivateKeyData = key
	}
	if err := s.save(); err != nil {
		return err
	}
	if s.Storage.Bucket == "" {
		err := s.createBucket(bucketName)
		if err != nil {
			return err
		}
		s.Storage.Bucket = bucketName
	}
	if err := s.save(); err != nil {
		return err
	}
	if s.Server.IPAddr == "" {
		ip, err := s.createAddress()
		if err != nil {
			return err
		}
		s.Server.IPAddr = ip
	}
	if err := s.save(); err != nil {
		return err
	}
	if !s.Server.Created {
		err := s.createInstance()
		if err != nil {
			return err
		}
		s.Server.Created = true
	}
	return s.save()
}

func (s *gcpState) createAddress() (ip string, err error) {
	client := s.JWTConfig.Client(context.Background())
	svc, err := compute.New(client)
	if err != nil {
		return "", err
	}

	const addressName = "upspinserver"
	addr := &compute.Address{
		Description: "Public IP address for upspinserver",
		Name:        addressName,
	}
	op, err := svc.Addresses.Insert(s.ProjectID, s.Region, addr).Do()
	if err = okReason("alreadyExists", s.waitOp(svc, op, err)); err != nil {
		return "", err
	}
	addr, err = svc.Addresses.Get(s.ProjectID, s.Region, addressName).Do()
	if err != nil {
		return "", err
	}
	return addr.Address, nil
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
	)
	machineType := "zones/" + s.Zone + "/machineTypes/n1-standard-1"

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
	op, err := svc.Firewalls.Insert(s.ProjectID, firewall).Do()
	if err = okReason("alreadyExists", s.waitOp(svc, op, err)); err != nil {
		return err
	}

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
				NatIP: s.Server.IPAddr,
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
	}
	if err != nil {
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

func (s *gcpState) createBucket(bucket string) error {
	client := s.JWTConfig.Client(context.Background())
	svc, err := storage.New(client)
	if err != nil {
		return err
	}

	_, err = svc.Buckets.Insert(s.ProjectID, &storage.Bucket{
		Acl: []*storage.BucketAccessControl{{
			Bucket: bucket,
			Entity: "user-" + s.Storage.ServiceAccount,
			Email:  s.Storage.ServiceAccount,
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

func (s *gcpState) configureServer(writers []upspin.UserName) error {
	files := map[string][]byte{}

	var buf bytes.Buffer
	for _, u := range writers {
		fmt.Fprintln(&buf, u)
	}
	files["Writers"] = buf.Bytes()

	for _, name := range []string{"public.upspinkey", "secret.upspinkey"} {
		b, err := ioutil.ReadFile(filepath.Join(s.Server.KeyDir, name))
		if err != nil {
			return err
		}
		files[name] = b
	}

	scfg := s.serverConfig()
	b, err := json.Marshal(scfg)
	if err != nil {
		return err
	}
	files["serverconfig.json"] = b

	b, err = json.Marshal(files)
	if err != nil {
		return err
	}
	u := "https://" + string(scfg.Addr) + "/setupserver"
	resp, err := http.Post(u, "application/octet-stream", bytes.NewReader(b))
	if err != nil {
		return err
	}
	b, _ = ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upspinserver returned status %v:\n%s", resp.Status, b)
	}
	return nil
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
