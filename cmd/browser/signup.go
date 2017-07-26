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
	"upspin.io/key/usercache"
	"upspin.io/upspin"
	"upspin.io/valid"
)

const signupURL = "https://key.upspin.io/signup"

type signupResponse struct {
	Step string

	// "secretseed"
	KeyDir     string
	SecretSeed string

	// "verify"
	UserName upspin.UserName
}

func (s *server) signup(req *http.Request) (*signupResponse, upspin.Config, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if cfg != nil {
		return nil, cfg, nil
	}

	step := req.FormValue("step")
	var secretSeed, keyDir string
	if step == "signup" {
		var (
			userName    = upspin.UserName(req.FormValue("username"))
			dirServer   = upspin.NetAddr(req.FormValue("dirserver"))
			storeServer = upspin.NetAddr(req.FormValue("storeserver"))
		)
		if err := valid.UserName(userName); err != nil {
			return nil, nil, err
		}
		// TODO(adg): validate endpoints

		// Check whether userName already exists on the KeyServer.
		userCfg := config.SetUserName(config.New(), userName)
		if ok, err := onKeyServer(userCfg); err != nil {
			return nil, nil, err
		} else if ok {
			return nil, nil, errors.Str("The given user name is already registered with the key server.")
		}

		// Write config file.
		err := writeConfig(flags.Config, userName, dirServer, storeServer)
		if err != nil {
			return nil, nil, err
		}
		// Generate keys.
		secretSeed, keyDir, err = keygen(userName)
		if err != nil {
			os.Remove(flags.Config)
			return nil, nil, err
		}
		step = "register"
	}

	// Look for a config file.
	cfg, err := config.FromFile(flags.Config)
	if errors.Match(errors.E(errors.NotExist), err) {
		// Config doesn't exist; need to sign up.
		return &signupResponse{
			Step: "signup",
		}, nil, nil
	} else if err != nil {
		return nil, nil, err
	}

	if step == "register" {
		//	if err := signup.MakeRequest(signupURL, cfg); err != nil {
		//		return nil, nil, err
		//	}
		next := "verify"
		if secretSeed != "" {
			// Show the secret seed if we have just generated the key.
			next = "secretseed"
		}
		return &signupResponse{
			Step:       next,
			KeyDir:     keyDir,
			SecretSeed: secretSeed,
			UserName:   cfg.UserName(),
		}, nil, nil
	}

	// Is the user now registered with the KeyServer?
	if ok, err := onKeyServer(cfg); err != nil {
		return nil, nil, err
	} else if !ok {
		// TODO: Read seed from secret.upspinkey?
		return &signupResponse{
			Step:     "verify",
			UserName: cfg.UserName(),
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

func keygen(user upspin.UserName) (seed, keyDir string, err error) {
	keyDir, err = config.DefaultSecretsDir(user)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		return "", "", err
	}
	out, err := exec.Command("upspin", "keygen", keyDir).CombinedOutput()
	if err != nil {
		return "", "", errors.Errorf("%v\n%s", err, out)
	}
	const prefix = "-secretseed "
	i := bytes.Index(out, []byte(prefix))
	if i == -1 {
		return "", "", errors.Errorf("unexpected keygen output:\n%s", out)
	}
	seed = string(out[i+len(prefix):])
	i = strings.Index(seed, " ")
	if i == -1 {
		return "", "", errors.Errorf("unexpected keygen output:\n%s", out)
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
	cfg := fmt.Sprintf("username: %s\n", user)
	if dir != "" {
		cfg += fmt.Sprintf("dirserver: remote,%s\n", dir)
	}
	if store != "" {
		cfg += fmt.Sprintf("storeserver: remote,%s\n", store)
	}
	cfg += "packing: ee\n"
	return ioutil.WriteFile(file, []byte(cfg), 0644)
}

func onKeyServer(cfg upspin.Config) (bool, error) {
	key, err := bind.KeyServer(cfg, cfg.KeyEndpoint())
	if err != nil {
		return false, err
	}
	usercache.ResetGlobal() // Avoid hitting the local user cache.
	_, err = key.Lookup(cfg.UserName())
	if errors.Match(errors.E(errors.NotExist), err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func makeRoot(cfg upspin.Config) error {
	ep := cfg.DirEndpoint()
	if ep.Transport != upspin.Remote {
		return nil
	}
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
