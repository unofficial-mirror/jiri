// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package jiritest provides utilities for testing jiri functionality.
package jiritest

import (
	"os"
	"path/filepath"
	"testing"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/log"
	"fuchsia.googlesource.com/jiri/tool"
)

// NewX is similar to jiri.NewX, but is meant for usage in a testing environment.
func NewX(t *testing.T) (*jiri.X, func()) {
	ctx := tool.NewDefaultContext()
	logger := log.NewLogger(log.InfoLevel)
	root, err := ctx.NewSeq().TempDir("", "")
	if err != nil {
		t.Fatalf("TempDir() failed: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, jiri.RootMetaDir), 0755); err != nil {
		t.Fatalf("TempDir() failed: %v", err)
	}
	cleanup := func() {
		if err := ctx.NewSeq().RemoveAll(root).Done(); err != nil {
			t.Fatalf("RemoveAll(%q) failed: %v", root, err)
		}
	}
	return &jiri.X{Context: ctx, Root: root, Jobs: jiri.DefaultJobs, Logger: logger}, cleanup
}
