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

	fmt.Println("Dirs:")
	for _, fi := range latest {
		if fi.User != "" {
			fmt.Printf("\t%s\t%s\n", fi.Addr, fi.User)
		}
	}
	fmt.Println("Stores:")
	for _, fi := range latest {
		if fi.User == "" {
			fmt.Printf("\t%s\n", fi.Addr)
		}
	}
}

func (s *State) readItems(file string) (items []upspin.ListRefsItem, err error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
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
		items = append(items, upspin.ListRefsItem{Ref: upspin.Reference(ref), Size: size})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
