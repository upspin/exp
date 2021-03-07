// Copyright 2021 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build carchive

package main

import "C"
import (
	"flag"
	"strings"

	"upspin.io/flags"
)

// warden is a global Warden instance that can be used when this is linked in as a library.
var warden *Warden

//export wardenInit
func wardenInit() *C.char {
	cmd := flag.String("cmd", "cacheserver,upspinfs,upspin-sharebot", "comma-separated list of `commands` to run")
	flags.Parse(nil, "log", "config")
	warden = NewWarden(strings.Split(*cmd, ","))
	return C.CString(*cmd)
}

//export wardenProcState
func wardenProcState(proc *C.char) *C.char {
	p := C.GoString(proc)
	return C.CString(warden.procs[p].State().String())
}

//export wardenProcLog
func wardenProcLog(proc *C.char) *C.char {
	p := C.GoString(proc)
	return C.CString(string(warden.procs[p].log.Log()))
}
