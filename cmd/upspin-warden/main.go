// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command upspin-warden runs Upspin client daemons, such as upspinfs and
// cacheserver, and exports information about them to external programs.
package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"upspin.io/flags"
	"upspin.io/log"
)

func main() {
	flags.Parse(nil, "log", "config", "http")
	w := NewWarden()
	log.Fatal(http.ListenAndServe(flags.HTTPAddr, w))
}

const (
	restartInterval = 10 * time.Second
)

// Warden implements the
type Warden struct {
	log   rollingLog
	procs map[string]*Process
}

func NewWarden() *Warden {
	s := &Warden{
		procs: map[string]*Process{
			"cacheserver": &Process{name: "cacheserver"},
			"upspinfs":    &Process{name: "upspinfs", args: []string{"/u"}},
			"accessor":    &Process{name: "accessor"},
		},
	}
	log.SetOutput(io.MultiWriter(os.Stdout, &s.log))
	go s.run()
	return s
}

func (s *Warden) run() {
	for _, p := range s.procs {
		go p.run()
	}
}

func (s *Warden) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Path[1:]
	if name == "" {
		// List processes and their states.
		var names []string
		for n := range s.procs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p := s.procs[n]
			fmt.Fprintf(w, "%s: %s\n", n, p.State())
			fprintNLines(w, p.stderr.Log(), 10, "\t")
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, "warden:")
		fprintNLines(w, s.log.Log(), 10, "\t")
		return
	}
	if name == "log" {
		w.Write(s.log.Log())
		return
	}
	p, ok := s.procs[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Write(p.stderr.Log())
}

func fprintNLines(w io.Writer, buf []byte, n int, prefix string) {
	var lines [][]byte
	for i := 0; i < 10; i++ {
		j := bytes.LastIndexByte(buf, '\n')
		if j <= 0 {
			if len(buf) > 0 {
				lines = append(lines, buf)
			}
			break
		}
		lines = append(lines, buf[j+1:])
		buf = buf[:j]
	}
	for i := len(lines) - 1; i >= 0; i-- {
		fmt.Fprintf(w, "%s%s\n", prefix, lines[i])
	}
}

type Process struct {
	name   string
	args   []string
	stdout rollingLog
	stderr rollingLog

	mu    sync.Mutex
	state ProcessState
}

type ProcessState int

const (
	Stopped ProcessState = iota
	Starting
	Running
	Error
)

//go:generate stringer -type ProcessState

func (p *Process) setState(s ProcessState) {
	p.mu.Lock()
	p.state = s
	p.mu.Unlock()
	log.Debug.Printf("%s: %s", p.name, s)
}

func (p *Process) State() ProcessState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

func (p *Process) run() {
	for {
		started := time.Now()
		err := p.exec()
		log.Printf("%v: %v", p.name, err)
		if d := time.Since(started); d < restartInterval {
			i := restartInterval - d
			log.Printf("%v: waiting %v before restarting", p.name, i)
			time.Sleep(i)
		}
	}
}

func (p *Process) exec() error {
	args := append([]string{
		"-log=" + flags.Log.String(),
		"-config=" + flags.Config},
		p.args...)
	p.setState(Starting)
	cmd := exec.Command(p.name, args...)
	cmd.Stdout = &p.stdout
	cmd.Stderr = &p.stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	p.setState(Running)
	err := cmd.Wait()
	p.setState(Error)
	return err
}
