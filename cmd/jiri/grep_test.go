// Copyright 2016 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/jiritest"
	"fuchsia.googlesource.com/jiri/project"
)

func setDefaultGrepFlags() {
	grepFlags.n = false
	grepFlags.e = ""
	grepFlags.h = true
	grepFlags.i = false
	grepFlags.l = false
	grepFlags.L = false
	grepFlags.w = false
}

func makeProjects(t *testing.T, fake *jiritest.FakeJiriRoot) []*project.Project {
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
			t.Fatal(err)
		}
		p := project.Project{
			Name:         projectPath,
			Path:         filepath.Join(fake.X.Root, projectPath),
			Remote:       fake.Projects[projectPath],
			RemoteBranch: "master",
		}
		if err := fake.AddProject(p); err != nil {
			t.Fatal(err)
		}
		projects = append(projects, &p)
	}
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	return projects
}

func expectGrep(t *testing.T, fake *jiritest.FakeJiriRoot, args []string, expected []string) {
	results, err := doGrep(fake.X, args)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(results)
	sort.Strings(expected)
	if len(results) != len(expected) {
		t.Fatalf("grep %v, expected %d matches, got %d matches", args, len(expected), len(results))
	}
	for i, result := range results {
		if result != expected[i] {
			t.Fatalf("grep %v, expected:\n%s\ngot:\n%s", args, expected[i], result)
		}
	}
}
func setup(t *testing.T, fake *jiritest.FakeJiriRoot) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	os.Chdir(fake.X.Root)

	projects := makeProjects(t, fake)

	files := []string{
		"Shall I compare thee to a summer's day?",
		"Thou art more lovely and more temperate:",
		"And summer's lease hath all too short a date:",
		"Sometime too hot the eye of heaven shines,",
		"line with -hyphen",
	}

	if got, want := len(projects), len(files); got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	for i, project := range projects {
		path := project.Path + "/file.txt"
		err := ioutil.WriteFile(path, []byte(files[i]), 0644)
		if err != nil {
			t.Fatal(err)
		}
		git := gitutil.New(fake.X, gitutil.RootDirOpt(project.Path))
		git.Add(path)
	}
}
func TestGrep(t *testing.T) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()

	setup(t, fake)
	setDefaultGrepFlags()
	expectGrep(t, fake, []string{"too"}, []string{
		"r.c/file.txt:And summer's lease hath all too short a date:",
		"sub/r.t1/file.txt:Sometime too hot the eye of heaven shines,",
	})

	expectGrep(t, fake, []string{"supercalifragilisticexpialidocious"}, []string{})
}

func TestNFlagGrep(t *testing.T) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()

	setup(t, fake)
	setDefaultGrepFlags()
	grepFlags.n = true
	expectGrep(t, fake, []string{"too"}, []string{
		"r.c/file.txt:1:And summer's lease hath all too short a date:",
		"sub/r.t1/file.txt:1:Sometime too hot the eye of heaven shines,",
	})
}

func TestWFlagGrep(t *testing.T) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()

	setup(t, fake)
	setDefaultGrepFlags()
	grepFlags.w = true
	grepFlags.i = true
	expectGrep(t, fake, []string{"i"}, []string{
		"r.a/file.txt:Shall I compare thee to a summer's day?",
	})
}

func TestEFlagGrep(t *testing.T) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()

	setup(t, fake)
	setDefaultGrepFlags()
	grepFlags.e = "-hyp"
	expectGrep(t, fake, []string{}, []string{
		"sub/sub2/r.t2/file.txt:line with -hyphen",
	})
}

func TestIFlagGrep(t *testing.T) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()

	setup(t, fake)
	setDefaultGrepFlags()
	expectGrep(t, fake, []string{"and"}, []string{
		"r.b/file.txt:Thou art more lovely and more temperate:",
	})

	grepFlags.i = true
	expectGrep(t, fake, []string{"and"}, []string{
		"r.b/file.txt:Thou art more lovely and more temperate:",
		"r.c/file.txt:And summer's lease hath all too short a date:",
	})
}

func TestLFlagGrep(t *testing.T) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()

	setup(t, fake)
	setDefaultGrepFlags()
	grepFlags.l = true
	expectGrep(t, fake, []string{"too"}, []string{
		"r.c/file.txt",
		"sub/r.t1/file.txt",
	})

	setDefaultGrepFlags()
	grepFlags.L = true
	expectGrep(t, fake, []string{"too"}, []string{
		"manifest/public",
		"r.a/file.txt",
		"r.b/file.txt",
		"sub/sub2/r.t2/file.txt",
	})
}
