// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/jiritest"
	"fuchsia.googlesource.com/jiri/project"
)

func setDefaultBranchFlags() {
	branchFlags.forceDeleteFlag = false
	branchFlags.deleteFlag = false
	branchFlags.listFlag = false
}

func createBranchCommits(t *testing.T, fake *jiritest.FakeJiriRoot, localProjects []project.Project) {
	for i, localProject := range localProjects {
		writeFile(t, fake.X, fake.Projects[localProject.Name], "file1"+strconv.Itoa(i), "file1"+strconv.Itoa(i))
	}
}

func createBranchProjects(t *testing.T, fake *jiritest.FakeJiriRoot, numProjects int) []project.Project {
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
	createBranchCommits(t, fake, localProjects)
	return localProjects
}

func TestBranch(t *testing.T) {
	setDefaultBranchFlags()
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()

	// Add projects
	numProjects := 8
	localProjects := createBranchProjects(t, fake, numProjects)
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	gitLocals := make([]*gitutil.Git, numProjects)
	for i, localProject := range localProjects {
		gitLocal := gitutil.New(fake.X, gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"), gitutil.RootDirOpt(localProject.Path))
		gitLocals[i] = gitLocal
	}

	testBranch := "testBranch"

	defaultWant := ""
	branchWant := ""
	cDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativePath := make([]string, numProjects)
	for i, p := range localProjects {
		relativePath[i], err = filepath.Rel(cDir, p.Path)
		if err != nil {
			t.Fatal(err)
		}
	}
	// current branch is not testBranch
	i := 0
	gitLocals[i].CreateBranch(testBranch)
	defaultWant = fmt.Sprintf("%v%v(%v)\n", defaultWant, localProjects[i].Name, relativePath[i])

	i = 2
	gitLocals[i].CreateBranch(testBranch)
	defaultWant = fmt.Sprintf("%v%v(%v)\n", defaultWant, localProjects[i].Name, relativePath[i])

	i = 3
	gitLocals[i].CreateBranch(testBranch)
	defaultWant = fmt.Sprintf("%v%v(%v)\n", defaultWant, localProjects[i].Name, relativePath[i])

	// current branch is test branch
	i = 1
	gitLocals[i].CreateBranch(testBranch)
	gitLocals[i].CheckoutBranch(testBranch)
	branchWant = fmt.Sprintf("%v%v(%v)\n", branchWant, localProjects[i].Name, relativePath[i])

	i = 6
	gitLocals[i].CreateBranch(testBranch)
	gitLocals[i].CheckoutBranch(testBranch)
	branchWant = fmt.Sprintf("%v%v(%v)\n", branchWant, localProjects[i].Name, relativePath[i])

	i = 4
	gitLocals[i].CreateBranch(testBranch)
	gitLocals[i].CheckoutBranch(testBranch)
	branchWant = fmt.Sprintf("%v%v(%v)\n", branchWant, localProjects[i].Name, relativePath[i])
	defaultWant = fmt.Sprintf("%v%v", defaultWant, branchWant)

	// Run default
	if got := executeBranch(t, fake, testBranch); !equalBranchOut(got, defaultWant) {
		t.Errorf("got %v, want %v", got, defaultWant)
	}

	// Run with listFlag
	branchFlags.listFlag = true
	if got := executeBranch(t, fake, testBranch); !equalBranchOut(got, branchWant) {
		t.Errorf("got %v, want %v", got, branchWant)
	}
}

func TestDeleteBranch(t *testing.T) {
	setDefaultBranchFlags()
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()

	// Add projects
	numProjects := 4
	localProjects := createBranchProjects(t, fake, numProjects)
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	gitLocals := make([]*gitutil.Git, numProjects)
	for i, localProject := range localProjects {
		gitLocal := gitutil.New(fake.X, gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"), gitutil.RootDirOpt(localProject.Path))
		gitLocals[i] = gitLocal
	}

	testBranch := "testBranch"

	// Test case when new test branch is on HEAD
	i := 0
	gitLocals[i].CreateBranch(testBranch)

	// Test when git branch -d fails
	i = 1
	gitLocals[i].CreateBranch(testBranch)
	gitLocals[i].CheckoutBranch(testBranch)
	writeFile(t, fake.X, localProjects[i].Path, "extrafile", "extrafile")
	gitLocals[i].CheckoutBranch("master")

	// Test when current branch is test branch
	i = 2
	gitLocals[i].CreateBranch(testBranch)
	gitLocals[i].CheckoutBranch(testBranch)

	// project-3 has no test branch

	projects := make(project.Projects)
	for _, localProject := range localProjects {
		projects[localProject.Key()] = localProject
	}

	// Run on default, should not delete any branch
	executeBranch(t, fake, testBranch)

	states, err := project.GetProjectStates(fake.X, projects, false)
	if err != nil {
		t.Error(err)
	}

	// test project states
	for i = 0; i < numProjects; i++ {
		localProject := localProjects[i]
		state, _ := states[localProject.Key()]
		branchFound := false
		for _, branch := range state.Branches {
			if branch.Name == testBranch {
				branchFound = true
				break
			}
		}
		if i == 0 || i == 1 || i == 2 {
			if !branchFound {
				t.Errorf("project %q should contain branch %q", localProject.Name, testBranch)
			}
		}
	}

	setDefaultBranchFlags()
	branchFlags.deleteFlag = true
	executeBranch(t, fake, testBranch)

	states, err = project.GetProjectStates(fake.X, projects, false)
	if err != nil {
		t.Error(err)
	}

	// test project states
	for i = 0; i < numProjects; i++ {
		localProject := localProjects[i]
		state, _ := states[localProject.Key()]
		branchFound := false
		for _, branch := range state.Branches {
			if branch.Name == testBranch {
				branchFound = true
				break
			}
		}
		if i == 1 || i == 2 {
			if !branchFound {
				t.Errorf("project %q should contain branch %q", localProject.Name, testBranch)
			}
		} else {
			if branchFound {

				t.Errorf("project %q should not contain branch %q", localProject.Name, testBranch)
			}

		}
	}

	setDefaultBranchFlags()
	branchFlags.forceDeleteFlag = true
	executeBranch(t, fake, testBranch)

	states, err = project.GetProjectStates(fake.X, projects, false)
	if err != nil {
		t.Error(err)
	}

	// test project states
	for i = 0; i < numProjects; i++ {
		localProject := localProjects[i]
		state, _ := states[localProject.Key()]
		branchFound := false
		for _, branch := range state.Branches {
			if branch.Name == testBranch {
				branchFound = true
				break
			}
		}
		if i == 2 {
			if !branchFound {
				t.Errorf("project %q should contain branch %q", localProject.Name, testBranch)
			}
		} else {
			if branchFound {

				t.Errorf("project %q should not contain branch %q", localProject.Name, testBranch)
			}

		}
	}
}

func equalBranchOut(first, second string) bool {
	second = strings.TrimSpace(second)
	firstStrings := strings.Split(first, "\n")
	secondStrings := strings.Split(second, "\n")
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

func executeBranch(t *testing.T, fake *jiritest.FakeJiriRoot, args ...string) string {
	stderr := ""
	runCmd := func() {
		if err := runBranch(fake.X, args); err != nil {
			stderr = err.Error()
		}
	}
	stdout, _, err := runfunc(runCmd)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(strings.Join([]string{stdout, stderr}, " "))
}
