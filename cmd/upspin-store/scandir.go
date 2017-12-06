// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"sync"

	"upspin.io/path"
	"upspin.io/upspin"
)

// This file implements the directory scan. Because the network time of flight is
// significant to throughput, the scan is parallelized, which makes the code
// more intricate than we'd like.
// The code actually walks the directory tree using Glob. We could in principle
// use Watch(-1), but snapshots are problematic for Watch. We take care to
// avoid scanning a directory we've already seen, which Watch doesn't do on
// the server. Our code makes it practical to scan the snapshot tree.

// TODO: For now we just print the total size.

const scanParallelism = 10 // Empirically chosen: speedup significant, not too many resources.

type DirScanner struct {
	State    *State
	inFlight sync.WaitGroup        // Count of directories we have seen but not yet processed.
	buffer   chan *upspin.DirEntry // Where to send directories for processing.
	dirsToDo chan *upspin.DirEntry // Receive from here to find next directory to process.
	done     chan *upspin.DirEntry // Send entries here once it is completely done, including children.
	seen     map[string]bool       // Remembers what directories we have seen, keyed by references within.
}

func (s *State) scanDirectories(args []string) {
	const help = `
Store scandir scans the directory tree for the named user roots.
For now it just prints the total storage consumed.`

	fs := flag.NewFlagSet("scandir", flag.ExitOnError)
	glob := flag.Bool("glob", true, "apply glob processing to the arguments")
	s.ParseFlags(fs, args, help, "store scandir root ...")

	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(2)
	}

	var paths []upspin.PathName
	if *glob {
		paths = s.GlobAllUpspinPath(fs.Args())
	} else {
		for _, p := range fs.Args() {
			paths = append(paths, upspin.PathName(p))
		}
	}

	// Check that the arguments are user roots.
	for _, p := range paths {
		parsed, err := path.Parse(p)
		if err != nil {
			s.Exit(err)
		}
		if !parsed.IsRoot() {
			s.Exitf("%q is not a user root", p)
		}
	}

	scanner := DirScanner{
		State:    s,
		buffer:   make(chan *upspin.DirEntry),
		dirsToDo: make(chan *upspin.DirEntry),
		done:     make(chan *upspin.DirEntry),
		seen:     make(map[string]bool),
	}

	for i := 0; i < scanParallelism; i++ {
		go scanner.dirWorker()
	}
	go scanner.bufferLoop()

	// Prime the pump.
	for _, p := range paths {
		dir := scanner.State.DirServer(p)
		de, err := dir.Lookup(p)
		if err != nil {
			scanner.State.Fail(err)
			continue
		}
		scanner.do(de)
	}

	// Shut down the process tree once nothing is in flight.
	go func() {
		scanner.inFlight.Wait()
		close(scanner.buffer)
		close(scanner.done)
	}()

	// Receive the data.
	size := make(map[upspin.Location]int64)
	for de := range scanner.done {
		for _, block := range de.Blocks {
			loc := block.Location
			size[loc] = block.Size
		}
	}

	sum := int64(0)
	for _, s := range size {
		sum += s
	}
	fmt.Println(sum, "bytes total (including directory entries)")
}

// do processes a DirEntry. If it's a file, we deliver it to the done channel.
// Otherwise it's a directory and we buffer it for expansion.
func (sc *DirScanner) do(entry *upspin.DirEntry) {
	if !entry.IsDir() {
		sc.done <- entry
	} else {
		sc.inFlight.Add(1)
		sc.buffer <- entry
	}
}

// bufferLoop gathers work to do and distributes it to the workers. It acts as
// an itermediary buffering work to avoid deadlock; without this loop, workers
// would both send to and receive from the dirsToDo channel. Once nothing is
// pending or in flight, bufferLoop shuts down the processing network.
func (sc *DirScanner) bufferLoop() {
	defer close(sc.dirsToDo)
	entriesPending := make(map[*upspin.DirEntry]bool)
	buffer := sc.buffer
	var buf bytes.Buffer
	for {
		var entry *upspin.DirEntry
		var dirsToDo chan *upspin.DirEntry
		if len(entriesPending) > 0 {
			// Pick one entry at random from the map.
			for entry = range entriesPending {
				break
			}
			dirsToDo = sc.dirsToDo
		} else if buffer == nil {
			return
		}
		select {
		case dirsToDo <- entry:
			delete(entriesPending, entry)
		case entry, active := <-buffer:
			if !active {
				buffer = nil
				break
			}
			// If this directory has already been done, don't do it again.
			// This situation arises when scanning a snapshot tree, as most of
			// the directories are just dups of those in the main tree.
			// We identify duplication by comparing the list of references within.
			// TODO: Find a less expensive check.
			buf.Reset()
			for i := range entry.Blocks {
				b := &entry.Blocks[i]
				fmt.Fprint(&buf, "%q %q\n", b.Location.Endpoint, b.Location.Reference)
			}
			key := buf.String()
			if sc.seen[key] {
				sc.inFlight.Done()
			} else {
				sc.seen[key] = true
				entriesPending[entry] = true
			}
		}
	}
}

// dirWorker receives DirEntries for directories from the dirsToDo channel
// and processes them, descending into their components and delivering
// the results to the buffer channel.
func (sc *DirScanner) dirWorker() {
	for dir := range sc.dirsToDo {
		des, err := sc.State.DirServer(dir.Name).Glob(upspin.AllFilesGlob(dir.Name))
		if err != nil {
			sc.State.Fail(err)
		} else {
			for _, de := range des {
				sc.do(de)
			}
		}
		sc.done <- dir
		sc.inFlight.Done()
	}
}