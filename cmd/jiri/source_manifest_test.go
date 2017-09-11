// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"fuchsia.googlesource.com/jiri/git"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/jiritest"
	"fuchsia.googlesource.com/jiri/project"
	"fuchsia.googlesource.com/jiri/tool"
)

// TestSourceManifest tests creation of source manifest.
func TestSourceManifest(t *testing.T) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()

	// Setup the initial remote and local projects.
	numProjects := 4
	for i := 0; i < numProjects; i++ {
		if err := fake.CreateRemoteProject(remoteProjectName(i)); err != nil {
			t.Fatalf("%s", err)
		}
		rb := ""
		if i == 2 {
			rb = "test-branch"
			g := gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[remoteProjectName(i)]))
			if err := g.CreateAndCheckoutBranch(rb); err != nil {
				t.Fatal(err)
			}
		}
		if err := fake.AddProject(project.Project{
			Name:         remoteProjectName(i),
			Path:         localProjectName(i),
			Remote:       fake.Projects[remoteProjectName(i)],
			RemoteBranch: rb,
		}); err != nil {
			t.Fatalf("%s", err)
		}
	}

	// Create initial commits in the remote projects and use UpdateUniverse()
	// to mirror them locally.
	for i := 0; i < numProjects; i++ {
		writeReadme(t, fake.X, fake.Projects[remoteProjectName(i)], fmt.Sprintf("proj %d", i))
	}
	if err := project.UpdateUniverse(fake.X, true, false, false, false, false, true /*run-hooks*/, project.DefaultHookTimeout); err != nil {
		t.Fatalf("%s", err)
	}

	// test when current revision is not in any branch
	writeReadme(t, fake.X, filepath.Join(fake.X.Root, localProjectName(3)), "file")

	// Get local revision
	paths := []string{"manifest"}
	for i := 0; i < numProjects; i++ {
		paths = append(paths, localProjectName(i))
	}
	revMap := make(map[string][]byte)
	for _, path := range paths {
		g := git.NewGit(filepath.Join(fake.X.Root, path))
		if rev, err := g.CurrentRevisionRaw(); err != nil {
			t.Fatal(err)
		} else {
			revMap[path] = rev
		}

	}

	var stdout bytes.Buffer
	fake.X.Context = tool.NewContext(tool.ContextOpts{Stdout: &stdout, Env: fake.X.Context.Env()})

	smTmpfile, err := ioutil.TempFile("", "jiri-sm-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(smTmpfile.Name())

	if err := runSourceManifest(fake.X, []string{smTmpfile.Name()}); err != nil {
		t.Fatalf("%s", err)
	}

	sm := &project.SourceManifest{
		Version: project.SourceManifestVersion,
	}
	sm.Directories = make(map[string]*project.SourceManifest_Directory)
	sm.Directories["manifest"] = &project.SourceManifest_Directory{
		GitCheckout: &project.SourceManifest_GitCheckout{
			RepoUrl:  fake.Projects["manifest"],
			Revision: revMap["manifest"],
			FetchRef: "refs/heads/master",
		},
	}
	for i := 0; i < numProjects; i++ {
		ref := "refs/heads/master"
		if i == 2 {
			ref = "refs/heads/test-branch"
		} else if i == 3 {
			ref = ""
		}
		sm.Directories[localProjectName(i)] = &project.SourceManifest_Directory{
			GitCheckout: &project.SourceManifest_GitCheckout{
				RepoUrl:  fake.Projects[remoteProjectName(i)],
				Revision: revMap[localProjectName(i)],
				FetchRef: ref,
			},
		}
	}

	want, err := json.MarshalIndent(sm, "", "  ")
	if err != nil {
		t.Fatalf("failed to serialize JSON output: %s\n", err)
	}

	got, _ := ioutil.ReadFile(smTmpfile.Name())
	if string(got) != string(want) {
		t.Fatalf("GOT:\n%s, \nWANT:\n%s", (string(got)), string(want))
	}
}
