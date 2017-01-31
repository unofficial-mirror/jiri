// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/jiritest"
	"fuchsia.googlesource.com/jiri/project"
)

func TestStatus(t *testing.T) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// Add projects
	numProjects := 3
	localProjects := []project.Project{}
	for i := 0; i < numProjects; i++ {
		name := fmt.Sprintf("project-%d", i)
		path := fmt.Sprintf("path-%d", i)
		if err := fake.CreateRemoteProject(name); err != nil {
			t.Fatal(err)
		}
		p := project.Project{
			Name:   name,
			Path:   filepath.Join(fake.X.Root, path),
			Remote: fake.Projects[name],
		}
		localProjects = append(localProjects, p)
		if err := fake.AddProject(p); err != nil {
			t.Fatal(err)
		}
	}
	var file2CommitRevs []string
	var file1CommitRevs []string
	var latestCommitRevs []string
	var relativePaths []string
	s := fake.X.NewSeq()
	for i, localProject := range localProjects {
		setDummyUser(t, fake.X, fake.Projects[localProject.Name])
		gitRemote := gitutil.New(s, gitutil.RootDirOpt(fake.Projects[localProject.Name]))
		writeFile(t, fake.X, fake.Projects[localProject.Name], "file1"+strconv.Itoa(i), "file1"+strconv.Itoa(i))
		file1CommitRev, _ := gitRemote.CurrentRevision()
		file1CommitRevs = append(file1CommitRevs, file1CommitRev)
		writeFile(t, fake.X, fake.Projects[localProject.Name], "file2"+strconv.Itoa(i), "file2"+strconv.Itoa(i))
		file2CommitRev, _ := gitRemote.CurrentRevision()
		file2CommitRevs = append(file2CommitRevs, file2CommitRev)
		writeFile(t, fake.X, fake.Projects[localProject.Name], "file3"+strconv.Itoa(i), "file3"+strconv.Itoa(i))
		file3CommitRev, _ := gitRemote.CurrentRevision()
		latestCommitRevs = append(latestCommitRevs, file3CommitRev)
		relativePath, _ := filepath.Rel(cwd, localProject.Path)
		relativePaths = append(relativePaths, relativePath)
	}
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	// Test no changes
	got := executeStatus(t, fake, "")
	want := ""
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	// Test when HEAD is on different revsion
	gitLocal := gitutil.New(s, gitutil.RootDirOpt(localProjects[1].Path))
	gitLocal.CheckoutBranch("HEAD~1")
	gitLocal = gitutil.New(s, gitutil.RootDirOpt(localProjects[2].Path))
	gitLocal.CheckoutBranch("HEAD~2")
	got = executeStatus(t, fake, "")
	want = fmt.Sprintf("%v(%v): Should be on revision %q, but is on revision %q\n", localProjects[1].Name, relativePaths[1], latestCommitRevs[1], file2CommitRevs[1])
	want = fmt.Sprintf("%v\n%v(%v): Should be on revision %q, but is on revision %q", want, localProjects[2].Name, relativePaths[2], latestCommitRevs[2], file1CommitRevs[2])
	if !equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	newfile := func(dir, file string) {
		testfile := filepath.Join(dir, file)
		_, err := s.Create(testfile)
		if err != nil {
			t.Errorf("failed to create %s: %v", testfile, err)
		}
	}

	// Test combinations of tracked and untracked changes
	newfile(localProjects[0].Path, "untracked1")
	newfile(localProjects[0].Path, "untracked2")
	newfile(localProjects[2].Path, "uncommitted.go")
	if err := gitLocal.Add("uncommitted.go"); err != nil {
		t.Error(err)
	}
	got = executeStatus(t, fake, "")
	want = fmt.Sprintf("%v(%v): \n?? untracked1\n?? untracked2\n", localProjects[0].Name, relativePaths[0])
	want = fmt.Sprintf("%v\n%v(%v): Should be on revision %q, but is on revision %q\n", want, localProjects[1].Name, relativePaths[1], latestCommitRevs[1], file2CommitRevs[1])
	want = fmt.Sprintf("%v\n%v(%v): Should be on revision %q, but is on revision %q", want, localProjects[2].Name, relativePaths[2], latestCommitRevs[2], file1CommitRevs[2])
	want = fmt.Sprintf("%v\nA  uncommitted.go", want)
	if !equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func equal(first, second string) bool {
	firstStrings := strings.Split(first, "\n\n")
	secondStrings := strings.Split(second, "\n\n")
	if len(firstStrings) != len(secondStrings) {
		return false
	}
	sort.Strings(firstStrings)
	sort.Strings(secondStrings)
	for i, first := range firstStrings {
		if first != secondStrings[i] {
			return false
		}
	}
	return true
}

func executeStatus(t *testing.T, fake *jiritest.FakeJiriRoot, args ...string) string {
	stderr := ""
	runCmd := func() {
		if err := runStatus(fake.X, args); err != nil {
			stderr = err.Error()
		}
	}
	stdout, _, err := runfunc(runCmd)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(strings.Join([]string{stdout, stderr}, " "))
}

func writeFile(t *testing.T, jirix *jiri.X, projectDir, fileName, message string) {
	path, perm := filepath.Join(projectDir, fileName), os.FileMode(0644)
	if err := ioutil.WriteFile(path, []byte(message), perm); err != nil {
		t.Fatalf("WriteFile(%v, %v) failed: %v", path, perm, err)
	}
	if err := gitutil.New(jirix.NewSeq(), gitutil.RootDirOpt(projectDir)).CommitFile(path, message); err != nil {
		t.Fatal(err)
	}
}

func setDummyUser(t *testing.T, jirix *jiri.X, projectDir string) {
	git := gitutil.New(jirix.NewSeq(), gitutil.RootDirOpt(projectDir))
	if err := git.Config("user.email", "john.doe@example.com"); err != nil {
		t.Fatalf("%v", err)
	}
	if err := git.Config("user.name", "John Doe"); err != nil {
		t.Fatalf("%v", err)
	}
}
