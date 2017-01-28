// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/jiritest"
	"fuchsia.googlesource.com/jiri/project"
)

func TestPrintHeadRev(t *testing.T) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	// Add projects
	numProjects := 2
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
	var latestCommitRevs []string
	s := fake.X.NewSeq()
	for i, localProject := range localProjects {
		gitRemote := gitutil.New(s, gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"), gitutil.RootDirOpt(fake.Projects[localProject.Name]))
		writeFile(t, fake.X, fake.Projects[localProject.Name], "file1"+strconv.Itoa(i), "file1"+strconv.Itoa(i))
		writeFile(t, fake.X, fake.Projects[localProject.Name], "file2"+strconv.Itoa(i), "file2"+strconv.Itoa(i))
		file2CommitRev, _ := gitRemote.CurrentRevision()
		file2CommitRevs = append(file2CommitRevs, file2CommitRev)
		writeFile(t, fake.X, fake.Projects[localProject.Name], "file3"+strconv.Itoa(i), "file3"+strconv.Itoa(i))
		file3CommitRev, _ := gitRemote.CurrentRevision()
		latestCommitRevs = append(latestCommitRevs, file3CommitRev)
	}
	manifest, err := fake.ReadRemoteManifest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.Projects[1].Revision = file2CommitRevs[0]
	if err := fake.WriteRemoteManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	// Test that correct haed rev is returned
	expectedRevs := [2]string{file2CommitRevs[0], latestCommitRevs[1]}
	for i, localProject := range localProjects {
		if err := os.Chdir(localProject.Path); err != nil {
			t.Fatal(err)
		}
		if headRev, err := getHeadRev(fake.X); err != nil {
			t.Fatal(err)
		} else {
			if headRev != expectedRevs[i] {
				t.Fatalf("Current commit for project %q is %v, it should be %v\n", localProject.Name, headRev, expectedRevs[i])
			}
		}
	}

	// Test that correct haed rev is returned even when repos are on different revs
	checkoutRevs := [2]string{latestCommitRevs[0], file2CommitRevs[1]}
	for i, localProject := range localProjects {
		gitLocal := gitutil.New(s, gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"), gitutil.RootDirOpt(localProject.Path))
		if err := gitLocal.CheckoutBranch(checkoutRevs[i]); err != nil {
			t.Fatal(err)
		}
	}

	for i, localProject := range localProjects {
		if err := os.Chdir(localProject.Path); err != nil {
			t.Fatal(err)
		}
		if headRev, err := getHeadRev(fake.X); err != nil {
			t.Fatal(err)
		} else {
			if headRev != expectedRevs[i] {
				t.Fatalf("Current commit for project %q is %v, it should be %v\n", localProject.Name, headRev, expectedRevs[i])
			}
		}
	}

}

func writeFile(t *testing.T, jirix *jiri.X, projectDir, fileName, message string) {
	path, perm := filepath.Join(projectDir, fileName), os.FileMode(0644)
	if err := ioutil.WriteFile(path, []byte(message), perm); err != nil {
		t.Fatalf("WriteFile(%v, %v) failed: %v", path, perm, err)
	}
	git := gitutil.New(jirix.NewSeq(), gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"), gitutil.RootDirOpt(projectDir))
	if err := git.CommitFile(path, "creating "+fileName); err != nil {
		t.Fatal(err)
	}
}
