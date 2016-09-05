// Copyright 2016 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build linux netbsd openbsd

package osutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Executable returns an absolute path to the currently executing program.
func Executable() (string, error) {
	var path string
	var err error
	switch runtime.GOOS {
	case "linux":
		const deletedTag = " (deleted)"
		path, err = os.Readlink("/proc/self/exe")
		if err != nil {
			return "", err
		}
		path = strings.TrimSuffix(path, deletedTag)
		path = strings.TrimPrefix(path, deletedTag)
	case "netbsd":
		path, err = os.Readlink("/proc/curproc/exe")
	case "openbsd":
		path, err = os.Readlink("/proc/curproc/file")
	}
	return filepath.Clean(path), err
}
