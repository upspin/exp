// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"io"
	"mime/multipart"

	"upspin.io/errors"
	"upspin.io/path"
	"upspin.io/upspin"
)

// put reads the given multipart file and writes it as an Upspin file in the
// given directory.
func (s *server) put(dir upspin.PathName, fh *multipart.FileHeader) error {
	src, err := fh.Open()
	if err != nil {
		return err
	}
	dst, err := s.cli.Create(path.Join(dir, fh.Filename))
	if err != nil {
		return err
	}
	n, err := io.Copy(dst, src)
	if err != nil {
		return err
	}
	if n != fh.Size {
		return errors.Errorf("put: copied %d bytes, source is %d bytes", n, fh.Size)
	}
	err = dst.Close()
	src.Close()
	return err
}
