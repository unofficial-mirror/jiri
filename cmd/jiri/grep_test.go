// Copyright 2016 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/jiritest"
	"fuchsia.googlesource.com/jiri/project"
)

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
	if len(results) != len(expected) {
		t.Fatalf("grep %v, expected %d matches, got %d matches", args, len(expected), len(results))
	}
	for i, result := range results {
		if result != expected[i] {
			t.Fatalf("grep %v, expected:\n%s\ngot:\n%s", args, expected[i], result)
		}
	}
}

func TestGrep(t *testing.T) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()

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
		"Rough winds do shake the darling buds of May,",
		"And summer's lease hath all too short a date:",
		"Sometime too hot the eye of heaven shines,",
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
		git := gitutil.New(fake.X.NewSeq(), gitutil.RootDirOpt(project.Path))
		git.Add(path)
	}

	expectGrep(t, fake, []string{"too"}, []string{
		"sub/r.t1/file.txt:And summer's lease hath all too short a date:",
		"sub/sub2/r.t2/file.txt:Sometime too hot the eye of heaven shines,",
	})

	expectGrep(t, fake, []string{"supercalifragilisticexpialidocious"}, []string{})
}
