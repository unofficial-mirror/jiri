// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package xtest provides utilities for testing jiri functionality.
package xtest

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.fuchsia.dev/jiri"
	"go.fuchsia.dev/jiri/cmdline"
	"go.fuchsia.dev/jiri/color"
	"go.fuchsia.dev/jiri/log"
	"go.fuchsia.dev/jiri/tool"
)

// NewX is similar to jiri.NewX, but is meant for usage in a testing environment.
func NewX(t *testing.T) (*jiri.X, func()) {
	ctx := tool.NewContextFromEnv(cmdline.EnvFromOS())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	color := color.NewColor(color.ColorNever)
	logger := log.NewLogger(log.InfoLevel, color, false, 0, time.Second*100, nil, nil)
	root, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatalf("TempDir() failed: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Setting cwd failed: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, jiri.RootMetaDir), 0755); err != nil {
		t.Fatalf("TempDir() failed: %v", err)
	}
	cleanup := func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("Setting cwd failed: %v", err)
		}
		if err := os.RemoveAll(root); err != nil {
			t.Fatalf("RemoveAll(%q) failed: %v", root, err)
		}
	}
	return &jiri.X{Context: ctx, Root: root, Jobs: jiri.DefaultJobs, Color: color, Logger: logger, Attempts: 1, LockfileEnabled: false}, cleanup
}
