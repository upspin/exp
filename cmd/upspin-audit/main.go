// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Upspin-audit provides subcommands for auditing storage consumption.
// It has several subcommands that should be used in a way yet to be
// determined.
package main

// TODO:
// - add failsafes to avoid misuse of delete-garbage
// - add a command that is the reverse of find-garbage (find-missing?)
// - add a tidy command to remove data from old scans

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
	timeFormat    = "2006-01-02 15:04:05"
	rootRefPrefix = "tree.root."

	dirFilePrefix     = "dir_"
	storeFilePrefix   = "store_"
	garbageFilePrefix = "garbage_"
)

type State struct {
	*subcmd.State
}

const help = `Upspin-audit provides subcommands for auditing storage consumption.

The subcommands are:

scan-dir
scan-store
	Scan the directory and store servers and report the storage consumed
	by those servers.

find-garbage
	Use the results of scan-dir and scan-store operations to create a list
	of references that are present in a store server but not referenced
	by the scanned directory servers.

delete-garbage
	Delete the references found by find-garbage from the store server.

To delete the garbage references in a given store server:
1. Run scan-store (as the store server user) to generate a list of references
   in the store server.
2. Run scan-dir for each Upspin tree that stores data in the store server (as
   the Upspin users that own those trees) to generate lists of references
   referred to by those trees.
3. Run find-garbage to compile a list of references that are in the scan-store
   output but not in the combined output of the scan-dir runs.
4. Run delete-garbage (as the store server user) to delete the references in
   the find-garbage output.
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
	case "scan-dir":
		s.scanDirectories(flag.Args()[1:])
	case "scan-store":
		s.scanStore(flag.Args()[1:])
	case "find-garbage":
		s.findGarbage(flag.Args()[1:])
	case "delete-garbage":
		s.deleteGarbage(flag.Args()[1:])
	default:
		usage()
	}

	s.ExitNow()
}

func usage() {
	fmt.Fprintln(os.Stderr, help)
	fmt.Fprintln(os.Stderr, "Usage of upspin audit:")
	fmt.Fprintln(os.Stderr, "\tupspin [globalflags] audit <command> [flags] ...")
	fmt.Fprintln(os.Stderr, "Commands: scan-dir, scan-store, find-garbage, delete-garbage")
	fmt.Fprintln(os.Stderr, "Global flags:")
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

// fileInfo holds a description of a reference list file written by scan-store
// or scan-dir. It is derived from the name of the file, not its contents.
type fileInfo struct {
	Path string
	Addr upspin.NetAddr
	User upspin.UserName // empty for store
	Time time.Time
}

// latestFilesWithPrefix returns the most recently generated files in dir that
// have that have the given prefixes.
func (s *State) latestFilesWithPrefix(dir string, prefixes ...string) (files []fileInfo) {
	paths, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		s.Exit(err)
	}
	type latestKey struct {
		Addr upspin.NetAddr
		User upspin.UserName // empty for store
	}
	latest := make(map[latestKey]fileInfo)
	for _, file := range paths {
		fi, err := filenameToFileInfo(file, prefixes...)
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
	for _, fi := range latest {
		files = append(files, fi)
	}
	return files
}

// errIgnoreFile is returned from filenameToFileInfo to signal that the given
// file name is not one generated by scan-dir or scan-store. It should be handled
// by callers to filenameToFileInfo and is not to be seen by users.
var errIgnoreFile = errors.Str("not a file we're interested in")

// filenameToFileInfo takes a file name generated by scan-dir or scan-store and
// returns the information held by that file name as a fileInfo.
func filenameToFileInfo(file string, prefixes ...string) (fi fileInfo, err error) {
	fi.Path = file
	file = filepath.Base(file)
	s := file // We will consume this string.

	// Check and trim prefix.
	ok := false
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			s = strings.TrimPrefix(s, p)
			ok = true
			break
		}
	}
	if !ok {
		err = errIgnoreFile
		return
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
	if strings.HasPrefix(file, dirFilePrefix) {
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
