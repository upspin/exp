// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"upspin.io/errors"
	"upspin.io/path"
	"upspin.io/upspin"
)

func (s *server) copyPaths(dst upspin.PathName, srcs []upspin.PathName) error {
	// Check that the destination exists and is a directory.
	dstEntry, err := s.cli.Lookup(dst, true)
	if err != nil {
		return err
	}
	if !dstEntry.IsDir() {
		return errors.E(dst, errors.NotDir)
	}

	// Iterate through sources and copy them recursively.
	for _, src := range srcs {
		// Lookup src, but don't follow links.
		srcEntry, err := s.cli.Lookup(src, false)
		if err != nil {
			return err
		}
		if err := s.copyPath(dst, srcEntry); err != nil {
			return err
		}
	}
	return nil
}

// Assume that dstDir exists and is a directory.
func (s *server) copyPath(dstDir upspin.PathName, srcEntry *upspin.DirEntry) error {
	srcPath, err := path.Parse(srcEntry.Name)
	if err != nil {
		return err
	}
	if srcPath.NElem() == 0 {
		return errors.E(srcEntry.Name, errors.Str("cannot copy root"))
	}
	dst := path.Join(dstDir, srcPath.Elem(srcPath.NElem()-1))

	switch {
	case srcEntry.IsDir():
		// Recurse into directories.
		if _, err := s.cli.MakeDirectory(dst); err != nil {
			return err
		}
		dir, err := s.cli.DirServer(srcEntry.Name)
		if err != nil {
			return err
		}
		des, err := dir.Glob(string(upspin.QuoteGlob(srcEntry.Name) + "/*"))
		if err != nil && err != upspin.ErrFollowLink {
			return err
		}
		for _, de := range des {
			if err := s.copyPath(dst, de); err != nil {
				return err
			}
		}
		return nil
	case srcEntry.IsLink():
		if _, err := s.cli.PutLink(srcEntry.Link, dst); err != nil {
			return err
		}
	default:
		if _, err := s.cli.PutDuplicate(srcEntry.Name, dst); err != nil {
			return err
		}
	}
	return nil
}
