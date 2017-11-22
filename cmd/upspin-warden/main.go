package main

import (
	"bytes"
	"net/http"
	"os/exec"
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
	cmd := exec.Command(p.name, append([]string{"-log=debug"}, p.args...)...)
	cmd.Stdout = &p.stdout
	cmd.Stderr = &p.stderr
	return cmd.Run()
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
