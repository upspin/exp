// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Upspin-audit provides subcommands for auditing storage consumption.
// It has several subcommands that should be used in a way yet to be
// determined.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"upspin.io/config"
	"upspin.io/errors"
	"upspin.io/flags"
	"upspin.io/subcmd"
	"upspin.io/transports"
	"upspin.io/upspin"
	"upspin.io/version"
)

const (
	timeFormat       = "2006-01-02 15:04:05"
	dirFilePrefix    = "dir_"
	storeFilePrefix  = "store_"
	orphanFilePrefix = "orphans_"
)

// fileInfo holds a description of a reference list file written by scanstore
// or scandir. It is derived from the name of the file, not its contents.
type fileInfo struct {
	Path string
	Addr upspin.NetAddr
	User upspin.UserName // empty for store
	Time time.Time
}

type State struct {
	*subcmd.State
}

const help = `Upspin-audit provides subcommands for auditing storage consumption.
It has subcommands scandir and scanstore to scan the directory and storage servers
and report the storage consumed by those servers.
The set of tools will grow.
`

func main() {
	const name = "audit"

	log.SetFlags(0)
	log.SetPrefix("upspin-audit: ")
	flag.Usage = usage
	flags.ParseArgsInto(flag.CommandLine, os.Args[1:], flags.Client, "version")

	if flags.Version {
		fmt.Fprint(os.Stdout, version.Version())
		os.Exit(2)
	}

	if flag.NArg() < 1 {
		usage()
	}
	s := &State{
		State: subcmd.NewState(name),
	}

	cfg, err := config.FromFile(flags.Config)
	if err != nil {
		s.Exit(err)
	}
	transports.Init(cfg)
	s.State.Init(cfg)

	switch flag.Arg(0) {
	case "scandir":
		s.scanDirectories(flag.Args()[1:])
	case "scanstore":
		s.scanStore(flag.Args()[1:])
	case "orphans":
		s.orphans(flag.Args()[1:])
	default:
		usage()
	}

	s.ExitNow()
}

func usage() {
	fmt.Fprintln(os.Stderr, help)
	fmt.Fprintln(os.Stderr, "Usage of upspin audit:")
	fmt.Fprintln(os.Stderr, "\tupspin [globalflags] audit <command> [flags] ...")
	fmt.Fprintln(os.Stderr, "\twhere <command> is one of scandir, scanstore")
	flag.PrintDefaults()
	os.Exit(2)
}

// dataDirFlag returns a string pointer bound to a new flag that specifies the data directory.
// Done here so the definition can be common among the commands.
func dataDirFlag(fs *flag.FlagSet) *string {
	var dataDir string
	fs.StringVar(&dataDir, "data", filepath.Join(os.Getenv("HOME"), "upspin", "audit"), "`directory` storing scan data")
	return &dataDir
}

// writeItems sorts and writes a list of reference/size pairs to file.
func (s *State) writeItems(file string, items []upspin.ListRefsItem) {
	sort.Slice(items, func(i, j int) bool { return items[i].Ref < items[j].Ref })

	f, err := os.Create(file)
	if err != nil {
		s.Exit(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			s.Exit(err)
		}
	}()
	w := bufio.NewWriter(f)
	for _, ri := range items {
		if _, err := fmt.Fprintf(w, "%q %d\n", ri.Ref, ri.Size); err != nil {
			s.Exit(err)
		}
	}
	if err := w.Flush(); err != nil {
		s.Exit(err)
	}
}

// readItems reads a list of reference/size pairs from the given file and
// returns them as a map. The asymmetry with writeItems, which takes a slice,
// is to fit the most common usage pattern.
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

func itemMapToSlice(m map[upspin.Reference]int64) (items []upspin.ListRefsItem) {
	for ref, size := range m {
		items = append(items, upspin.ListRefsItem{Ref: ref, Size: size})
	}
	return
}
