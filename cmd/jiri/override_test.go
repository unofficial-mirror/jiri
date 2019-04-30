// Copyright 2018 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"testing"

	"fuchsia.googlesource.com/jiri/jiritest"
	"fuchsia.googlesource.com/jiri/project"
)

type overrideTestCase struct {
	Args           []string
	Filename       string
	OutputFileName string
	Exist, Want    string
	Stdout, Stderr string
	SetFlags       func()
	runOnce        bool
}

func setDefaultOverrideFlags() {
	overrideFlags.importManifest = ""
	overrideFlags.path = ""
	overrideFlags.revision = ""
	overrideFlags.gerritHost = ""
	overrideFlags.delete = false
	overrideFlags.list = false
	overrideFlags.JSONOutput = ""
}

func TestOverride(t *testing.T) {
	tests := []overrideTestCase{
		{
			Stderr: `wrong number of arguments`,
		},
		{
			Args:   []string{"a"},
			Stderr: `wrong number of arguments`,
		},
		{
			Args:   []string{"a", "b", "c"},
			Stderr: `wrong number of arguments`,
		},
		// Remote imports, default append behavior
		{
			Args: []string{"foo", "https://github.com/new.git"},
			Want: `<manifest>
  <imports>
    <import manifest="manifest" name="foo" remote="https://github.com/new.git"/>
  </imports>
  <overrides>
    <project name="foo" remote="https://github.com/new.git"/>
  </overrides>
</manifest>
`,
		},
		{
			SetFlags: func() {
				overrideFlags.path = "bar"
			},
			Args: []string{"foo", "https://github.com/new.git"},
			Want: `<manifest>
  <imports>
    <import manifest="manifest" name="foo" remote="https://github.com/new.git"/>
  </imports>
  <overrides>
    <project name="foo" path="bar" remote="https://github.com/new.git"/>
  </overrides>
</manifest>
`,
		},
		{
			SetFlags: func() {
				overrideFlags.revision = "bar"
			},
			Args: []string{"foo", "https://github.com/new.git"},
			Want: `<manifest>
  <imports>
    <import manifest="manifest" name="foo" remote="https://github.com/new.git"/>
  </imports>
  <overrides>
    <project name="foo" remote="https://github.com/new.git" revision="bar"/>
  </overrides>
</manifest>
`,
		},
		{
			SetFlags: func() {
				overrideFlags.list = true
				overrideFlags.JSONOutput = "file"
			},
			Exist: `<manifest>
  <imports>
    <import manifest="manifest" name="orig" remote="https://github.com/orig.git"/>
  </imports>
  <overrides>
    <project name="foo" remote="https://github.com/new.git"/>
  </overrides>
</manifest>
`,
			OutputFileName: `file`,
			Want: `[
  {
    "name": "foo",
    "remote": "https://github.com/new.git",
    "revision": "HEAD"
  }
]
`,
		},
		{
			SetFlags: func() {
				overrideFlags.list = true
			},
			Exist: `<manifest>
  <imports>
    <import manifest="manifest" name="orig" remote="https://github.com/orig.git"/>
  </imports>
  <overrides>
    <project name="foo" remote="https://github.com/new.git"/>
  </overrides>
</manifest>
`,
			Stdout: `* override foo
  Name:        foo
  Remote:      https://github.com/new.git
`,
		},
		{
			Args: []string{"bar", "https://github.com/bar.git"},
			Exist: `<manifest>
  <imports>
    <import manifest="manifest" name="orig" remote="https://github.com/orig.git"/>
  </imports>
  <overrides>
    <project name="foo" remote="https://github.com/foo.git"/>
  </overrides>
</manifest>
`,
			Want: `<manifest>
  <imports>
    <import manifest="manifest" name="orig" remote="https://github.com/orig.git"/>
  </imports>
  <overrides>
    <project name="foo" remote="https://github.com/foo.git"/>
    <project name="bar" remote="https://github.com/bar.git"/>
  </overrides>
</manifest>
`,
		},
		// test delete flag
		{
			SetFlags: func() {
				overrideFlags.delete = true
			},
			Stderr:  `wrong number of arguments`,
			runOnce: true,
		},
		{
			SetFlags: func() {
				overrideFlags.delete = true
			},
			Args:    []string{"a", "b", "c"},
			Stderr:  `wrong number of arguments`,
			runOnce: true,
		},
		{
			SetFlags: func() {
				overrideFlags.delete = true
				overrideFlags.list = true
			},
			Args:    []string{"a", "b"},
			Stderr:  `cannot use -delete and -list together`,
			runOnce: true,
		},
		{
			SetFlags: func() {
				overrideFlags.delete = true
			},
			Args:    []string{"foo"},
			runOnce: true,
			Exist: `<manifest>
  <imports>
    <import manifest="manifest" name="orig" remote="https://github.com/orig.git"/>
  </imports>
  <overrides>
    <project name="foo" remote="https://github.com/foo.git"/>
    <project name="bar" remote="https://github.com/bar.git"/>
  </overrides>
</manifest>
`,
			Want: `<manifest>
  <imports>
    <import manifest="manifest" name="orig" remote="https://github.com/orig.git"/>
  </imports>
  <overrides>
    <project name="bar" remote="https://github.com/bar.git"/>
  </overrides>
</manifest>
`,
		},
		{
			SetFlags: func() {
				overrideFlags.delete = true
			},
			Args:    []string{"foo"},
			runOnce: true,
			Exist: `<manifest>
  <imports>
    <import manifest="manifest" name="orig" remote="https://github.com/orig.git"/>
  </imports>
  <overrides>
    <project name="foo" remote="https://github.com/foo.git"/>
    <project name="foo" remote="https://github.com/bar.git"/>
  </overrides>
</manifest>
`,
			Stderr: `more than one override matches`,
		},
		{
			SetFlags: func() {
				overrideFlags.delete = true
			},
			Args:    []string{"foo", "https://github.com/bar.git"},
			runOnce: true,
			Exist: `<manifest>
  <imports>
    <import manifest="manifest" name="orig" remote="https://github.com/orig.git"/>
  </imports>
  <overrides>
    <project name="foo" remote="https://github.com/foo.git"/>
    <project name="foo" remote="https://github.com/bar.git"/>
  </overrides>
</manifest>
`,
			Want: `<manifest>
  <imports>
    <import manifest="manifest" name="orig" remote="https://github.com/orig.git"/>
  </imports>
  <overrides>
    <project name="foo" remote="https://github.com/foo.git"/>
  </overrides>
</manifest>
`,
		},
		{
			SetFlags: func() {
				overrideFlags.importManifest = "manifest"
				overrideFlags.revision = "eabeadae97b1e7f97ba93206066411adfe93a509"
			},
			Args:    []string{"orig", "https://github.com/orig.git"},
			runOnce: true,
			Exist: `<manifest>
  <imports>
    <import manifest="manifest" name="orig" remote="https://github.com/orig.git"/>
  </imports>
</manifest>
`,
			Want: `<manifest>
  <imports>
    <import manifest="manifest" name="orig" remote="https://github.com/orig.git"/>
  </imports>
  <overrides>
    <import manifest="manifest" name="orig" remote="https://github.com/orig.git" revision="eabeadae97b1e7f97ba93206066411adfe93a509"/>
  </overrides>
</manifest>
`,
		},
	}

	// Temporary directory in which our jiri binary will live.
	binDir, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(binDir)

	for _, test := range tests {
		if err := testOverride(t, test); err != nil {
			t.Errorf("%v: %v", test.Args, err)
		}
	}
}

