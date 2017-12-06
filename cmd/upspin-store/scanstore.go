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

	"upspin.io/bind"
	"upspin.io/cloud/storage"
	"upspin.io/upspin"
)

// This file implements the storage scan. The storage.Lister API is efficient so
// no parallelism is required.

// TODO: For now we just print the total size.

func (s *State) scanStore(args []string) {
	const help = `
Store scanstore scans the storage server to identify all references.
For now it just prints the total storage they represent.`

	fs := flag.NewFlagSet("scanstore", flag.ExitOnError)
	dataDirectory := flag.String("data", filepath.Join(os.Getenv("HOME"), "upspin", "store"), "`directory` storing scan data")
	endpointFlag := flag.String("endpoint", "", "network `address` of storage server; default is from config")
	_ = dataDirectory
	s.ParseFlags(fs, args, help, "store scanstore [endpoint]")

	if fs.NArg() != 0 {
		fs.Usage()
		os.Exit(2)
	}

	endpoint := s.Config.StoreEndpoint()
	if *endpointFlag != "" {
		var err error
		ep, err := upspin.ParseEndpoint(*endpointFlag)
		if err != nil {
			s.Exit(err)
		}
		endpoint = *ep
	}

	store, err := bind.StoreServer(s.Config, endpoint)
	if err != nil {
		s.Fail(err)
		return
	}
	b, _, _, err := store.Get(upspin.ListRefsMetadata)
	if err != nil {
		s.Fail(err)
		return
	}
	var refs []storage.RefInfo
	err = json.Unmarshal(b, &refs)
	if err != nil {
		s.Fail(err)
		return
	}
	sum := int64(0)
	for _, r := range refs {
		sum += r.Size
	}
	fmt.Println(sum, "bytes total in storage")
}
