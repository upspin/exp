// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

func TestRollingLog(t *testing.T) {
	oldMax := maxBacklog
	defer func() { maxBacklog = oldMax }()
	maxBacklog = 1024
	l := rollingLog{}

	for i := 0; i < 2000; i++ {
		n := rand.Intn(100)
		fmt.Fprintf(&l, "%.2d%s\n", n, strings.Repeat("n", n))
		err := validate(l.Log())
		if err != nil {
			t.Fatalf("iteration %d: %v", n, err)
		}
	}

	// Write a >maxBacklog string of m's and n's;
	// it should just replace the log.
	mm := strings.Repeat("m", 512)
	nn := strings.Repeat("n", 512)
	want := fmt.Sprintf("%s\n%s\n", mm, nn)
	l.Write([]byte(want))
	if got := string(l.Log()); got != want {
		t.Fatalf("mismatch after long write\ngot %d bytes: %q\nwant %d bytes: %q",
			len(got), got, len(want), want)
	}

	// Now this next write should leave us with
	// the run of n's followed by "hello".
	s := "hello\n"
	l.Write([]byte(s))
	want = fmt.Sprintf("%s\n%s", nn, s)
	if got := string(l.Log()); got != want {
		t.Fatalf("mismatch after short write after long write\ngot %d bytes: %q\nwant %d bytes: %q",
			len(got), got, len(want), want)
	}
}

func validate(b []byte) error {
	lines := bytes.Split(b, []byte("\n"))
	for i, l := range lines {
		if len(l) == 0 {
			if i != len(lines)-1 {
				return fmt.Errorf("found empty line mid-log at %d", i)
			}
			return nil
		}
		if len(l) < 2 {
			return fmt.Errorf("line %d too short", i)
		}
		n, err := strconv.Atoi(string(l[:2]))
		if err != nil {
			return fmt.Errorf("invalid length of line %d: %v", i, err)
		}
		if !bytes.Equal(l[2:], bytes.Repeat([]byte("n"), n)) {
			return fmt.Errorf("bad line %d: %q", i, l)
		}
	}
	return nil
}
