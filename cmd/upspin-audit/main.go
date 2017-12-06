// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Upspin-audit provides subcommands for auditing storage consumption.
// It has several subcommands that should be used in a way yet to be
// determined.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"upspin.io/config"
	"upspin.io/flags"
	"upspin.io/subcmd"
	"upspin.io/transports"
	"upspin.io/version"
)

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
	default:
		usage()
	}

	s.ExitNow()
}

func usage() {
	fmt.Fprintln(os.Stderr, help)
	fmt.Fprintln(os.Stderr, "Usage of upspin audit:")
	fmt.Fprintln(os.Stderr, "\tupspin [globalflags] <command> [flags] ...")
	fmt.Fprintln(os.Stderr, "\twhere <command> is one of scandir, scanstore")
	flag.PrintDefaults()
	os.Exit(2)
}

// ByteSize provides a way to make numbers format in nice compact form.
// Convert a number to ByteSize and print it using its String method to see
// 2392685154 pint as 2.23GB.
type ByteSize float64

const (
	_           = iota // ignore first value by assigning to blank identifier
	KB ByteSize = 1 << (10 * iota)
	MB
	GB
	TB
	PB
	EB
	ZB
	YB
)

func (b ByteSize) String() string {
	switch {
	case b >= YB:
		return fmt.Sprintf("%.2fYB", b/YB)
	case b >= ZB:
		return fmt.Sprintf("%.2fZB", b/ZB)
	case b >= EB:
		return fmt.Sprintf("%.2fEB", b/EB)
	case b >= PB:
		return fmt.Sprintf("%.2fPB", b/PB)
	case b >= TB:
		return fmt.Sprintf("%.2fTB", b/TB)
	case b >= GB:
		return fmt.Sprintf("%.2fGB", b/GB)
	case b >= MB:
		return fmt.Sprintf("%.2fMB", b/MB)
	case b >= KB:
		return fmt.Sprintf("%.2fKB", b/KB)
	}
	return fmt.Sprintf("%.2fB", b)
}
