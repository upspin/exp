// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// TODO(adg): Support automatically provisioning an upspinserver instance.

package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

// noneEndpoint is a sentinel Endpoint value that should be passed to
// writeConfig when we wish to set the dirserver and/or storeserver
// specifically to 'unassigned', to distinguish from the zero value.
// The NetAddr "none" is not written to the config file.
var noneEndpoint = upspin.Endpoint{
	Transport: upspin.Unassigned,
	NetAddr:   "none",
}

// startupResponse is sent to the client in response to startup requests.
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
//  - Check that the user has endpoints defined in the config file. If not:
//    - Prompt the user to choose dir/store endpoints, or none. (Step: "serverSelect")
//      TODO(adg): GCP project creation
//    - Update the user's endpoints in the keyserver record.
//    - Rewrite the config file with the chosen endpoints.
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
		userName := upspin.UserName(req.FormValue("username"))
		if err := valid.UserName(userName); err != nil {
			return nil, nil, err
		}

		// Check whether userName already exists on the KeyServer.
		userCfg := config.SetUserName(config.New(), userName)
		if ok, err := isRegistered(userCfg); err != nil {
			return nil, nil, err
		} else if ok {
			return nil, nil, errors.Errorf("%q is already registered.", userName)
		}

		// Write config file.
		err := writeConfig(flags.Config, userName, upspin.Endpoint{}, upspin.Endpoint{}, false)
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

	switch action {
	case "register":
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

	case "specifyEndpoints":
		dirHost := req.FormValue("dirServer")
		dirEndpoint, err := hostnameToEndpoint(dirHost)
		if err != nil {
			return nil, nil, errors.Errorf("invalid hostname %q: %v", dirHost, err)
		}
		storeHost := req.FormValue("storeServer")
		storeEndpoint, err := hostnameToEndpoint(storeHost)
		if err != nil {
			return nil, nil, errors.Errorf("invalid hostname %q: %v", storeHost, err)
		}

		dir, err := bind.DirServer(cfg, dirEndpoint)
		if err != nil {
			return nil, nil, errors.Errorf("could not find %q:\n%v", dirHost, err)
		}
		store, err := bind.StoreServer(cfg, storeEndpoint)
		if err != nil {
			return nil, nil, errors.Errorf("could not find %q:\n%v", storeHost, err)
		}

		// Check that the StoreServer is up.
		_, _, _, err = store.Get("Upspin:notexist")
		if err != nil && !errors.Match(errors.E(errors.NotExist), err) {
			return nil, nil, errors.Errorf("error communicating with %q:\n%v", storeHost, err)
		}
		cfg = config.SetStoreEndpoint(cfg, storeEndpoint)

		// Check that the DirServer is up, and create the user root.
		makeRoot := false
		root := upspin.PathName(cfg.UserName())
		_, err = dir.Lookup(root)
		if errors.Match(errors.E(errors.NotExist), err) {
			makeRoot = true
		} else if err != nil {
			return nil, nil, errors.Errorf("error communicating with %q:\n%v", dirHost, err)
		}
		cfg = config.SetDirEndpoint(cfg, dirEndpoint)
		if makeRoot {
			_, err = client.New(cfg).MakeDirectory(root)
			if err != nil {
				return nil, nil, errors.Errorf("error creating Upspin root:\n%v", err)
			}
		}

		// Put the updated user record to the key server.
		if err := putUser(cfg); err != nil {
			return nil, nil, errors.Errorf("error updating key server:\n%v", err)
		}

		// Write config file with updated endpoints.
		err = writeConfig(flags.Config, cfg.UserName(), dirEndpoint, storeEndpoint, true)
		if err != nil {
			return nil, nil, err
		}

	case "specifyNoEndpoints":
		cfg = config.SetDirEndpoint(cfg, noneEndpoint)
		cfg = config.SetStoreEndpoint(cfg, noneEndpoint)

		// Write config file with updated "none" endpoints.
		err = writeConfig(flags.Config, cfg.UserName(), noneEndpoint, noneEndpoint, true)
		if err != nil {
			return nil, nil, err
		}
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

	// If the user has not specified an endpoint (including 'unassigned')
	// in their config file, prompt them to select Upspin servers.
	if ok, err := hasEndpoints(flags.Config); err != nil {
		return nil, nil, err
	} else if !ok && cfg.DirEndpoint() == (upspin.Endpoint{}) {
		return &startupResponse{
			Step: "serverSelect",
		}, nil, nil
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
// provided user name and endpoints.
// It will fail if file exists and allowOverwrite is false.
func writeConfig(file string, user upspin.UserName, dir, store upspin.Endpoint, allowOverwrite bool) error {
	if exists(file) && !allowOverwrite {
		return errors.Errorf("cannot write %s: file already exists", file)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		return err
	}
	cfg := fmt.Sprintf("username: %s\n", user)
	if dir != (upspin.Endpoint{}) {
		cfg += fmt.Sprintf("dirserver: %s\n", dir)
	}
	if store != (upspin.Endpoint{}) {
		cfg += fmt.Sprintf("storeserver: %s\n", store)
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

// putUser updates the key server with the user name, endpoints, and public key
// in the given config.
func putUser(cfg upspin.Config) error {
	f := cfg.Factotum()
	if f == nil {
		return errors.E(cfg.UserName(), errors.Str("user has no keys"))
	}
	newU := upspin.User{
		Name:      cfg.UserName(),
		Dirs:      []upspin.Endpoint{cfg.DirEndpoint()},
		Stores:    []upspin.Endpoint{cfg.StoreEndpoint()},
		PublicKey: f.PublicKey(),
	}

	key, err := bind.KeyServer(cfg, cfg.KeyEndpoint())
	if err != nil {
		return err
	}
	usercache.ResetGlobal() // Avoid hitting the local user cache.
	oldU, err := key.Lookup(cfg.UserName())
	if err != nil {
		return err
	}
	if reflect.DeepEqual(*oldU, newU) {
		// Don't do anything if we're not changing anything.
		return nil
	}
	return key.Put(&newU)
}

// hasEndpoints reports whether the given config file contains a dirserver
// endpoint. It is only a rough test (it doesn't actually parse the YAML) and
// should be used in concert with a check against the parsed config.
func hasEndpoints(configFile string) (bool, error) {
	b, err := ioutil.ReadFile(configFile)
	if err != nil {
		return false, err
	}
	return bytes.Contains(b, []byte("\ndirserver:")), nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func hostnameToEndpoint(hostname string) (upspin.Endpoint, error) {
	if !strings.Contains(hostname, ":") {
		hostname += ":443"
	}
	host, port, err := net.SplitHostPort(hostname)
	if err != nil {
		return upspin.Endpoint{}, err
	}
	return upspin.Endpoint{
		Transport: upspin.Remote,
		NetAddr:   upspin.NetAddr(host + ":" + port),
	}, nil
}