func testOverride(t *testing.T, test overrideTestCase) error {
	jirix, cleanup := jiritest.NewX(t)
	defer cleanup()
	// Temporary directory in which to run `jiri import`.
	tmpDir, err := ioutil.TempDir("", "")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	// Create a .jiri_manifest file which imports the manifest created above.
	manifest := project.Manifest{
		Imports: []project.Import{
			project.Import{
				Manifest: "manifest",
				Name:     "foo",
				Remote:   "https://github.com/new.git",
			},
		},
	}
	if err := manifest.ToFile(jirix, jirix.JiriManifestFile()); err != nil {
		t.Fatal(err)
	}

	// Return to the current working directory when done.
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	defer os.Chdir(cwd)

	// cd into a root directory in which to do the actual import.
	jiriRoot := jirix.Root
	if err := os.Chdir(jiriRoot); err != nil {
		return err
	}

	// Allow optional non-default filenames.
	filename := test.Filename
	if filename == "" {
		filename = ".jiri_manifest"
	}

	// Set up an existing file if it was specified.
	if test.Exist != "" {
		if err := ioutil.WriteFile(filename, []byte(test.Exist), 0644); err != nil {
			return err
		}
	}

	run := func() error {
		// Run override and check the results.
		overrideCmd := func() {
			setDefaultOverrideFlags()
			if test.SetFlags != nil {
				test.SetFlags()
			}
			err = runOverride(jirix, test.Args)
		}
		stdout, _, runErr := runfunc(overrideCmd)
		if runErr != nil {
			return err
		}
		stderr := ""
		if err != nil {
			stderr = err.Error()
		}
		if got, want := stdout, test.Stdout; !strings.Contains(got, want) || (got != "" && want == "") {
			return fmt.Errorf("stdout got %q, want substr %q", got, want)
		}
		if got, want := stderr, test.Stderr; !strings.Contains(got, want) || (got != "" && want == "") {
			return fmt.Errorf("stderr got %q, want substr %q", got, want)
		}
		return nil
	}
	if err := run(); err != nil {
		return err
	}

	// check that it is idempotent
	if !test.runOnce {
		if err := run(); err != nil {
			return err
		}
	}
	f := test.OutputFileName
	if f == "" {
		f = filename
	}

	// Make sure the right file is generated.
	if test.Want != "" {
		data, err := ioutil.ReadFile(f)
		if err != nil {
			return err
		}
		if got, want := string(data), test.Want; got != want {
			return fmt.Errorf("GOT\n%s\nWANT\n%s", got, want)
		}
	}
	return nil
}
