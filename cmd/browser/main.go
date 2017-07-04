package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"

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

	http.Handle("/_upspin", newServer(cfg))
	http.Handle("/static/", http.FileServer(http.Dir(".")))

	https.ListenAndServeFromFlags(nil)
}

type server struct {
	cfg upspin.Config
	cli upspin.Client
}

func newServer(cfg upspin.Config) http.Handler {
	return &server{
		cfg: cfg,
		cli: client.New(cfg),
	}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var resp interface{}
	switch r.FormValue("method") {
	case "whoami":
		resp = struct {
			UserName upspin.UserName
			Upspinfs string
		}{s.cfg.UserName(), *upspinfs}
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
		// TODO(adg): remove recursively
		r.ParseForm()
		var errString string
		for _, p := range r.Form["paths[]"] {
			err := s.cli.Delete(upspin.PathName(p))
			if err != nil {
				errString = err.Error()
				break
			}
		}
		resp = struct {
			Error string
		}{Error: errString}
	case "copy":
	}
	if err := json.NewEncoder(w).Encode(&resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
