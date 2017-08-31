// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command browser presents a web interface to the Upspin name space.
// It operates as the user in the specified config.
// It is still in its early stages of development and should be used with care.
package main // import "exp.upspin.io/cmd/browser"

// TODO(adg): Flesh out the inspector (show blocks, etc).
// TODO(adg): Drag and drop support.
// TODO(adg): Secure the web UI; only allow the local user to access it.
// TODO(adg): Update the URL in the browser window to reflect the UI.
// TODO(adg): Facility to add/edit Access files in UI.
// TODO(adg): Awareness of Access files during copy and remove.
// TODO(adg): Show progress of removes/copies in the user interface.

import (
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"go/build"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strings"
	"sync"

	"upspin.io/errors"
	"upspin.io/flags"
	"upspin.io/upspin"

	_ "upspin.io/transports"
)

func main() {
	httpAddr := flag.String("http", "localhost:8000", "HTTP listen `address` (must be loopback)")
	flags.Parse(flags.Client)

	// Disallow listening on non-loopback addresses until we have a better
	// security model. (Even this is not really secure enough.)
	if err := isLocal(*httpAddr); err != nil {
		exit(err)
	}

	s, err := newServer()
	if err != nil {
		exit(err)
	}
	http.Handle("/", s)

	l, err := net.Listen("tcp", *httpAddr)
	if err != nil {
		exit(err)
	}
	url := fmt.Sprintf("http://%s/#token=%s", *httpAddr, s.xsrfToken)
	if !startBrowser(url) {
		fmt.Printf("Open %s in your web browser.\n", url)
	} else {
		fmt.Printf("Serving at %s\n", url)
	}
	exit(http.Serve(l, nil))
}

func exit(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// server implements an http.Handler that performs various Upspin operations
// using a config. It is the back end for the JavaScript Upspin browser.
type server struct {
	xsrfToken string       // Random token to prevent cross-site request forgery.
	static    http.Handler // Handler for serving static content (HTML, JS, etc).

	mu  sync.Mutex
	cfg upspin.Config // Non-nil if signup flow has been completed.
	cli upspin.Client
}

func newServer() (*server, error) {
	token, err := generateToken()
	if err != nil {
		return nil, err
	}

	pkg, err := build.Default.Import("exp.upspin.io/cmd/browser/static", "", build.FindOnly)
	if err != nil {
		return nil, fmt.Errorf("could not find static web content: %v", err)
	}

	return &server{
		xsrfToken: token,
		static:    http.FileServer(http.Dir(pkg.Dir)),
	}, nil
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/_upspin" {
		s.serveAPI(w, r)
		return
	}
	if strings.Contains(p, "@") {
		s.serveContent(w, r)
		return
	}
	s.static.ServeHTTP(w, r)
}

func (s *server) serveContent(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("token") != s.xsrfToken {
		http.Error(w, "Invalid XSRF token", http.StatusForbidden)
		return
	}

	p := r.URL.Path[1:]
	name := upspin.PathName(p)
	de, err := s.cli.Lookup(name, true)
	if err != nil {
		httpError(w, err)
		return
	}
	f, err := s.cli.Open(name)
	if err != nil {
		httpError(w, err)
		return
	}
	http.ServeContent(w, r, path.Base(p), de.Time.Go(), f)
	f.Close()
}

func (s *server) serveAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	method := r.FormValue("method")

	s.mu.Lock()
	hasConfig := s.cfg != nil
	s.mu.Unlock()

	// Require a valid XSRF token.
	if r.FormValue("token") != s.xsrfToken {
		http.Error(w, "Invalid XSRF token", http.StatusForbidden)
		return
	}

	// Don't permit accesses of non-startup methods if there is no config
	// nor client; those methods need them.
	if method != "startup" && !hasConfig {
		http.Error(w, "No configuration", http.StatusBadRequest)
		return
	}

	var resp interface{}
	switch method {
	case "startup":
		sResp, cfg, err := s.startup(r)
		var errString string
		if err != nil {
			errString = err.Error()
		}
		var user string
		if cfg != nil {
			user = string(cfg.UserName())
		}
		resp = struct {
			Startup  *startupResponse
			UserName string
			Error    string
		}{sResp, user, errString}
	case "list":
		path := upspin.PathName(r.FormValue("path"))
		des, err := s.cli.Glob(upspin.AllFilesGlob(path))
		var errString string
		if err != nil {
			errString = err.Error()
		}
		resp = struct {
			Entries []*upspin.DirEntry
			Error   string
		}{des, errString}
	case "mkdir":
		_, err := s.cli.MakeDirectory(upspin.PathName(r.FormValue("path")))
		var errString string
		if err != nil {
			errString = err.Error()
		}
		resp = struct {
			Error string
		}{errString}
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
		}{errString}
	case "copy":
		dst := upspin.PathName(r.FormValue("dest"))
		var paths []upspin.PathName
		for _, p := range r.Form["paths[]"] {
			paths = append(paths, upspin.PathName(p))
		}
		var errString string
		if err := s.copy(dst, paths); err != nil {
			errString = err.Error()
		}
		resp = struct {
			Error string
		}{errString}
	}
	b, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(b)
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

// isLocal returns an error if the given address is not a loopback address.
func isLocal(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return fmt.Errorf("cannot listen on non-loopback address %q", addr)
		}
	}
	return nil
}

// ifError checks if the error is the expected one, and if so writes back an
// HTTP error of the corresponding code.
func ifError(w http.ResponseWriter, got error, want errors.Kind, code int) bool {
	if !errors.Match(errors.E(want), got) {
		return false
	}
	http.Error(w, http.StatusText(code), code)
	return true
}

func httpError(w http.ResponseWriter, err error) {
	// This construction sets the HTTP error to the first type that matches.
	switch {
	case ifError(w, err, errors.Private, http.StatusForbidden):
	case ifError(w, err, errors.Permission, http.StatusForbidden):
	case ifError(w, err, errors.NotExist, http.StatusNotFound):
	case ifError(w, err, errors.BrokenLink, http.StatusNotFound):
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// startBrowser tries to open the URL in a web browser,
// and reports whether it succeed.
func startBrowser(url string) bool {
	var args []string
	switch runtime.GOOS {
	case "darwin":
		args = []string{"open"}
	case "windows":
		args = []string{"cmd", "/c", "start"}
	default:
		args = []string{"xdg-open"}
	}
	cmd := exec.Command(args[0], append(args[1:], url)...)
	return cmd.Start() == nil
}
