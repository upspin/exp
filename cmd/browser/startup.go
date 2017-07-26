// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// TODO(adg): Support automatically provisioning an upspinserver instance.

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
	"upspin.io/serverutil/signup"
	"upspin.io/upspin"
	"upspin.io/valid"
)

const signupURL = "https://key.upspin.io/signup"

// startupResponse is a sent to the client in response to startup requests.
type startupResponse struct {
	// Step is the modal dialog that should be displayed to the user at
	// this stage of the startup process.
	// It may be one of "startup", "secretseed", or "verify".
	Step string

	// Step: "secretseed"
	KeyDir     string
	SecretSeed string

	// Step: "verify"
	UserName upspin.UserName
}

// startup populates s.cfg and s.cli by either loading the config file
// nominated by flags.Config, or by taking the user through the signup process.
//
// The signup process works by checking for various conditions, and instructing
// the JS/HTML front end to present various Steps to the user.
//  - The config file exists at flags.Config. If not:
//    - Prompt the user for a user name and server endpoints (Step: "signup").
//    - Write a new config and generate keys (action "signup").
//    - Register the user and keys with the key server (action "register").
//  - Check that the config's user exists on the Key Server. If not:
//    - Prompt the user to click the verification link in the email (Step: "verify").
//  - Check that the user's root exists. If not:
//    - Make the user's root.
//
// Only one of startup's return values should be non-nil. If a user is to be
// presented with a given step, startup returns a non-nil startupResponse. If
// all the conditions are met, startup returns a non-nil Config. If an error
// occurs startup returns that error.
func (s *server) startup(req *http.Request) (*startupResponse, upspin.Config, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if cfg != nil {
		return nil, cfg, nil
	}

	action := req.FormValue("action")

	var secretSeed, keyDir string
	if action == "signup" {
		// The user clicked the "Sign up" button on the signup dialog.
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
		if ok, err := isRegistered(userCfg); err != nil {
			return nil, nil, err
		} else if ok {
			return nil, nil, errors.Errorf("%q is already registered.", userName)
		}

		// Write config file.
		err := writeConfig(flags.Config, userName, dirServer, storeServer)
		if err != nil {
			return nil, nil, err
		}

		// Generate keys.
		secretSeed, keyDir, err = keygen(userName)
		if err != nil {
			// Don't leave the config lying around.
			os.Remove(flags.Config)
			return nil, nil, err
		}

		// Move on to the "register" action,
		// to send the signup request to the key server.
		action = "register"
	}

	// Look for a config file.
	if !exists(flags.Config) {
		// Config doesn't exist; need to sign up.
		return &startupResponse{
			Step: "signup",
		}, nil, nil
	}
	cfg, err := config.FromFile(flags.Config)
	if err != nil {
		return nil, nil, err
	}

	if action == "register" {
		if err := signup.MakeRequest(signupURL, cfg); err != nil {
			if keyDir != "" {
				// We have just generated the keys, so we
				// should remove both the keys and the config,
				// since they are bad. TODO(adg): really think
				// about this carefully!
				os.RemoveAll(keyDir)
				os.Remove(flags.Config)
			}
			return nil, nil, err
		}
		next := "verify"
		if secretSeed != "" {
			// Show the secret seed if we have just generated the key.
			next = "secretseed"
		}
		return &startupResponse{
			Step:       next,
			KeyDir:     keyDir,
			SecretSeed: secretSeed,
			UserName:   cfg.UserName(),
		}, nil, nil
	}

	// Is the user now registered with the KeyServer?
	if ok, err := isRegistered(cfg); err != nil {
		return nil, nil, err
	} else if !ok {
		// TODO: Read seed from secret.upspinkey?
		return &startupResponse{
			Step:     "verify",
			UserName: cfg.UserName(),
		}, nil, nil
	}

	// Make the user's root if it doesn't exist.
	if err := makeRoot(cfg); err != nil {
		return nil, nil, err
	}

	// We have a valid config. Set it in the server struct so that the
	// other methods can use it.
	s.mu.Lock()
	s.cfg = cfg
	s.cli = client.New(cfg)
	s.mu.Unlock()

	// Start cache if necessary.
	cacheutil.Start(cfg)
	return nil, cfg, nil
}

// keygen runs 'upspin keygen', placing keys in the default directory for the
// given user. It returns the secret seed for the keys and the key directory.
// If the default key directory already exists, keygen return an error.
func keygen(user upspin.UserName) (seed, keyDir string, err error) {
	keyDir, err = config.DefaultSecretsDir(user)
	if err != nil {
		return "", "", err
	}
	if exists(keyDir) {
		return "", "", errors.Errorf("cannot generate keys in %s: directory already exists", keyDir)
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

// writeConfig writes an Upspin config to the nominated file containing the
// provided user name and endpoint addresses. It will fail if file exists.
func writeConfig(file string, user upspin.UserName, dir, store upspin.NetAddr) error {
	if exists(file) {
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
	cfg += "cache: yes\n" // TODO(adg): make this configurable?
	return ioutil.WriteFile(file, []byte(cfg), 0644)
}

// isRegistered reports whether the user in the given config is present on the
// default KeyServer.
func isRegistered(cfg upspin.Config) (bool, error) {
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

// makeRoot creates the root for the user in the given config on the DirServer
// in the given config. If no DirServer endpoint is present in the config, or
// if the root already exists, it does nothing.
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

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
