// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"sync"
)

var maxBacklog = 64 * 1024 // Tests override this.

// rollingLog is an io.Writer that buffers all data written to it, purging
// earlier entries to maintain a buffer size of maxBacklog bytes.
// Its methods are safe for concurrent use.
type rollingLog struct {
	mu  sync.Mutex
	buf []byte
}

func (l *rollingLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(b) >= maxBacklog {
		l.buf = append(l.buf[:0], b...)
		return len(b), nil
	}
	if len(l.buf)+len(b) > maxBacklog {
		// Make room for b.
		i := len(b)
		if len(l.buf) > maxBacklog {
			i += len(l.buf) - maxBacklog
		}
		b2 := l.buf[i:]
		// Start at the first line feed,
		// so that we don't keep partial lines.
		if i := bytes.IndexByte(b2, '\n'); i >= 0 {
			b2 = b2[i+1:]
		}
		// Replace buffer.
		l.buf = append(l.buf[:0], b2...)
	}
	l.buf = append(l.buf, b...)
	return len(b), nil
}

// Log returns a copy of the log buffer.
func (l *rollingLog) Log() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.buf...)
}
