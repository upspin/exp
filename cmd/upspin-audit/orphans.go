// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"upspin.io/errors"
	"upspin.io/upspin"
)

// TODO:
// - write resulting data to files
// - check timestamps for correct order of operations
//   for garbage collection, scanstore before scandir
//   for tree verfication, scandir before scanstore
//   - add a flag to say which mode you're in

func (s *State) orphans(args []string) {
	const help = `
Audit orphans analyses previously collected scandir and scanstore runs
and summarizes 
`
	fs := flag.NewFlagSet("orphans", flag.ExitOnError)
	dataDir := dataDirFlag(fs)
	s.ParseFlags(fs, args, help, "audit orphans")

	if fs.NArg() != 0 || fs.Arg(0) == "help" {
		fs.Usage()
		os.Exit(2)
	}

	// Iterate through the files in dataDir and collect a set of the latest
	// files for each dir endpoint/tree and store endpoint.
	type latestKey struct {
		Addr upspin.NetAddr
		User upspin.UserName // empty for store
	}
	type fileInfo struct {
		Name string
		Addr upspin.NetAddr
		User upspin.UserName // empty for store
		Time time.Time
	}
	latest := make(map[latestKey]fileInfo)

	files, err := filepath.Glob(filepath.Join(*dataDir, "*"))
	if err != nil {
		s.Exit(err)
	}
	for _, file := range files {
		fi := fileInfo{Name: file}

		file = filepath.Base(file)
		isDir := strings.HasPrefix(file, dirFilePrefix)
		if !isDir && !strings.HasPrefix(file, storeFilePrefix) {
			continue
		}
		if isDir {
			file = strings.TrimPrefix(file, dirFilePrefix)
		} else {
			file = strings.TrimPrefix(file, storeFilePrefix)
		}

		i := strings.Index(file, "_")
		if i < 0 {
			continue
		}
		fi.Addr = upspin.NetAddr(file[:i])
		file = file[i+1:]

		if isDir {
			i := strings.LastIndex(file, "_")
			if i < 0 {
				continue
			}
			fi.User = upspin.UserName(file[:i])
			file = file[i+1:]
		}

		ts, err := strconv.ParseInt(file, 10, 64)
		if err != nil {
			continue
		}
		fi.Time = time.Unix(ts, 0)

		k := latestKey{
			Addr: fi.Addr,
			User: fi.User,
		}
		if cur, ok := latest[k]; ok && cur.Time.After(fi.Time) {
			continue
		}
		latest[k] = fi
	}

	fmt.Println("Found data for these store endpoints: (scanstore output)")
	for _, fi := range latest {
		if fi.User == "" {
			fmt.Printf("\t%s\n", fi.Addr)
		}
	}
	fmt.Println("Found data for these user trees and store endpoints: (scandir output)")
	for _, fi := range latest {
		if fi.User != "" {
			fmt.Printf("\t%s\t%s\n", fi.Addr, fi.User)
		}
	}
	fmt.Println()

	// Look for orphaned references and summarize them.
	for _, store := range latest {
		if store.User != "" {
			continue // Ignore dirs.
		}
		storeItems, err := s.readItems(store.Name)
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
			users = append(users, string(dir.User))
			dirItems, err := s.readItems(dir.Name)
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
				fmt.Printf("Store %q missing %d references present in %q.", store.Addr, len(storeMissing), dir.User)
			}
		}
		if len(dirsMissing) > 0 {
			fmt.Printf("Store %q contains %d references not present in these trees:\n\t%s\n", store.Addr, len(dirsMissing), strings.Join(users, "\n\t"))
		}
	}
}

func (s *State) readItems(file string) (map[upspin.Reference]int64, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	items := make(map[upspin.Reference]int64)
	for sc.Scan() {
		line := sc.Text()
		i := strings.LastIndex(line, " ")
		if i < 0 {
			return nil, errors.Errorf("malformed line in %q: %q", file, line)
		}
		quotedRef, sizeString := line[:i], line[i+1:]

		ref, err := strconv.Unquote(quotedRef)
		if err != nil {
			return nil, errors.Errorf("malformed ref in %q: %v", file, err)
		}
		size, err := strconv.ParseInt(sizeString, 10, 64)
		if err != nil {
			return nil, errors.Errorf("malformed size in %q: %v", file, err)
		}
		items[upspin.Reference(ref)] = size
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
