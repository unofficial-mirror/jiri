// Copyright 2015 The Vanadium Authors. All rights reserved.
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

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/git"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/jiritest"
	"fuchsia.googlesource.com/jiri/project"
	"fuchsia.googlesource.com/jiri/tool"
)

func checkReadme(t *testing.T, jirix *jiri.X, project, message string) {
	if _, err := os.Stat(project); err != nil {
		t.Fatalf("%v", err)
	}
	readmeFile := filepath.Join(project, "README")
	data, err := ioutil.ReadFile(readmeFile)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if got, want := data, []byte(message); bytes.Compare(got, want) != 0 {
		t.Fatalf("unexpected content %v:\ngot\n%s\nwant\n%s\n", project, got, want)
	}
}

func localProjectName(i int) string {
	return "test-local-project-" + fmt.Sprintf("%d", i+1)
}

func remoteProjectName(i int) string {
	return "test-remote-project-" + fmt.Sprintf("%d", i+1)
}

func setDefaultSnapshotFlag() {
	sourceManifestFilename = ""
}

func writeReadme(t *testing.T, jirix *jiri.X, projectDir, message string) {
	path, perm := filepath.Join(projectDir, "README"), os.FileMode(0644)
	if err := ioutil.WriteFile(path, []byte(message), perm); err != nil {
		t.Fatalf("%v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer os.Chdir(cwd)
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("%v", err)
	}
	if err := gitutil.New(jirix, gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com")).CommitFile(path, "creating README"); err != nil {
		t.Fatalf("%v", err)
	}
}

// TestSnapshot tests creating and checking out a snapshot.
func TestSnapshot(t *testing.T) {
	setDefaultSnapshotFlag()
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()

	// Setup the initial remote and local projects.
	numProjects, remoteProjects := 2, []string{}
	for i := 0; i < numProjects; i++ {
		if err := fake.CreateRemoteProject(remoteProjectName(i)); err != nil {
			t.Fatalf("%v", err)
		}
		if err := fake.AddProject(project.Project{
			Name:   remoteProjectName(i),
			Path:   localProjectName(i),
			Remote: fake.Projects[remoteProjectName(i)],
		}); err != nil {
			t.Fatalf("%v", err)
		}
	}

	// Create initial commits in the remote projects and use UpdateUniverse()
	// to mirror them locally.
	for i := 0; i < numProjects; i++ {
		writeReadme(t, fake.X, fake.Projects[remoteProjectName(i)], "revision 1")
	}
	if err := project.UpdateUniverse(fake.X, true, false, false, false, false, project.DefaultHookTimeout); err != nil {
		t.Fatalf("%v", err)
	}

	// Create a snapshot.
	var stdout bytes.Buffer
	fake.X.Context = tool.NewContext(tool.ContextOpts{Stdout: &stdout, Env: fake.X.Context.Env()})

	tmpfile, err := ioutil.TempFile("", "jiri-snapshot-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if err := runSnapshot(fake.X, []string{tmpfile.Name()}); err != nil {
		t.Fatalf("%v", err)
	}

	// Remove the local project repositories.
	for i, _ := range remoteProjects {
		localProject := filepath.Join(fake.X.Root, localProjectName(i))
		if err := os.RemoveAll(localProject); err != nil {
			t.Fatalf("%v", err)
		}
	}

	// Check that invoking the UpdateUniverse() with the snapshot restores the
	// local repositories.
	snapshotFile := tmpfile.Name()
	localX := fake.X.Clone(tool.ContextOpts{
		Manifest: &snapshotFile,
	})
	if err := project.UpdateUniverse(localX, true, false, false, false, false, project.DefaultHookTimeout); err != nil {
		t.Fatalf("%v", err)
	}
	for i, _ := range remoteProjects {
		localProject := filepath.Join(fake.X.Root, localProjectName(i))
		checkReadme(t, fake.X, localProject, "revision 1")
	}
}

// TestSnapshot tests creating source manifest.
func TestSourceManifestSnapshot(t *testing.T) {
	setDefaultSnapshotFlag()
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()

	// Setup the initial remote and local projects.
	numProjects := 3
	for i := 0; i < numProjects; i++ {
		if err := fake.CreateRemoteProject(remoteProjectName(i)); err != nil {
			t.Fatalf("%v", err)
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
		writeReadme(t, fake.X, fake.Projects[remoteProjectName(i)], "revision 1")
	}
	if err := project.UpdateUniverse(fake.X, true, false, false, false, false, project.DefaultHookTimeout); err != nil {
		t.Fatalf("%s", err)
	}

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

	// Create a snapshot.
	var stdout bytes.Buffer
	fake.X.Context = tool.NewContext(tool.ContextOpts{Stdout: &stdout, Env: fake.X.Context.Env()})

	tmpfile, err := ioutil.TempFile("", "jiri-snapshot-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	smTmpfile, err := ioutil.TempFile("", "jiri-sm-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(smTmpfile.Name())
	sourceManifestFilename = smTmpfile.Name()

	if err := runSnapshot(fake.X, []string{tmpfile.Name()}); err != nil {
		t.Fatalf("%v", err)
	}

	sm := &project.SourceManifest{
		Version: project.SourceManifestVersion,
	}
	sm.Directories = make(map[string]*project.SourceManifest_Directory)
	sm.Directories["manifest"] = &project.SourceManifest_Directory{
		GitCheckout: &project.SourceManifest_GitCheckout{
			RepoUrl:     fake.Projects["manifest"],
			Revision:    revMap["manifest"],
			TrackingRef: "refs/heads/master",
		},
	}
	for i := 0; i < numProjects; i++ {
		ref := "refs/heads/master"
		if i == 2 {
			ref = "refs/heads/test-branch"
		}
		sm.Directories[localProjectName(i)] = &project.SourceManifest_Directory{
			GitCheckout: &project.SourceManifest_GitCheckout{
				RepoUrl:     fake.Projects[remoteProjectName(i)],
				Revision:    revMap[localProjectName(i)],
				TrackingRef: ref,
			},
		}
	}

	want, err := json.MarshalIndent(sm, "", "  ")
	if err != nil {
		t.Fatalf("failed to serialize JSON output: %s\n", err)
	}

	got, _ := ioutil.ReadFile(smTmpfile.Name())
	if string(got) != string(want) {
		t.Fatalf("%s, \n%s", (string(got)), string(want))
	}
}
