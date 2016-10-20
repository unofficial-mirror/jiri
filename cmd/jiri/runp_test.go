// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/jiritest"
	"fuchsia.googlesource.com/jiri/project"
)

var (
	buildJiriOnce   sync.Once
	buildJiriBinDir = ""
)

func buildJiri(t *testing.T) string {
	buildJiriOnce.Do(func() {
		binDir, err := ioutil.TempDir("", "")
		if err != nil {
			t.Fatal(err)
		}
		buildGoPkg(t, "fuchsia.googlesource.com/jiri/cmd/jiri", binDir)
		buildJiriBinDir = binDir
	})
	return buildJiriBinDir
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

func run(t *testing.T, dir, bin string, args ...string) string {
	cmd := exec.Command(filepath.Join(dir, bin), args...)
	stdout, stderr := runCmd(t, cmd, false)
	return strings.TrimSpace(strings.Join([]string{stdout, stderr}, " "))
}

func TestMain(m *testing.M) {
	flag.Parse()
	r := m.Run()
	if buildJiriBinDir != "" {
		os.RemoveAll(buildJiriBinDir)
	}
	os.Exit(r)
}

func TestRunP(t *testing.T) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()
	projects := addProjects(t, fake)
	dir := buildJiri(t)

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

	got := run(t, dir, "jiri", "runp", "--show-name-prefix", "-v", "echo")
	hdr := "Project Names: manifest r.a r.b r.c sub/r.t1 sub/sub2/r.t2\n"
	hdr += "Project Keys: " + strings.Join(keys, " ") + "\n"

	if want := hdr + "manifest: \nr.a: \nr.b: \nr.c: \nsub/r.t1: \nsub/sub2/r.t2:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	got = run(t, dir, "jiri", "runp", "-v", "--interactive=false", "basename", "$(", filepath.Join(dir, "jiri"), "project", "info", "-f", "{{.Project.Path}}", ")")
	if want := hdr + "manifest\nr.a\nr.b\nr.c\nr.t1\nr.t2"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	got = run(t, dir, "jiri", "runp", "--interactive=false", "git", "rev-parse", "--abbrev-ref", "HEAD")
	if want := "master\nmaster\nmaster\nmaster\nmaster\nmaster"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	got = run(t, dir, "jiri", "runp", "-interactive=false", "--show-name-prefix=true", "git", "rev-parse", "--abbrev-ref", "HEAD")
	if want := "manifest: master\nr.a: master\nr.b: master\nr.c: master\nsub/r.t1: master\nsub/sub2/r.t2: master"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	got = run(t, dir, "jiri", "runp", "--interactive=false", "--show-key-prefix=true", "git", "rev-parse", "--abbrev-ref", "HEAD")
	if want := strings.Join(keys, ": master\n") + ": master"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	uncollated := run(t, dir, "jiri", "runp", "--interactive=false", "--collate-stdout=false", "--show-name-prefix=true", "git", "rev-parse", "--abbrev-ref", "HEAD")
	split := strings.Split(uncollated, "\n")
	sort.Strings(split)
	got = strings.TrimSpace(strings.Join(split, "\n"))
	if want := "manifest: master\nr.a: master\nr.b: master\nr.c: master\nsub/r.t1: master\nsub/sub2/r.t2: master"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	got = run(t, dir, "jiri", "runp", "--show-name-prefix", "--projects=r.t[12]", "echo")
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

	got = run(t, dir, "jiri", "runp", "--untracked", "--show-name-prefix", "echo")
	if want := "r.b:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	got = run(t, dir, "jiri", "runp", "--no-untracked", "--show-name-prefix", "echo")
	if want := "manifest: \nr.a: \nr.c: \nsub/r.t1: \nsub/sub2/r.t2:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	newfile(rc, "uncommitted.go")

	if err := git(rc).Add("uncommitted.go"); err != nil {
		t.Error(err)
	}

	got = run(t, dir, "jiri", "runp", "--uncommitted", "--show-name-prefix", "echo")
	if want := "r.c:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	got = run(t, dir, "jiri", "runp", "--no-uncommitted", "--show-name-prefix", "echo")
	if want := "manifest: \nr.a: \nr.b: \nsub/r.t1: \nsub/sub2/r.t2:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	// test ordering of has-<x> flags
	newfile(rc, "untracked.go")
	got = run(t, dir, "jiri", "runp", "--untracked", "--uncommitted", "--show-name-prefix", "echo")
	if want := "r.c:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	got = run(t, dir, "jiri", "runp", "--uncommitted", "--untracked", "--show-name-prefix", "echo")
	if want := "r.c:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	git(rb).CreateAndCheckoutBranch("a1")
	git(rb).CreateAndCheckoutBranch("b2")
	git(rc).CreateAndCheckoutBranch("b2")
	git(t1).CreateAndCheckoutBranch("a1")

	chdir(rc)

	// Just the projects with branch b2.
	got = run(t, dir, "jiri", "runp", "--show-name-prefix", "echo")
	if want := "r.b: \nr.c:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	// All projects since --projects takes precendence over branches.
	got = run(t, dir, "jiri", "runp", "--projects=.*", "--show-name-prefix", "echo")
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

	got = run(t, dir, "jiri", "runp", "--gerrit-message", "--show-name-prefix", "echo")
	if want := "r.b:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	got = run(t, dir, "jiri", "runp", "--no-gerrit-message", "--show-name-prefix", "echo")
	if want := "sub/r.t1:"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

}
