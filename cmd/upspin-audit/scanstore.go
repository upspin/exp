// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"upspin.io/bind"
	"upspin.io/upspin"
)

// This file implements the storage scan.

// TODO: For now we just print the total size.

func (s *State) scanStore(args []string) {
	const help = `
Audit scanstore scans the storage server to identify all references.
By default it scans the storage server mentioned in the config file.
For now it just prints the total storage they represent.`

	fs := flag.NewFlagSet("scanstore", flag.ExitOnError)
	endpointFlag := fs.String("endpoint", string(s.Config.StoreEndpoint().NetAddr), "network `address` of storage server; default is from config")
	dataDir := dataDirFlag(fs)
	s.ParseFlags(fs, args, help, "audit scanstore [-endpoint <storeserver address>]")

	if fs.NArg() != 0 { // "audit scanstore help" is covered by this.
		fs.Usage()
		os.Exit(2)
	}

	if err := os.MkdirAll(*dataDir, 0600); err != nil {
		s.Exit(err)
	}

	endpoint, err := upspin.ParseEndpoint("remote," + *endpointFlag)
	if err != nil {
		s.Exit(err)
	}

	now := time.Now()

	store, err := bind.StoreServer(s.Config, *endpoint)
	if err != nil {
		s.Fail(err)
		return
	}
	var (
		token string
		sum   int64
		items []upspin.ListRefsItem
	)
	for {
		b, _, _, err := store.Get(upspin.ListRefsMetadata + upspin.Reference(token))
		if err != nil {
			s.Exit(err)
			return
		}
		var refs upspin.ListRefsResponse
		err = json.Unmarshal(b, &refs)
		if err != nil {
			s.Exit(err)
			return
		}
		for _, ri := range refs.Refs {
			sum += ri.Size
			items = append(items, ri)
		}
		token = refs.Next
		if token == "" {
			break
		}
	}
	fmt.Printf("%s: %d bytes total (%s) in %d references\n", endpoint.NetAddr, sum, ByteSize(sum), len(items))
	file := filepath.Join(*dataDir, fmt.Sprintf("store.%s.%d", endpoint.NetAddr, now.Unix()))
	s.writeItems(file, items)
}
