// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
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
