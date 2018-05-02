// Copyright 2018 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"io/ioutil"
	"strings"
	"testing"

	"fuchsia.googlesource.com/jiri/jiritest"
)

func TestManifest(t *testing.T) {
	// Create a test manifest file.
	testManifestFile, err := ioutil.TempFile("", "test_manifest")
	if err != nil {
		t.Fatalf("failed to create test manifest: %s", err)
	}
	testManifestFile.Write([]byte(`
<?xml version="1.0" encoding="UTF-8"?>
<manifest>
	<imports>
	<import name="the_import"
			manifest="the_import_manifest"
			remote="https://fuchsia.googlesource.com/the_import"
			revision="the_import_revision"
			remotebranch="the_import_remotebranch"
			root="the_import_root"/>
	</imports>
	<projects>
	<project name="the_project"
				path="path/to/the_project"
				remote="https://fuchsia.googlesource.com/the_project"
				remotebranch="the_project_remotebranch"
				revision="the_project_revision"
				githooks="the_project_githooks"
				gerrithost="https://fuchsia-review.googlesource.com"
				historydepth="2"/>
	</projects>
</manifest>
`))

	runCommand := func(t *testing.T, args []string) (stdout string, stderr string) {
		// Set up a fake Jiri root to pass to our command.
		fake, cleanup := jiritest.NewFakeJiriRoot(t)
		defer cleanup()

		// Initialize flags for the command.
		flagSet := flag.NewFlagSet("manifest-test", flag.ContinueOnError)
		setManifestFlags(flagSet)

		// Make sure flags parse correctly.
		if err := flagSet.Parse(args); err != nil {
			t.Error(err)
		}

		// Run the command.
		runCmd := func() {
			if err := runManifest(fake.X, flagSet.Args()); err != nil {
				// Capture the error as stderr since Jiri subcommands don't
				// intenionally print to stderr when they fail.
				stderr = err.Error()
			}
		}

		var err error
		stdout, _, err = runfunc(runCmd)
		if err != nil {
			t.Fatal(err)
		}

		return stdout, stderr
	}

	// Expects manifest to return a specific value when given args.
	expectAttributeValue := func(t *testing.T, args []string, expectedValue string) {
		stdout, stderr := runCommand(t, args)

		// If an error occurred, fail.
		if stderr != "" {
			t.Error("error:", stderr)
			return
		}

		// Compare stdout to the expected value.
		if strings.Trim(stdout, " \n") != expectedValue {
			t.Errorf("expected %s, got %s", expectedValue, stdout)
		}
	}

	// Expects manifest to error when given args.
	expectError := func(t *testing.T, args []string) {
		stdout, stderr := runCommand(t, args)

		// Fail if no error was output.
		if stderr == "" {
			t.Errorf("expected an error, got %s", stdout)
			return
		}
	}

	t.Run("should fail if manifest file is missing", func(t *testing.T) {
		expectError(t, []string{
			"-element=the_import",
			"-template={{.Name}}",
		})

		expectError(t, []string{
			"-element=the_project",
			"-template={{.Name}}",
		})
	})

	t.Run("should fail if -attribute is missing", func(t *testing.T) {
		expectError(t, []string{
			"-element=the_import",
			testManifestFile.Name(),
		})

		expectError(t, []string{
			"-element=the_project",
			testManifestFile.Name(),
		})
	})

	t.Run("should fail if -element is missing", func(t *testing.T) {
		expectError(t, []string{
			"-template={{.Name}}",
			testManifestFile.Name(),
		})

		expectError(t, []string{
			"-template={{.Name}}",
			testManifestFile.Name(),
		})
	})

	t.Run("should read <project> attributes", func(t *testing.T) {
		expectAttributeValue(t, []string{
			"-element=the_project",
			"-template={{.Name}}",
			testManifestFile.Name(),
		},
			"the_project")

		expectAttributeValue(t, []string{
			"-element=the_project",
			"-template={{.Remote}}",
			testManifestFile.Name(),
		},
			"https://fuchsia.googlesource.com/the_project")

		expectAttributeValue(t, []string{
			"-element=the_project",
			"-template={{.Revision}}",
			testManifestFile.Name(),
		},
			"the_project_revision")

		expectAttributeValue(t, []string{
			"-element=the_project",
			"-template={{.RemoteBranch}}",
			testManifestFile.Name(),
		},
			"the_project_remotebranch")

		expectAttributeValue(t, []string{
			"-element=the_project",
			"-template={{.Path}}",
			testManifestFile.Name(),
		},
			"path/to/the_project")
	})

	t.Run("should read <import> attributes", func(t *testing.T) {
		expectAttributeValue(t, []string{
			"-element=the_import",
			"-template={{.Name}}",
			testManifestFile.Name(),
		},
			"the_import")

		expectAttributeValue(t, []string{
			"-element=the_import",
			"-template={{.Remote}}",
			testManifestFile.Name(),
		},
			"https://fuchsia.googlesource.com/the_import")

		expectAttributeValue(t, []string{
			"-element=the_import",
			"-template={{.Manifest}}",
			testManifestFile.Name(),
		},
			"the_import_manifest")

		expectAttributeValue(t, []string{
			"-element=the_import",
			"-template={{.Revision}}",
			testManifestFile.Name(),
		},
			"the_import_revision")

		expectAttributeValue(t, []string{
			"-element=the_import",
			"-template={{.RemoteBranch}}",
			testManifestFile.Name(),
		},
			"the_import_remotebranch")
	})
}
