// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"upspin.io/upspin"
)

func (s *State) findGarbage(args []string) {
	const help = `
Audit find-garbage analyses the output of scan-dir and scan-store to finds
blocks that are present in the store server but not referred to by the scanned
directory trees.
`
	fs := flag.NewFlagSet("find-garbage", flag.ExitOnError)
	dataDir := dataDirFlag(fs)
	s.ParseFlags(fs, args, help, "audit find-garbage")

	if fs.NArg() != 0 {
		fs.Usage()
		os.Exit(2)
	}

	if err := os.MkdirAll(*dataDir, 0700); err != nil {
		s.Exit(err)
	}

	// Iterate through the files in dataDir and collect a set of the latest
	// files for each dir endpoint/tree and store endpoint.
	latest := s.latestFilesWithPrefix(*dataDir, storeFilePrefix, dirFilePrefix)

	// Print a summary of the files we found.
	nDirs, nStores := 0, 0
	fmt.Println("Found data for these store endpoints: (scan-store output)")
	for _, fi := range latest {
		if fi.User == "" {
			fmt.Printf("\t%s\t%s\n", fi.Time.Format(timeFormat), fi.Addr)
			nStores++
		}
	}
	if nStores == 0 {
		fmt.Println("\t(none)")
	}
	fmt.Println("Found data for these user trees and store endpoints: (scan-dir output)")
	for _, fi := range latest {
		if fi.User != "" {
			fmt.Printf("\t%s\t%s\t%s\n", fi.Time.Format(timeFormat), fi.Addr, fi.User)
			nDirs++
		}
	}
	if nDirs == 0 {
		fmt.Println("\t(none)")
	}
	fmt.Println()

	if nDirs == 0 || nStores == 0 {
		s.Exitf("nothing to do; run scan-store and scan-dir first")
	}

	// Look for garbage references and summarize them.
	for _, store := range latest {
		if store.User != "" {
			continue // Ignore dirs.
		}
		storeItems, err := s.readItems(store.Path)
		if err != nil {
			s.Exit(err)
		}
		dirsMissing := make(map[upspin.Reference]int64)
		for ref, size := range storeItems {
			dirsMissing[ref] = size
		}
		var users []string
		for _, dir := range latest {
			if dir.User == "" {
				continue // Ignore stores.
			}
			if store.Addr != dir.Addr {
				continue
			}
			if dir.Time.Before(store.Time) {
				s.Exitf("scan-store must be performed before all scan-dir operations\n"+
					"scan-dir output in\n\t%s\npredates scan-store output in\n\t%s",
					filepath.Base(dir.Path), filepath.Base(store.Path))
			}
			users = append(users, string(dir.User))
			dirItems, err := s.readItems(dir.Path)
			if err != nil {
				s.Exit(err)
			}
			storeMissing := make(map[upspin.Reference]int64)
			for ref, size := range dirItems {
				if _, ok := storeItems[ref]; !ok {
					storeMissing[ref] = size
				}
				delete(dirsMissing, ref)
			}
			if len(storeMissing) > 0 {
				fmt.Printf("Store %q missing %d references present in %q.\n", store.Addr, len(storeMissing), dir.User)
			}
		}
		if len(dirsMissing) > 0 {
			fmt.Printf("Store %q contains %d references not present in these trees:\n\t%s\n", store.Addr, len(dirsMissing), strings.Join(users, "\n\t"))
			file := filepath.Join(*dataDir, fmt.Sprintf("%s%s_%d", garbageFilePrefix, store.Addr, store.Time.Unix()))
			s.writeItems(file, itemMapToSlice(dirsMissing))
		}
	}
}
