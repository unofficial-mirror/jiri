// Copyright 2016 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build freebsd darwin

package osutil

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

// Executable returns an absolute path to the currently executing program.
func Executable() (string, error) {
	var mib [4]int32
	switch runtime.GOOS {
	case "freebsd":
		mib = [4]int32{1 /* CTL_KERN */, 14 /* KERN_PROC */, 12 /* KERN_PROC_PATHNAME */, -1}
	case "darwin":
		mib = [4]int32{1 /* CTL_KERN */, 38 /* KERN_PROCARGS */, int32(os.Getpid()), -1}
	}

	n := uintptr(0)
	_, _, err := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), 4, 0, uintptr(unsafe.Pointer(&n)), 0, 0)
	if err != 0 {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	buf := make([]byte, n)
	_, _, err = syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), 4, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0)
	if err != 0 {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	for i, v := range buf {
		if v == 0 {
			buf = buf[:i]
			break
		}
	}
	p := string(buf)
	if !filepath.IsAbs(p) {
		wd, err := os.Getwd()
		if err != nil {
			return p, err
		}
		p = filepath.Join(wd, filepath.Clean(p))
	}
	return filepath.EvalSymlinks(p)
}
