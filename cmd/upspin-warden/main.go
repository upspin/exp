package main

import (
	"bytes"
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"sync"
	"time"

	"upspin.io/log"
)

func main() {
	s := &Server{
		procs: map[string]*Process{
			"cacheserver": &Process{name: "cacheserver"},
			"upspinfs":    &Process{name: "upspinfs", args: []string{"/u"}},
			"accessor":    &Process{name: "accessor"},
		},
	}
	go s.run()
	log.Fatal(http.ListenAndServe("localhost:9999", s))
}

const (
	restartInterval = 10 * time.Second
	maxBacklog      = 64 * 1024
)

type ProcessState int

const (
	Stopped ProcessState = iota
	Starting
	Running
	Error
)

//go:generate stringer -type ProcessState

type Server struct {
	procs map[string]*Process
}

func (s *Server) run() {
	for _, p := range s.procs {
		go p.babysit()
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		}
		return
	}
	p, ok := s.procs[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Write(p.stderr.Log())
}

type Process struct {
	name   string
	args   []string
	stdout rollingLog
	stderr rollingLog

	mu    sync.Mutex
	state ProcessState
}

func (p *Process) setState(s ProcessState) {
	p.mu.Lock()
	p.state = s
	p.mu.Unlock()
}

func (p *Process) State() ProcessState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

func (p *Process) babysit() {
	for {
		started := time.Now()
		err := p.run()
		log.Printf("%v: %v", p.name, err)
		if d := time.Since(started); d < restartInterval {
			i := restartInterval - d
			log.Printf("%v: waiting %v before restarting", p.name, i)
			time.Sleep(i)
		}
	}
}

func (p *Process) run() error {
	p.setState(Starting)
	cmd := exec.Command(p.name, append([]string{"-log=debug"}, p.args...)...)
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

type rollingLog struct {
	mu  sync.Mutex
	buf []byte
}

func (l *rollingLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buf)+len(b) > maxBacklog {
		b2 := l.buf[:0]
		if len(b) <= maxBacklog {
			b2 = l.buf[:maxBacklog-len(b)]
		}
		if i := bytes.IndexByte(b2, '\n'); i > 0 {
			b2 = b2[i+1:]
		}
		l.buf = append(l.buf[:0], b2...)
	}
	l.buf = append(l.buf, b...)
	return len(b), nil
}

func (l *rollingLog) Log() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.buf...)
}
