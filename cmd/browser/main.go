// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command browser presents a web interface to the Upspin name space.
// It operates as the user in the specified config.
package main

// TODO(adg): Flesh out the inspector (show blocks, etc).
// TODO(adg): Drag and drop support.
// TODO(adg): Secure the web UI; only allow the local user to access it.
// TODO(adg): Update the URL in the browser window to reflect the UI.
// TODO(adg): Facility to add/edit Access files in UI.
// TODO(adg): Awareness of Access files during copy and remove.

import (
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"

	"golang.org/x/net/xsrftoken"

	"upspin.io/client"
	"upspin.io/cloud/https"
	"upspin.io/config"
	"upspin.io/flags"
	"upspin.io/upspin"

	_ "upspin.io/transports"
)

var upspinfs = flag.String("upspinfs", "/u", "upspinfs `mount point`")

func main() {
	flags.Parse(flags.Server)

	cfg, err := config.FromFile(flags.Config)
	if err != nil {
		log.Fatal(err)
	}
	if err != nil {
		log.Fatal(err)
	}
	s, err := newServer(cfg)

	http.Handle("/_upspin", s)
	http.Handle("/static/", http.FileServer(http.Dir(".")))

	https.ListenAndServeFromFlags(nil)
}

type server struct {
	cfg     upspin.Config
	cli     upspin.Client
	xsrfKey string
}

func newServer(cfg upspin.Config) (http.Handler, error) {
	key, err := generateKey()
	if err != nil {
		return nil, err
	}
	return &server{
		cfg:     cfg,
		cli:     client.New(cfg),
		xsrfKey: key,
	}, nil
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	method := r.FormValue("method")

	// Validate XSRF token.
	if method != "whoami" && !xsrftoken.Valid(r.FormValue("token"), s.xsrfKey, string(s.cfg.UserName()), "") {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}

	var resp interface{}
	switch method {
	case "whoami":
		user := string(s.cfg.UserName())
		token := xsrftoken.Generate(s.xsrfKey, user, "")
		resp = struct {
			UserName string
			Token    string
			Upspinfs string
		}{user, token, *upspinfs}
	case "list":
		des, err := s.cli.Glob(r.FormValue("path") + "/*")
		var errString string
		if err != nil {
			errString = err.Error()
		}
		resp = struct {
			Entries []*upspin.DirEntry
			Error   string
		}{Entries: des, Error: errString}
	case "mkdir":
		_, err := s.cli.MakeDirectory(upspin.PathName(r.FormValue("path")))
		var errString string
		if err != nil {
			errString = err.Error()
		}
		resp = struct {
			Error string
		}{Error: errString}
	case "rm":
		var errString string
		for _, p := range r.Form["paths[]"] {
			if err := s.rm(upspin.PathName(p)); err != nil {
				errString = err.Error()
				break
			}
		}
		resp = struct {
			Error string
		}{Error: errString}
	case "copy":
		dst := upspin.PathName(r.FormValue("dest"))
		var paths []upspin.PathName
		for _, p := range r.Form["paths[]"] {
			paths = append(paths, upspin.PathName(p))
		}
		var errString string
		if err := s.copyPaths(dst, paths); err != nil {
			errString = err.Error()
		}
		resp = struct {
			Error string
		}{Error: errString}
	}
	b, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(b)
}

func generateKey() (string, error) {
	b := make([]byte, 8)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}
