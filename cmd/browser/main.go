package main

import (
	"encoding/json"
	"log"
	"net/http"

	"upspin.io/client"
	"upspin.io/cloud/https"
	"upspin.io/config"
	"upspin.io/flags"
	"upspin.io/upspin"

	_ "upspin.io/transports"
)

func main() {
	flags.Parse(flags.Server)

	cfg, err := config.FromFile(flags.Config)
	if err != nil {
		log.Fatal(err)
	}

	http.Handle("/", newServer(cfg))
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
	var resp interface{}
	switch r.FormValue("method") {
	case "lookup":

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
	}
	if err := json.NewEncoder(w).Encode(&resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
