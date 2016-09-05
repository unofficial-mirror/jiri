// Copyright 2016 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build windows

package osutil

import (
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	kernel32               = syscall.MustLoadDLL("kernel32.dll")
	procGetModuleFileNameW = kernel32.MustFindProc("GetModuleFileNameW")
)

// Executable returns an absolute path to the currently executing program.
func getModuleFileName(handle syscall.Handle) (string, error) {
	n := uint32(1024)
	var buf []uint16
	for {
		buf = make([]uint16, n)
		r0, _, e1 := syscall.Syscall(procGetModuleFileNameW.Addr(), 3, uintptr(0), uintptr(unsafe.Pointer(&buf[0])), uintptr(n))
		r := uint32(r0)
		if r == 0 {
			if e1 != 0 {
				return "", error(e1)
			} else {
				return "", syscall.EINVAL
			}
		}
		if r < n {
			break
		}
		n += 1024
	}
	return syscall.UTF16ToString(buf), nil
}

func Executable() (string, error) {
	p, err := getModuleFileName(0)
	return filepath.Clean(p), err
}
