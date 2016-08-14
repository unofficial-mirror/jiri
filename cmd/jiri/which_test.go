// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWhich(t *testing.T) {
	buildDir, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(buildDir)
	jiriPath := buildGoPkg(t, "fuchsia.googlesource.com/jiri/cmd/jiri", buildDir)
	whichCmd := exec.Command(jiriPath, "which")
	stdout, stderr := runCmd(t, whichCmd, true)
	if got, want := stdout, fmt.Sprintf("# binary\n%s\n", jiriPath); got != want {
		t.Errorf("stdout got %q, want %q", got, want)
	}
	if got, want := stderr, ""; got != want {
		t.Errorf("stderr got %q, want %q", got, want)
	}
}

// TestWhichScript tests the behavior of "jiri which" for the shim script.
func TestWhichScript(t *testing.T) {
	// This relative path points at the checked-in copy of the jiri script.
	jiriScript, err := filepath.Abs("../../scripts/jiri")
	if err != nil {
		t.Fatalf("couldn't determine absolute path to jiri script")
	}
	whichCmd := exec.Command(jiriScript, "which")
	stdout, stderr := runCmd(t, whichCmd, true)
	if got, want := stdout, fmt.Sprintf("# script\n%s\n", jiriScript); got != want {
		t.Errorf("stdout got %q, want %q", got, want)
	}
	if got, want := stderr, ""; got != want {
		t.Errorf("stderr got %q, want %q", got, want)
	}
}
