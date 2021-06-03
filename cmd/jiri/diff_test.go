// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"go.fuchsia.dev/jiri/jiritest"
	"go.fuchsia.dev/jiri/project"
)

// Setup two snapshots and also return their diff
func setUpSnapshots(t *testing.T, rootDir string) ([]byte, []byte, *Diff) {
	n := 7
	m1 := &project.Manifest{Version: project.ManifestVersion}
	m2 := &project.Manifest{Version: project.ManifestVersion}
	m1.Projects = make([]project.Project, n)
	m2.Projects = make([]project.Project, n)
	for i := 0; i < n; i++ {
		m1.Projects[i].Name = fmt.Sprintf("project-%d", i)
		m1.Projects[i].Remote = "remote-url"
		m1.Projects[i].Revision = fmt.Sprintf("revision-%d", i)
		m1.Projects[i].GerritHost = ""
		m1.Projects[i].Path = fmt.Sprintf("path-%d", i)
		m2.Projects[i] = m1.Projects[i]
	}

	d := &Diff{}
	d.NewProjects = make([]DiffProject, 2)
	d.DeletedProjects = make([]DiffProject, 2)
	d.UpdatedProjects = make([]DiffProject, 2)

	//Simulate delete and new
	i := 2
	d.DeletedProjects[0].Name = m1.Projects[i].Name
	d.DeletedProjects[0].Remote = m1.Projects[i].Remote
	d.DeletedProjects[0].Path = filepath.Join(rootDir, m1.Projects[i].Path)
	d.DeletedProjects[0].RelativePath = m1.Projects[i].Path
	d.DeletedProjects[0].Revision = m1.Projects[i].Revision
	d.NewProjects[0] = d.DeletedProjects[0]
	m2.Projects[i].Name = fmt.Sprintf("new-project-%d", i)
	m2.Projects[i].Path = fmt.Sprintf("new-path-%d", i)
	d.NewProjects[0].Name = m2.Projects[i].Name
	d.NewProjects[0].Path = filepath.Join(rootDir, m2.Projects[i].Path)
	d.NewProjects[0].RelativePath = m2.Projects[i].Path

	i = 4
	d.DeletedProjects[1].Name = m1.Projects[i].Name
	d.DeletedProjects[1].Remote = m1.Projects[i].Remote
	d.DeletedProjects[1].Path = filepath.Join(rootDir, m1.Projects[i].Path)
	d.DeletedProjects[1].RelativePath = m1.Projects[i].Path
	d.DeletedProjects[1].Revision = m1.Projects[i].Revision
	d.NewProjects[1] = d.DeletedProjects[1]
	m2.Projects[i].Name = fmt.Sprintf("new-project-%d", i)
	m2.Projects[i].Path = fmt.Sprintf("new-path-%d", i)
	d.NewProjects[1].Name = m2.Projects[i].Name
	d.NewProjects[1].Path = filepath.Join(rootDir, m2.Projects[i].Path)
	d.NewProjects[1].RelativePath = m2.Projects[i].Path

	// update revision
	i = 0
	m2.Projects[i].Revision = fmt.Sprintf("new-rev-%d", i)
	d.UpdatedProjects[0].Name = m1.Projects[i].Name
	d.UpdatedProjects[0].Remote = m1.Projects[i].Remote
	d.UpdatedProjects[0].Path = filepath.Join(rootDir, m1.Projects[i].Path)
	d.UpdatedProjects[0].RelativePath = m1.Projects[i].Path
	d.UpdatedProjects[0].OldRevision = m1.Projects[i].Revision
	d.UpdatedProjects[0].Revision = m2.Projects[i].Revision
	d.UpdatedProjects[0].Error = "no gerrit host"

	// move project
	i = 1
	m2.Projects[i].Path = fmt.Sprintf("new-path-%d", i)
	d.UpdatedProjects[1].Name = m1.Projects[i].Name
	d.UpdatedProjects[1].Remote = m1.Projects[i].Remote
	d.UpdatedProjects[1].Path = filepath.Join(rootDir, m2.Projects[i].Path)
	d.UpdatedProjects[1].RelativePath = m2.Projects[i].Path
	d.UpdatedProjects[1].OldPath = filepath.Join(rootDir, m1.Projects[i].Path)
	d.UpdatedProjects[1].OldRelativePath = m1.Projects[i].Path
	d.UpdatedProjects[1].Revision = m1.Projects[i].Revision

	// rename project
	i = 3
	m2.Projects[i].Name = fmt.Sprintf("new-project-%d", i)
	b1, err := m1.ToBytes()
	if err != nil {
		t.Fatal(err)
	}

	b2, err := m2.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return b1, b2, d.Sort()

}

func TestDiffLocalSnapshots(t *testing.T) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()
	s1, s2, d := setUpSnapshots(t, fake.X.Root)
	if string(s1) == string(s2) {
		t.Fatal("e")
	}
	want, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	t1, err := ioutil.TempFile("", "test-diff")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(t1.Name())
	defer t1.Close()
	if _, err := t1.Write(s1); err != nil {
		t.Fatal(err)
	}
	t1.Sync()

	t2, err := ioutil.TempFile("", "test-diff")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(t2.Name())
	defer t2.Close()
	if _, err := t2.Write(s2); err != nil {
		t.Fatal(err)
	}
	t2.Sync()

	diff, err := getDiff(fake.X, t1.Name(), t2.Name())
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(diff)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("Error, got: %s\n\nwant:%s", got, want)
	}
}

func TestDiffSnapshotsUrl(t *testing.T) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()

	s1, s2, d := setUpSnapshots(t, fake.X.Root)
	want, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}

	serverMux := http.NewServeMux()
	serverMux.HandleFunc("/1", func(rw http.ResponseWriter, r *http.Request) {
		rw.Write(s1)
	})
	serverMux.HandleFunc("/2", func(rw http.ResponseWriter, r *http.Request) {
		rw.Write(s2)
	})
	server := httptest.NewServer(serverMux)
	defer server.Close()

	diff, err := getDiff(fake.X, server.URL+"/1", server.URL+"/2")
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(diff)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("Error, got: %s\n\nwant:%s", got, want)
	}
}
