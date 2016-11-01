// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/jiritest"
	"fuchsia.googlesource.com/jiri/project"
)

func setDefaultRunpFlags() {
	runpFlags.projectKeys = ""
	runpFlags.verbose = false
	runpFlags.interactive = false
	runpFlags.uncommitted = false
	runpFlags.untracked = false
	runpFlags.noUncommitted = false
	runpFlags.noUntracked = false
	runpFlags.showNamePrefix = false
	runpFlags.showKeyPrefix = false
	runpFlags.exitOnError = false
	runpFlags.collateOutput = true
	runpFlags.branch = ""
}

func addProjects(t *testing.T, fake *jiritest.FakeJiriRoot) []*project.Project {
	projects := []*project.Project{}
	for _, name := range []string{"a", "b", "c", "t1", "t2"} {
		projectPath := "r." + name
		if name == "t1" {
			projectPath = "sub/" + projectPath
		}
		if name == "t2" {
			projectPath = "sub/sub2/" + projectPath
		}
		if err := fake.CreateRemoteProject(projectPath); err != nil {
			t.Fatalf("%v", err)
		}
		p := project.Project{
			Name:         projectPath,
			Path:         filepath.Join(fake.X.Root, projectPath),
			Remote:       fake.Projects[projectPath],
			RemoteBranch: "master",
		}
		if err := fake.AddProject(p); err != nil {
			t.Fatalf("%v", err)
		}
		projects = append(projects, &p)
	}
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatalf("%v", err)
	}
	return projects
}

func executeRunp(t *testing.T, fake *jiritest.FakeJiriRoot, args ...string) string {
	stderr := ""
	runCmd := func() {
		if err := runRunp(fake.X, args); err != nil {
			stderr = err.Error()
		}
	}
	stdout, _, err := runfunc(runCmd)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(strings.Join([]string{stdout, stderr}, " "))
}

func TestRunP(t *testing.T) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()
	projects := addProjects(t, fake)

	if got, want := len(projects), 5; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	chdir := func(dir string) {
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
	}

	manifestKey := strings.Replace(string(projects[0].Key()), "r.a", "manifest", -1)
	keys := []string{manifestKey}
	for _, p := range projects {
		keys = append(keys, string(p.Key()))
	}

	chdir(projects[0].Path)
	setDefaultRunpFlags()
	runpFlags.showNamePrefix = true
	runpFlags.verbose = true
	got := executeRunp(t, fake, "echo")
	hdr := "Project Names: manifest r.a r.b r.c sub/r.t1 sub/sub2/r.t2\n"
	hdr += "Project Keys: " + strings.Join(keys, " ") + "\n"

	if want := hdr + "manifest: \nr.a: \nr.b: \nr.c: \nsub/r.t1: \nsub/sub2/r.t2:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	setDefaultRunpFlags()
	runpFlags.interactive = false
	got = executeRunp(t, fake, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if want := "HEAD\nHEAD\nHEAD\nHEAD\nHEAD\nHEAD"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	setDefaultRunpFlags()
	runpFlags.showKeyPrefix = true
	runpFlags.interactive = false
	got = executeRunp(t, fake, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if want := strings.Join(keys, ": HEAD\n") + ": HEAD"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	setDefaultRunpFlags()
	runpFlags.showNamePrefix = true
	runpFlags.interactive = false
	runpFlags.collateOutput = false
	uncollated := executeRunp(t, fake, "git", "rev-parse", "--abbrev-ref", "HEAD")
	split := strings.Split(uncollated, "\n")
	sort.Strings(split)
	got = strings.TrimSpace(strings.Join(split, "\n"))
	if want := "manifest: HEAD\nr.a: HEAD\nr.b: HEAD\nr.c: HEAD\nsub/r.t1: HEAD\nsub/sub2/r.t2: HEAD"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	setDefaultRunpFlags()
	runpFlags.projectKeys = "r.t[12]"
	runpFlags.showNamePrefix = true
	got = executeRunp(t, fake, "echo")
	if want := "sub/r.t1: \nsub/sub2/r.t2:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	rb := projects[1].Path
	rc := projects[2].Path
	t1 := projects[3].Path

	s := fake.X.NewSeq()
	newfile := func(dir, file string) {
		testfile := filepath.Join(dir, file)
		_, err := s.Create(testfile)
		if err != nil {
			t.Errorf("failed to create %s: %v", testfile, err)
		}
	}

	git := func(dir string) *gitutil.Git {
		return gitutil.New(fake.X.NewSeq(), gitutil.RootDirOpt(dir))
	}

	newfile(rb, "untracked.go")
	setDefaultRunpFlags()
	runpFlags.untracked = true
	runpFlags.showNamePrefix = true
	got = executeRunp(t, fake, "echo")
	if want := "r.b:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	setDefaultRunpFlags()
	runpFlags.noUntracked = true
	runpFlags.showNamePrefix = true
	got = executeRunp(t, fake, "echo")
	if want := "manifest: \nr.a: \nr.c: \nsub/r.t1: \nsub/sub2/r.t2:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	newfile(rc, "uncommitted.go")

	if err := git(rc).Add("uncommitted.go"); err != nil {
		t.Error(err)
	}

	setDefaultRunpFlags()
	runpFlags.uncommitted = true
	runpFlags.showNamePrefix = true
	got = executeRunp(t, fake, "echo")
	if want := "r.c:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	setDefaultRunpFlags()
	runpFlags.noUncommitted = true
	runpFlags.showNamePrefix = true
	got = executeRunp(t, fake, "echo")
	if want := "manifest: \nr.a: \nr.b: \nsub/r.t1: \nsub/sub2/r.t2:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	newfile(rc, "untracked.go")
	setDefaultRunpFlags()
	runpFlags.uncommitted = true
	runpFlags.untracked = true
	runpFlags.showNamePrefix = true
	got = executeRunp(t, fake, "echo")
	if want := "r.c:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	git(rb).CreateAndCheckoutBranch("a1")
	git(rb).CreateAndCheckoutBranch("b2")
	git(rc).CreateAndCheckoutBranch("b2")
	git(t1).CreateAndCheckoutBranch("a1")

	chdir(rc)

	// Just the projects with branch b2.
	setDefaultRunpFlags()
	runpFlags.showNamePrefix = true
	got = executeRunp(t, fake, "echo")
	if want := "r.b: \nr.c:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	// All projects since --projects takes precendence over branches.
	setDefaultRunpFlags()
	runpFlags.projectKeys = ".*"
	runpFlags.showNamePrefix = true
	got = executeRunp(t, fake, "echo")
	if want := "manifest: \nr.a: \nr.b: \nr.c: \nsub/r.t1: \nsub/sub2/r.t2:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	if err := s.MkdirAll(filepath.Join(rb, ".jiri", "a1"), os.FileMode(0755)).Done(); err != nil {
		t.Fatal(err)
	}
	newfile(rb, filepath.Join(".jiri", "a1", ".gerrit_commit_message"))

	git(rb).CheckoutBranch("a1")
	git(t1).CheckoutBranch("a1")
	chdir(t1)
}
