// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"upspin.io/bind"
	"upspin.io/client"
	"upspin.io/cmd/cacheserver/cacheutil"
	"upspin.io/config"
	"upspin.io/errors"
	"upspin.io/flags"
	"upspin.io/serverutil/signup"
	"upspin.io/upspin"
)

const signupURL = "https://key.upspin.io/signup"

type signupResponse struct {
	NextStep string

	SecretSeed string
}

func (s *server) signup(req *http.Request) (*signupResponse, upspin.Config, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if cfg != nil {
		return nil, cfg, nil
	}

	step := req.FormValue("step")
	if step == "signup" {
		var (
			userName    = upspin.UserName(req.FormValue("username"))
			dirServer   = upspin.NetAddr(req.FormValue("dirserver"))
			storeServer = upspin.NetAddr(req.FormValue("storeserver"))
		)
		// TODO(adg): validate input

		// TODO(adg): check whether userName already exists on the KeyServer.

		// Write config file.
		err := writeConfig(flags.Config, userName, dirServer, storeServer)
		if err != nil {
			return nil, nil, err
		}
		// Generate keys.
		seed, err := keygen(userName)
		if err != nil {
			os.Remove(flags.Config)
			return nil, nil, err
		}
		// Send signup request.
		if err := signup.MakeRequest(signupURL, cfg); err != nil {
			return nil, nil, err
		}
		return &signupResponse{
			NextStep:   "wait-verify",
			SecretSeed: seed,
		}, nil, nil
	}

	// Look for a config file.
	cfg, err := config.FromFile(flags.Config)
	if errors.Match(errors.E(errors.NotExist), err) {
		// Config doesn't exist; need to sign up.
		return &signupResponse{
			NextStep: "signup",
		}, nil, nil
	} else if err != nil {
		return nil, nil, err
	}

	// Has the user asked us to re-send the signup request?
	if step == "resend" {
		if err := signup.MakeRequest(signupURL, cfg); err != nil {
			return nil, nil, err
		}
		return &signupResponse{
			NextStep: "wait-verify",
		}, nil, nil
	}

	// Is the user now registered with the KeyServer?
	if ok, err := onKeyServer(cfg); err != nil {
		return nil, nil, err
	} else if !ok {
		return &signupResponse{
			NextStep: "wait-verify",
		}, nil, nil
	}

	// Make the user's root if it doesn't exist.
	if err := makeRoot(cfg); err != nil {
		return nil, nil, err
	}

	s.mu.Lock()
	s.cfg = cfg
	s.cli = client.New(cfg)
	s.mu.Unlock()

	cacheutil.Start(cfg)
	return nil, cfg, nil
}

func keygen(user upspin.UserName) (seed string, err error) {
	secrets, err := config.DefaultSecretsDir(user)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(secrets, 0700); err != nil {
		return "", err
	}
	out, err := exec.Command("upspin", "keygen", secrets).CombinedOutput()
	if err != nil {
		return "", errors.Errorf("%v\n%s", err, out)
	}
	i := bytes.Index(out, []byte("upspin keygen"))
	if i == -1 {
		return "", errors.Errorf("unexpected keygen output:\n%s", out)
	}
	seed = string(out[i:])
	i = strings.Index(seed, "\n")
	if i == -1 {
		return "", errors.Errorf("unexpected keygen output:\n%s", out)
	}
	seed = seed[:i]
	return
}

func writeConfig(file string, user upspin.UserName, dir, store upspin.NetAddr) error {
	if _, err := os.Stat(file); err == nil {
		return errors.Errorf("cannot write %s: file already exists", file)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		return err
	}
	cfg := fmt.Sprintf(`username: %s
storeserver: remote,%s
dirserver: remote,%s
packing: ee
`)
	return ioutil.WriteFile(file, []byte(cfg), 0644)
}

func onKeyServer(cfg upspin.Config) (bool, error) {
	key, err := bind.KeyServer(cfg, cfg.KeyEndpoint())
	if err != nil {
		return false, err
	}
	_, err = key.Lookup(cfg.UserName())
	if errors.Match(errors.E(errors.NotExist), err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, err
}

func makeRoot(cfg upspin.Config) error {
	dir, err := bind.DirServer(cfg, cfg.DirEndpoint())
	if err != nil {
		return err
	}
	p := upspin.PathName(cfg.UserName())
	_, err = dir.Lookup(p)
	if err == nil {
		return nil
	}
	if !errors.Match(errors.E(errors.NotExist), err) {
		return err
	}
	_, err = client.New(cfg).MakeDirectory(p)
	return err
}
