// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command upspin-warden runs Upspin client daemons, such as upspinfs and
// cacheserver, and exports information about them to external programs.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"upspin.io/flags"
	"upspin.io/log"
)

func main() {
	cmd := flag.String("cmd", "cacheserver,upspinfs,upspin-sharebot", "comma-separated list of `commands` to run")
	flags.Parse(nil, "log", "config", "http")
	w := NewWarden(strings.Split(*cmd, ","))
	log.Fatal(http.ListenAndServe(flags.HTTPAddr, w))
}

// restartInterval specifies the time between daemon restarts.
const restartInterval = 10 * time.Second

// Warden implements the upspin-warden daemon.
type Warden struct {
	log   rollingLog
	procs map[string]*Process
}

// NewWarden creates a Warden that runs the given commands.
// It implements a http.Handler that exports server state and logs.
// It redirects global Upspin log output to its internal rolling log.
func NewWarden(cmds []string) *Warden {
	w := &Warden{procs: map[string]*Process{}}
	for _, c := range cmds {
		w.procs[c] = &Process{name: c}
	}
	log.SetOutput(io.MultiWriter(os.Stderr, &w.log))
	for _, p := range w.procs {
		go p.Run()
	}
	return w
}

// ServeHTTP implements http.Handler.
func (w *Warden) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	switch name := r.URL.Path[1:]; name {
	case "": // Root.
		// Show truncated warden logs.
		fmt.Fprintln(rw, "warden:")
		fprintLastNLines(rw, w.log.Log(), 10, "\t")
		// Show processes, their states, and truncated logs.
		var names []string
		for n := range w.procs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p := w.procs[n]
			fmt.Fprintf(rw, "\n%s: %s\n", n, p.State())
			fprintLastNLines(rw, p.log.Log(), 10, "\t")
		}
	case "warden":
		// Show complete warden log.
		rw.Write(w.log.Log())
	default:
		// Show log for the given process.
		p, ok := w.procs[name]
		if !ok {
			http.NotFound(rw, r)
			return
		}
		rw.Write(p.log.Log())
	}
}

// fprintLastNLines writes the last n lines of buf to w,
// adding prefix to the start of each line.
func fprintLastNLines(w io.Writer, buf []byte, n int, prefix string) {
	lines := make([][]byte, 0, n)
	for i := 0; i <= n; i++ {
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

// ProcessState describes the state of a Process.
type ProcessState int

//go:generate stringer -type ProcessState

const (
	NotStarted ProcessState = iota
	Starting
	Running
	Error
)

// Process manages the execution of a daemon process and captures its logs.
type Process struct {
	name string
	log  rollingLog

	mu    sync.Mutex
	state ProcessState
}

// State reports the state of the process.
func (p *Process) State() ProcessState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// Run executes the process in a loop, restarting it after restartInterval
// since its last start.
func (p *Process) Run() {
	for {
		started := time.Now()
		err := p.exec()
		log.Error.Printf("%v: %v", p.name, err)
		if d := time.Since(started); d < restartInterval {
			i := restartInterval - d
			log.Debug.Printf("%v: waiting %v before restarting", p.name, i)
			time.Sleep(i)
		}
	}
}

// Exec starts the process and waits for it to return,
// updating the process's state field as necessary.
func (p *Process) exec() error {
	cmd := exec.Command(p.name,
		"-log="+flags.Log.String(),
		"-config="+flags.Config)
	cmd.Stdout = &p.log
	cmd.Stderr = &p.log
	p.setState(Starting)
	if err := cmd.Start(); err != nil {
		return err
	}
	p.setState(Running)
	err := cmd.Wait()
	p.setState(Error)
	return err
}

func (p *Process) setState(s ProcessState) {
	p.mu.Lock()
	p.state = s
	p.mu.Unlock()
	log.Debug.Printf("%s: %s", p.name, s)
}
