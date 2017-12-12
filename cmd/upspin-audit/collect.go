// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"os"
	"strings"

	"upspin.io/bind"
	"upspin.io/errors"
	"upspin.io/upspin"
)

func (s *State) collect(args []string) {
	const help = `
Collect deletes orphaned references as listed by the latest orphans file
for the store endpoint of the current user. Use with caution.
`
	fs := flag.NewFlagSet("collect", flag.ExitOnError)
	dataDir := dataDirFlag(fs)
	s.ParseFlags(fs, args, help, "audit collect")

	if fs.NArg() != 0 {
		fs.Usage()
		os.Exit(2)
	}

	for _, fi := range s.latestFilesWithPrefix(*dataDir, orphanFilePrefix) {
		if fi.Addr != s.Config.StoreEndpoint().NetAddr {
			continue
		}
		orphans, err := s.readItems(fi.Path)
		if err != nil {
			s.Exit(err)
		}
		store, err := bind.StoreServer(s.Config, s.Config.StoreEndpoint())
		if err != nil {
			s.Exit(err)
		}
		const numWorkers = 10
		c := collector{
			State: s,
			store: store,
			refs:  make(chan upspin.Reference),
			stop:  make(chan bool, numWorkers),
		}
		for i := 0; i < numWorkers; i++ {
			go c.worker()
		}
	loop:
		for ref := range orphans {
			if strings.HasPrefix(string(ref), rootRefPrefix) {
				// Don't ever collect root backups.
				continue
			}
			select {
			case c.refs <- ref:
			case <-c.stop:
				break loop
			}
		}
		close(c.refs)
	}
}

type collector struct {
	State *State
	store upspin.StoreServer
	refs  chan upspin.Reference
	stop  chan bool
}

func (c *collector) worker() {
	for ref := range c.refs {
		err := c.store.Delete(ref)
		if err != nil {
			c.State.Fail(err)
			// Stop the entire process if we get a permission error;
			// we likely are running as the wrong user.
			if errors.Is(errors.Permission, err) {
				c.stop <- true
				return
			}
		}
	}
}
