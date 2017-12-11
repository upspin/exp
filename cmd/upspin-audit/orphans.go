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
// - add a flag to run in reverse (not garbage collection mode)
// - add a -tidy flag to remove data from old scans (maybe tidy should be its own sub-command)

// fileInfo holds a description of a reference list file written by scanstore
// or scandir. It is derived from the name of the file, not its contents.
type fileInfo struct {
	Path string
	Addr upspin.NetAddr
	User upspin.UserName // empty for store
	Time time.Time
}

func (s *State) orphans(args []string) {
	const help = `
Audit orphans analyses previously collected scandir and scanstore runs and
finds references that are present in the store but missing from the scanned
directory trees, and vice versa.
`
	fs := flag.NewFlagSet("orphans", flag.ExitOnError)
	dataDir := dataDirFlag(fs)
	s.ParseFlags(fs, args, help, "audit orphans")

	if fs.NArg() != 0 || fs.Arg(0) == "help" {
		fs.Usage()
		os.Exit(2)
	}

	if err := os.MkdirAll(*dataDir, 0700); err != nil {
		s.Exit(err)
	}

	// Iterate through the files in dataDir and collect a set of the latest
	// files for each dir endpoint/tree and store endpoint.
	files, err := filepath.Glob(filepath.Join(*dataDir, "*"))
	if err != nil {
		s.Exit(err)
	}
	type latestKey struct {
		Addr upspin.NetAddr
		User upspin.UserName // empty for store
	}
	latest := make(map[latestKey]fileInfo)
	for _, file := range files {
		fi, err := filenameToFileInfo(file)
		if err == errIgnoreFile {
			continue
		}
		if err != nil {
			s.Exit(err)
		}
		k := latestKey{
			Addr: fi.Addr,
			User: fi.User,
		}
		if cur, ok := latest[k]; ok && cur.Time.After(fi.Time) {
			continue
		}
		latest[k] = fi
	}

	// Print a summary of the files we found.
	const timeFormat = "2006-01-02 15:04:05"
	fmt.Println("Found data for these store endpoints: (scanstore output)")
	for _, fi := range latest {
		if fi.User == "" {
			fmt.Printf("\t%s\t%s\n", fi.Time.Format(timeFormat), fi.Addr)
		}
	}
	fmt.Println("Found data for these user trees and store endpoints: (scandir output)")
	for _, fi := range latest {
		if fi.User != "" {
			fmt.Printf("\t%s\t%s\t%s\n", fi.Time.Format(timeFormat), fi.Addr, fi.User)
		}
	}
	fmt.Println()

	// Look for orphaned references and summarize them.
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
				s.Exitf("scanstore must be performed before all scandir operations\n"+
					"scandir output in\n\t%s\npredates scanstore output in\n\t%s",
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
				fmt.Printf("Store %q missing %d references present in %q.", store.Addr, len(storeMissing), dir.User)
				// TODO(adg): write these to a file
			}
		}
		if len(dirsMissing) > 0 {
			fmt.Printf("Store %q contains %d references not present in these trees:\n\t%s\n", store.Addr, len(dirsMissing), strings.Join(users, "\n\t"))
			file := filepath.Join(*dataDir, fmt.Sprintf("%s%s_%d", orphanFilePrefix, store.Addr, store.Time.Unix()))
			s.writeItems(file, itemMapToSlice(dirsMissing))
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

var errIgnoreFile = errors.Str("not a file we're interested in")

func filenameToFileInfo(file string) (fi fileInfo, err error) {
	fi.Path = file
	file = filepath.Base(file)
	s := file // We will consume this string.

	// Check and trim prefix.
	isDir := strings.HasPrefix(s, dirFilePrefix)
	if !isDir && !strings.HasPrefix(s, storeFilePrefix) {
		err = errIgnoreFile
		return
	}
	if isDir {
		s = strings.TrimPrefix(s, dirFilePrefix)
	} else {
		s = strings.TrimPrefix(s, storeFilePrefix)
	}

	// Collect and trim endpoint name.
	i := strings.Index(s, "_")
	if i < 0 {
		err = errors.Errorf("malformed file name %q", file)
		return
	}
	fi.Addr = upspin.NetAddr(s[:i])
	s = s[i+1:]

	// For dir files, collect and trim user name.
	if isDir {
		i := strings.LastIndex(s, "_")
		if i < 0 {
			err = errors.Errorf("malformed file name %q: missing user name", file)
			return
		}
		fi.User = upspin.UserName(s[:i])
		s = s[i+1:]
	}

	// Collect time stamp.
	ts, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		err = errors.Errorf("malformed file name %q: bad timestamp: %v", file, err)
		return
	}
	fi.Time = time.Unix(ts, 0)

	return
}
