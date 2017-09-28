// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the https://golang.org/LICENSE file.

// +build ignore

// This file is adapted from golang.org/x/tools/godoc/static/makestatic.go.

// Command makestatic reads a set of files and writes a Go source file to
// "static.go" that declares a map of string constants containing contents of
// the input files. It is intended to be invoked via "go generate".
package main

import (
	"bytes"
	"fmt"
	"go/format"
	"io/ioutil"
	"os"
	"time"
	"unicode/utf8"
)

var files = []string{
	"augie.png",
	"favicon-32x32.png",
	"index.html",
	"script.js",
	"third_party/bootstrap/css/bootstrap-theme.min.css",
	"third_party/bootstrap/css/bootstrap-theme.min.css.map",
	"third_party/bootstrap/css/bootstrap.min.css",
	"third_party/bootstrap/css/bootstrap.min.css.map",
	"third_party/bootstrap/fonts/glyphicons-halflings-regular.eot",
	"third_party/bootstrap/fonts/glyphicons-halflings-regular.svg",
	"third_party/bootstrap/fonts/glyphicons-halflings-regular.ttf",
	"third_party/bootstrap/fonts/glyphicons-halflings-regular.woff",
	"third_party/bootstrap/fonts/glyphicons-halflings-regular.woff2",
	"third_party/bootstrap/js/bootstrap.min.js",
	"third_party/jquery/jquery.min.js",
	"third_party/ladda/ladda-themeless.min.css",
	"third_party/ladda/ladda.min.js",
	"third_party/ladda/spin.min.js",
}

func main() {
	if err := makestatic(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func makestatic() error {
	f, err := os.Create("static.go")
	if err != nil {
		return err
	}
	defer f.Close()
	buf := new(bytes.Buffer)
	fmt.Fprintf(buf, "%v\n\n%v\n\npackage static\n\n", license, warning)
	fmt.Fprintf(buf, "func init() { files = map[string]string{\n")
	for _, fn := range files {
		b, err := ioutil.ReadFile(fn)
		if err != nil {
			return err
		}
		fmt.Fprintf(buf, "\t%q: ", fn)
		if utf8.Valid(b) {
			fmt.Fprintf(buf, "`%s`", sanitize(b))
		} else {
			fmt.Fprintf(buf, "%q", b)
		}
		fmt.Fprintln(buf, ",\n")
	}
	fmt.Fprintln(buf, "}}")
	fmtbuf, err := format.Source(buf.Bytes())
	if err != nil {
		return err
	}
	return ioutil.WriteFile("static.go", fmtbuf, 0666)
}

// sanitize prepares a valid UTF-8 string as a raw string constant.
func sanitize(b []byte) []byte {
	// Replace ` with `+"`"+`
	b = bytes.Replace(b, []byte("`"), []byte("`+\"`\"+`"), -1)

	// Replace BOM with `+"\xEF\xBB\xBF"+`
	// (A BOM is valid UTF-8 but not permitted in Go source files.
	// I wouldn't bother handling this, but for some insane reason
	// jquery.js has a BOM somewhere in the middle.)
	return bytes.Replace(b, []byte("\xEF\xBB\xBF"), []byte("`+\"\\xEF\\xBB\\xBF\"+`"), -1)
}

const warning = `// Code generated by "makestatic"; DO NOT EDIT.`

var license = fmt.Sprintf(`// Copyright %d The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.`, time.Now().UTC().Year())