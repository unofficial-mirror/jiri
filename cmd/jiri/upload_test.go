// Copyright 2016 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/gerrit"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/jiritest"
	"fuchsia.googlesource.com/jiri/project"
)

func projectName(i int) string {
	return fmt.Sprintf("project-%d", i)
}

// setupUniverse creates a fake jiri root with 3 remote projects.  Each project
// has a README with text "initial readme".
func setupUniverse(t *testing.T) ([]project.Project, *jiritest.FakeJiriRoot, func()) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	success := false
	defer func() {
		if !success {
			cleanup()
		}
	}()

	// Create some projects and add them to the remote manifest.
	numProjects := 3
	localProjects := []project.Project{}
	for i := 0; i < numProjects; i++ {
		name := projectName(i)
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

	// Create initial commit in each repo.
	for _, remoteProjectDir := range fake.Projects {
		writeReadme(t, fake.X, remoteProjectDir, "initial readme")
	}

	success = true
	return localProjects, fake, cleanup
}

// setupTest creates a setup for testing the upload tool.
func setupUploadTest(t *testing.T) (*jiritest.FakeJiriRoot, []project.Project, func()) {
	localProjects, fake, cleanup := setupUniverse(t)
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	return fake, localProjects, cleanup
}

func assertUploadPushedFilesToRef(t *testing.T, jirix *jiri.X, gerritPath, pushedRef string, files []string) {
	currentDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(currentDir); err != nil {
			t.Fatal(err)
		}
	}()
	if err := os.Chdir(gerritPath); err != nil {
		t.Fatal(err)
	}
	if err := gitutil.New(jirix.NewSeq()).CheckoutBranch(pushedRef); err != nil {
		t.Fatalf("%v", err)
	}
	assertFilesCommitted(t, jirix, files)
}

func resetFlags() {
	uploadCcsFlag = ""
	uploadHostFlag = ""
	uploadPresubmitFlag = string(gerrit.PresubmitTestTypeAll)
	uploadReviewersFlag = ""
	uploadTopicFlag = ""
	uploadVerifyFlag = true
}

func TestUploadSimple(t *testing.T) {
	defer resetFlags()
	fake, localProjects, cleanup := setupUploadTest(t)
	defer cleanup()
	currentDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(currentDir); err != nil {
			t.Fatal(err)
		}
	}()
	if err := os.Chdir(localProjects[1].Path); err != nil {
		t.Fatal(err)
	}
	branch := "my-branch"
	git := gitutil.New(fake.X.NewSeq(), gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"))
	if err := git.CreateBranchWithUpstream(branch, "origin/master"); err != nil {
		t.Fatalf("%v", err)
	}
	if err := git.CheckoutBranch(branch); err != nil {
		t.Fatalf("%v", err)
	}
	files := []string{"file1"}
	commitFiles(t, fake.X, files)

	gerritPath := fake.Projects[localProjects[1].Name]
	uploadHostFlag = gerritPath
	if err := runUpload(fake.X, []string{}); err != nil {
		t.Fatal(err)
	}

	expectedRef := "refs/for/master"
	assertUploadPushedFilesToRef(t, fake.X, gerritPath, expectedRef, files)
}

func TestUploadMultipleCommits(t *testing.T) {
	defer resetFlags()
	fake, localProjects, cleanup := setupUploadTest(t)
	defer cleanup()
	currentDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(currentDir); err != nil {
			t.Fatal(err)
		}
	}()
	if err := os.Chdir(localProjects[1].Path); err != nil {
		t.Fatal(err)
	}
	branch := "my-branch"
	git := gitutil.New(fake.X.NewSeq(), gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"))
	if err := git.CreateBranchWithUpstream(branch, "origin/master"); err != nil {
		t.Fatalf("%v", err)
	}
	if err := git.CheckoutBranch(branch); err != nil {
		t.Fatalf("%v", err)
	}
	files1 := []string{"file1"}
	commitFiles(t, fake.X, files1)

	files2 := []string{"file2"}
	commitFiles(t, fake.X, files2)

	gerritPath := fake.Projects[localProjects[1].Name]
	uploadHostFlag = gerritPath
	if err := runUpload(fake.X, []string{}); err != nil {
		t.Fatal(err)
	}

	expectedRef := "refs/for/master"
	assertUploadPushedFilesToRef(t, fake.X, gerritPath, expectedRef, append(files1, files2...))
}

func TestUploadThrowsErrorWhenNotOnBranch(t *testing.T) {
	defer resetFlags()
	fake, localProjects, cleanup := setupUploadTest(t)
	defer cleanup()
	currentDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(currentDir); err != nil {
			t.Fatal(err)
		}
	}()
	if err := os.Chdir(localProjects[1].Path); err != nil {
		t.Fatal(err)
	}
	git := gitutil.New(fake.X.NewSeq(), gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"))
	if err := git.CheckoutBranch("HEAD", gitutil.DetachOpt(true)); err != nil {
		t.Fatalf("%v", err)
	}
	files := []string{"file1"}
	commitFiles(t, fake.X, files)

	if err := runUpload(fake.X, []string{}); err == nil {
		t.Fatalf("Should have got a error here.")
	} else if !strings.Contains(err.Error(), "project is not on any branch") {
		t.Fatalf("Wrong error: %v", err)
	}
}

func TestUploadFailsWhenNoGerritHost(t *testing.T) {
	defer resetFlags()
	fake, localProjects, cleanup := setupUploadTest(t)
	defer cleanup()
	currentDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(currentDir); err != nil {
			t.Fatal(err)
		}
	}()
	if err := os.Chdir(localProjects[1].Path); err != nil {
		t.Fatal(err)
	}
	branch := "my-branch"
	git := gitutil.New(fake.X.NewSeq(), gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"))
	if err := git.CreateBranchWithUpstream(branch, "origin/master"); err != nil {
		t.Fatalf("%v", err)
	}
	if err := git.CheckoutBranch(branch); err != nil {
		t.Fatalf("%v", err)
	}
	files := []string{"file1"}
	commitFiles(t, fake.X, files)

	if err := runUpload(fake.X, []string{}); err == nil {
		t.Fatalf("Should have got a error here.")
	} else if !strings.Contains(err.Error(), "Please use the '--host' flag") {
		t.Fatalf("Wrong error: %v", err)
	}
}

func TestUploadFailsForUntrackedBranch(t *testing.T) {
	defer resetFlags()
	fake, localProjects, cleanup := setupUploadTest(t)
	defer cleanup()
	currentDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(currentDir); err != nil {
			t.Fatal(err)
		}
	}()
	if err := os.Chdir(localProjects[1].Path); err != nil {
		t.Fatal(err)
	}
	branch := "my-branch"
	git := gitutil.New(fake.X.NewSeq(), gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"))
	if err := git.CreateAndCheckoutBranch(branch); err != nil {
		t.Fatalf("%v", err)
	}
	files := []string{"file1"}
	commitFiles(t, fake.X, files)

	gerritPath := fake.Projects[localProjects[1].Name]
	uploadHostFlag = gerritPath
	if err := runUpload(fake.X, []string{}); err == nil {
		t.Fatalf("Should have got a error here.")
	} else if !strings.Contains(err.Error(), "branch is un-tracked or tracks a local un-tracked branch") {
		t.Fatalf("Wrong error: %v", err)
	}
}
