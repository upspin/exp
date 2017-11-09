// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"testing"

	"upspin.io/pack"
	"upspin.io/test/testenv"
	"upspin.io/upspin"
)

func TestIntegration(t *testing.T) {
	const (
		name  = "test@example.com"
		other = "aly@example.net"
	)
	env, err := testenv.New(&testenv.Setup{
		OwnerName: name,
		Kind:      "server",
		Packing:   upspin.EEPack,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer env.Exit()

	_, err = env.NewUser(other)
	if err != nil {
		t.Fatal(err)
	}

	r := testenv.NewRunner()
	r.AddUser(env.Config)

	const (
		dir        = name + "/dir"
		file       = dir + "/file"
		accessFile = dir + "/Access"
	)

	r.As(name)
	r.MakeDirectory(name + "/dir")
	r.Put(file, "some content")
	if r.Failed() {
		t.Fatal(r.Diag())
	}

	w, err := NewWatcher(env.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Shutdown()

	done := r.DirWatch(name, -1)
	defer close(done)

	// No Access file.
	r.GetNEvents(3)
	r.GotEvent(file, true)
	if r.Failed() {
		t.Fatal(err)
	}
	if got := numHashes(r, file); got != 1 {
		t.Fatalf("got %d hashes for %q, want 1", got, file)
	}

	// Access file with two readers.
	r.Put(accessFile, "*:"+name+"\nr:"+other)
	r.GetNEvents(1)
	r.GotEvent(accessFile, true)
	r.GetNEvents(1)
	r.GotEvent(file, true)
	if r.Failed() {
		t.Fatal(err)
	}
	if got := numHashes(r, file); got != 2 {
		t.Fatalf("got %d hashes for %q, want 2", got, file)
	}

	// No Access file.
	r.Delete(accessFile)
	r.GetDeleteEvent(accessFile)
	r.GetNEvents(1)
	r.GotEvent(file, true)
	if r.Failed() {
		t.Fatal(r.Diag())
	}
	if got := numHashes(r, file); got != 1 {
		t.Fatalf("got %d hashes for %q, want 1", got, file)
	}

	// Access file with just owner.
	r.Put(accessFile, "*:"+name)
	r.GetNEvents(1)
	r.GotEvent(accessFile, true)
	if r.Failed() {
		t.Fatal(err)
	}
	// No change to file.

	// Access file with two readers again.
	r.Put(accessFile, "*:"+name+"\nr:"+other)
	r.GetNEvents(1)
	r.GotEvent(accessFile, true)
	r.GetNEvents(1)
	r.GotEvent(file, true)
	if r.Failed() {
		t.Fatal(err)
	}
	if got := numHashes(r, file); got != 2 {
		t.Fatalf("got %d hashes for %q, want 2", got, file)
	}
}

func numHashes(r *testenv.Runner, name upspin.PathName) int {
	for _, e := range r.Events {
		if e.Entry.Name == name {
			hs, _ := pack.Lookup(upspin.EEPack).ReaderHashes(e.Entry.Packdata)
			return len(hs)
		}
	}
	return -1
}
