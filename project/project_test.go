// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package project_test

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cipd"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/jiritest"
	"fuchsia.googlesource.com/jiri/project"
)

func dirExists(dirname string) error {
	fileInfo, err := os.Stat(dirname)
	if err != nil {
		return err
	}
	if !fileInfo.IsDir() {
		return os.ErrNotExist
	}
	return nil
}

func fileExists(dirname string) error {
	fileInfo, err := os.Stat(dirname)
	if err != nil {
		return err
	}
	if fileInfo.IsDir() {
		return os.ErrNotExist
	}
	return nil
}

func checkReadme(t *testing.T, jirix *jiri.X, p project.Project, message string) {
	if _, err := os.Stat(p.Path); err != nil {
		t.Fatalf("%v", err)
	}
	readmeFile := filepath.Join(p.Path, "README")
	data, err := ioutil.ReadFile(readmeFile)
	if err != nil {
		t.Fatalf("ReadFile(%v) failed: %v", readmeFile, err)
	}
	if got, want := data, []byte(message); bytes.Compare(got, want) != 0 {
		t.Fatalf("unexpected content in project %v:\ngot\n%s\nwant\n%s\n", p.Name, got, want)
	}
}

func checkJiriRevFiles(t *testing.T, jirix *jiri.X, p project.Project) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()

	g := gitutil.New(fake.X, gitutil.RootDirOpt(p.Path))

	file := filepath.Join(p.Path, ".git", "JIRI_HEAD")
	data, err := ioutil.ReadFile(file)
	if err != nil {
		t.Fatalf("ReadFile(%v) failed: %s", file, err)
	}
	headFileContents := string(data)
	headFileCommit, err := g.CurrentRevisionForRef(headFileContents)
	if err != nil {
		t.Fatalf("CurrentRevisionForRef failed: %s", err)
	}

	projectRevision := p.Revision
	if projectRevision == "" {
		if p.RemoteBranch == "" {
			projectRevision = "origin/master"
		} else {
			projectRevision = "origin/" + p.RemoteBranch
		}
	}
	revisionCommit, err := g.CurrentRevisionForRef(projectRevision)
	if err != nil {
		t.Fatalf("CurrentRevisionForRef failed: %s", err)
	}

	if revisionCommit != headFileCommit {
		t.Fatalf("JIRI_HEAD contains %s (%s) expected %s (%s)", headFileContents, headFileCommit, projectRevision, revisionCommit)
	}
	file = filepath.Join(p.Path, ".git", "JIRI_LAST_BASE")
	data, err = ioutil.ReadFile(file)
	if err != nil {
		t.Fatalf("ReadFile(%v) failed: %s", file, err)
	}
	if rev, err := g.CurrentRevision(); err != nil {
		t.Fatalf("CurrentRevision() failed: %s", err)
	} else if rev != string(data) {
		t.Fatalf("JIRI_LAST_BASE contains %s expected %s", string(data), rev)
	}
}

func commitFile(t *testing.T, jirix *jiri.X, dir, file, msg string) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := gitutil.New(jirix, gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com")).CommitFile(file, msg); err != nil {
		t.Fatal(err)
	}
}

func projectName(i int) string {
	return fmt.Sprintf("project-%d", i)
}

func writeUncommitedFile(t *testing.T, jirix *jiri.X, projectDir, fileName, message string) string {
	path, perm := filepath.Join(projectDir, fileName), os.FileMode(0644)
	if err := ioutil.WriteFile(path, []byte(message), perm); err != nil {
		t.Fatalf("WriteFile(%v, %v) failed: %v", path, perm, err)
	}
	return path
}
func writeFile(t *testing.T, jirix *jiri.X, projectDir, fileName, message string) {
	path := writeUncommitedFile(t, jirix, projectDir, fileName, message)
	commitFile(t, jirix, projectDir, path, "creating "+fileName)
}

func writeReadme(t *testing.T, jirix *jiri.X, projectDir, message string) {
	writeFile(t, jirix, projectDir, "README", message)
}

func checkProjectsMatchPaths(t *testing.T, gotProjects project.Projects, wantProjectPaths []string) {
	gotProjectPaths := []string{}
	for _, p := range gotProjects {
		gotProjectPaths = append(gotProjectPaths, p.Path)
	}
	sort.Strings(gotProjectPaths)
	sort.Strings(wantProjectPaths)
	if !reflect.DeepEqual(gotProjectPaths, wantProjectPaths) {
		t.Errorf("project paths got %v, want %v", gotProjectPaths, wantProjectPaths)
	}
}

// TestLocalProjects tests the behavior of the LocalProjects method with
// different ScanModes.
func TestLocalProjects(t *testing.T) {
	jirix, cleanup := jiritest.NewX(t)
	defer cleanup()

	// Create some projects.
	numProjects, projectPaths := 3, []string{}
	for i := 0; i < numProjects; i++ {
		name := projectName(i)
		path := filepath.Join(jirix.Root, name)
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}

		// Initialize empty git repository.  The commit is necessary, otherwise
		// "git rev-parse master" fails.
		git := gitutil.New(jirix, gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"), gitutil.RootDirOpt(path))
		if err := git.Init(path); err != nil {
			t.Fatal(err)
		}
		if err := git.Commit(); err != nil {
			t.Fatal(err)
		}

		// Write project metadata.
		p := project.Project{
			Path: path,
			Name: name,
		}
		if err := project.InternalWriteMetadata(jirix, p, path); err != nil {
			t.Fatalf("writeMetadata %v %v) failed: %v\n", p, path, err)
		}
		projectPaths = append(projectPaths, path)
	}

	// Create a latest update snapshot but only tell it about the first project.
	manifest := project.Manifest{
		Version: project.ManifestVersion,
		Projects: []project.Project{
			{
				Name: projectName(0),
				Path: projectPaths[0],
			},
		},
	}
	if err := os.MkdirAll(jirix.UpdateHistoryDir(), 0755); err != nil {
		t.Fatalf("MkdirAll(%v) failed: %v", jirix.UpdateHistoryDir(), err)
	}
	if err := manifest.ToFile(jirix, jirix.UpdateHistoryLatestLink()); err != nil {
		t.Fatalf("manifest.ToFile(%v) failed: %v", jirix.UpdateHistoryLatestLink(), err)
	}

	// LocalProjects with scanMode = FastScan should only find the first
	// project.
	foundProjects, err := project.LocalProjects(jirix, project.FastScan)
	if err != nil {
		t.Fatalf("LocalProjects(%v) failed: %v", project.FastScan, err)
	}
	checkProjectsMatchPaths(t, foundProjects, projectPaths[:1])

	// LocalProjects with scanMode = FullScan should find all projects.
	foundProjects, err = project.LocalProjects(jirix, project.FullScan)
	if err != nil {
		t.Fatalf("LocalProjects(%v) failed: %v", project.FastScan, err)
	}
	checkProjectsMatchPaths(t, foundProjects, projectPaths[:])

	// Check that deleting a project forces LocalProjects to run a full scan,
	// even if FastScan is specified.
	if err := os.RemoveAll(projectPaths[0]); err != nil {
		t.Fatalf("RemoveAll(%s) failed: %s", projectPaths[0], err)
	}
	foundProjects, err = project.LocalProjects(jirix, project.FastScan)
	if err != nil {
		t.Fatalf("LocalProjects(%v) failed: %v", project.FastScan, err)
	}
	checkProjectsMatchPaths(t, foundProjects, projectPaths[1:])
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
	numProjects := 7
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
	}
	localProjects[2].HistoryDepth = 1
	localProjects[3].Path = filepath.Join(localProjects[2].Path, "path-3")
	localProjects[4].Path = filepath.Join(localProjects[3].Path, "path-4")
	localProjects[5].Path = filepath.Join(localProjects[2].Path, "path-5")
	localProjects[6].Path = filepath.Join(localProjects[0].Path, "path-6")
	for _, p := range localProjects {
		if err := fake.AddProject(p); err != nil {
			t.Fatal(err)
		}
	}

	// Create initial commit in each repo.
	for _, remoteProjectDir := range fake.Projects {
		writeReadme(t, fake.X, remoteProjectDir, "initial readme")
	}
	writeFile(t, fake.X, fake.Projects[localProjects[2].Name], ".gitignore", "path-3/\npath-5/\n")
	writeFile(t, fake.X, fake.Projects[localProjects[0].Name], ".gitignore", "path-6/\n")
	writeFile(t, fake.X, fake.Projects[localProjects[3].Name], ".gitignore", "path-4/\n")

	success = true
	return localProjects, fake, cleanup
}

// TestUpdateUniverseSimple tests that UpdateUniverse will pull remote projects
// locally, and that jiri metadata is ignored in the repos.
func TestUpdateUniverseSimple(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()

	// Check that calling UpdateUniverse() creates local copies of the remote
	// repositories.
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	for _, p := range localProjects {
		if err := dirExists(p.Path); err != nil {
			t.Fatalf("expected project to exist at path %q but none found", p.Path)
		}
		if branches, _, err := gitutil.New(fake.X, gitutil.RootDirOpt(p.Path)).GetBranches(); err != nil {
			t.Fatal(err)
		} else if len(branches) != 0 {
			t.Fatalf("expected project %s(%s) to contain no branches but it contains %s", p.Name, p.Path, branches)
		}
		checkReadme(t, fake.X, p, "initial readme")
		checkJiriRevFiles(t, fake.X, p)
	}
}

func TestUpdateUniverseWhenLocalTracksLocal(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()

	// Check that calling UpdateUniverse() creates local copies of the remote
	// repositories, and that jiri metadata is ignored by git.
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	gitLocal := gitutil.New(fake.X, gitutil.RootDirOpt(localProjects[1].Path))
	gitLocal.CreateBranchWithUpstream("A", "origin/master")
	gitLocal.CreateBranch("B")
	gitLocal.SetUpstream("B", "A")
	writeFile(t, fake.X, fake.Projects[localProjects[1].Name], "file1", "file1")
	gitRemote := gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[localProjects[1].Name]))
	remoteRev, _ := gitRemote.CurrentRevision()
	if err := project.UpdateUniverse(fake.X, false, false, false, false, true /*rebase-all*/, true /*run-hooks*/, true /*run-packages*/, project.DefaultHookTimeout, project.DefaultPackageTimeout); err != nil {
		t.Fatal(err)
	}
	projects, err := project.LocalProjects(fake.X, project.FastScan)
	if err != nil {
		t.Fatal(err)
	}
	states, err := project.GetProjectStates(fake.X, projects, false)
	if err != nil {
		t.Fatal(err)
	}
	state := states[localProjects[1].Key()]
	for _, b := range state.Branches {
		if b.Revision != remoteRev {
			t.Fatalf("Branch %q should have rev %q, instead it has %q", b.Name, remoteRev, b.Revision)
		}
	}
}

func TestUpdateUniverseWhenLocalTracksEachOther(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()

	// Check that calling UpdateUniverse() creates local copies of the remote
	// repositories, and that jiri metadata is ignored by git.
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	gitLocal := gitutil.New(fake.X, gitutil.RootDirOpt(localProjects[1].Path))
	gitLocal.CreateBranch("A")
	gitLocal.CreateBranch("B")
	gitLocal.SetUpstream("B", "A")
	gitLocal.SetUpstream("A", "B")

	gitRemote := gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[localProjects[1].Name]))
	oldRemoteRev, _ := gitRemote.CurrentRevision()
	writeFile(t, fake.X, fake.Projects[localProjects[1].Name], "file1", "file1")
	remoteRev, _ := gitRemote.CurrentRevision()

	if err := project.UpdateUniverse(fake.X, false, false, false, false, true /*rebase-all*/, true /*run-hooks*/, true /*run-packages*/, project.DefaultHookTimeout, project.DefaultPackageTimeout); err != nil {
		t.Fatal(err)
	}
	projects, err := project.LocalProjects(fake.X, project.FastScan)
	if err != nil {
		t.Fatal(err)
	}
	states, err := project.GetProjectStates(fake.X, projects, false)
	if err != nil {
		t.Fatal(err)
	}
	state := states[localProjects[1].Key()]
	for _, b := range state.Branches {
		expectedRev := oldRemoteRev
		if b.Name == "" {
			expectedRev = remoteRev
		}
		if b.Revision != expectedRev {
			t.Fatalf("Branch %q should have rev %q, instead it has %q", b.Name, expectedRev, b.Revision)
		}
	}
}

// TestOldMetaDirIsMovedOnUpdate tests that old metadir os moved to new
// location on update and projects are updated properly
func TestOldMetaDirIsMovedOnUpdate(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()

	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	for i, p := range localProjects {
		oldPath := filepath.Join(p.Path, jiri.OldProjectMetaDir)
		newPath := filepath.Join(p.Path, jiri.ProjectMetaDir)

		// move new path to old path to replicate old structure
		if err := os.Rename(newPath, oldPath); err != nil {
			t.Fatal(err)
		}
		if i != 1 {
			writeReadme(t, fake.X, fake.Projects[p.Name], "new readme")
		}
	}
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	for i, p := range localProjects {
		newPath := filepath.Join(p.Path, jiri.ProjectMetaDir)
		if err := dirExists(newPath); err != nil {
			t.Fatalf("expected metadata to exist at path %q but none found", newPath)
		}
		// Check all projects are at latest
		if i != 1 {
			checkReadme(t, fake.X, p, "new readme")
		} else {
			checkReadme(t, fake.X, p, "initial readme")
		}
	}
}

// TestUpdateUniverseWithCache checks that UpdateUniverse can clone and pull
// from a cache.
func TestUpdateUniverseWithCache(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()

	// Create cache directory
	cacheDir, err := ioutil.TempDir("", "cache")
	if err != nil {
		t.Fatalf("TempDir() failed: %v", err)
	}
	if err := os.MkdirAll(cacheDir, os.FileMode(0700)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(cacheDir); err != nil {
			t.Fatalf("RemoveAll(%q) failed: %v", cacheDir, err)
		}
	}()
	fake.X.Cache = cacheDir

	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	for _, p := range localProjects {
		// Check that local clone was referenced from cache
		err := fileExists(p.Path + "/.git/objects/info/alternates")
		if p.HistoryDepth == 0 {
			if err != nil {
				t.Fatalf("expected %v to exist, but not found", p.Path+"/.git/objects/info/alternates")
			}
		} else if err == nil {
			t.Fatalf("expected %v to not exist, but found", p.Path+"/.git/objects/info/alternates")
		}
		checkReadme(t, fake.X, p, "initial readme")
		checkJiriRevFiles(t, fake.X, p)
	}

	// Commit to master branch of a project 1.
	writeReadme(t, fake.X, fake.Projects[localProjects[1].Name], "master commit")
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	checkReadme(t, fake.X, localProjects[1], "master commit")
	checkJiriRevFiles(t, fake.X, localProjects[1])

	// Check that cache was updated
	cacheDirPath, err := localProjects[1].CacheDirPath(fake.X)
	if err != nil {
		t.Fatal(err)
	}
	gCache := gitutil.New(fake.X, gitutil.RootDirOpt(cacheDirPath))
	cacheRev, err := gCache.CurrentRevision()
	if err != nil {
		t.Fatal(err)
	}
	gitLocal := gitutil.New(fake.X, gitutil.RootDirOpt(localProjects[1].Path))
	localRev, err := gitLocal.CurrentRevision()
	if err != nil {
		t.Fatal(err)
	}
	if cacheRev != localRev {
		t.Fatalf("Cache revision(%v) not equal to local revision(%v)", cacheRev, localRev)
	}
}

func TestProjectUpdateWhenNoUpdate(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	lc := project.LocalConfig{NoUpdate: true}
	project.WriteLocalConfig(fake.X, localProjects[1], lc)
	// Commit to master branch of a project 1.
	writeReadme(t, fake.X, fake.Projects[localProjects[1].Name], "master commit")
	gitRemote := gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[localProjects[1].Name]))
	remoteRev, _ := gitRemote.CurrentRevision()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	gitLocal := gitutil.New(fake.X, gitutil.RootDirOpt(localProjects[1].Path))
	localRev, _ := gitLocal.CurrentRevision()

	if remoteRev == localRev {
		t.Fatal("local project should not be updated")
	}
}

func TestRecursiveImport(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()

	manifest, err := fake.ReadRemoteManifest()
	if err != nil {
		t.Fatal(err)
	}

	// Remove last project from manifest
	lastProject := manifest.Projects[len(manifest.Projects)-1]
	manifest.Projects = manifest.Projects[:len(manifest.Projects)-1]
	remoteManifestStr := "remotemanifest"
	if err := fake.CreateRemoteProject(remoteManifestStr); err != nil {
		t.Fatal(err)
	}
	// Fix last projet rev
	lastPRev, _ := gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[lastProject.Name])).CurrentRevision()
	lastProject.Revision = lastPRev
	remoteManifest := &project.Manifest{
		Projects: []project.Project{lastProject, project.Project{
			Name:   remoteManifestStr,
			Path:   remoteManifestStr,
			Remote: fake.Projects[remoteManifestStr],
		}},
	}
	remoteManifestFile := filepath.Join(fake.Projects[remoteManifestStr], "manifest")
	if err := remoteManifest.ToFile(fake.X, remoteManifestFile); err != nil {
		t.Fatal(err)
	}
	commitFile(t, fake.X, fake.Projects[remoteManifestStr], "manifest", "1")
	rev, _ := gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[remoteManifestStr])).CurrentRevision()

	// unpin last project in next commit
	remoteManifest.Projects[0].Revision = ""
	if err := remoteManifest.ToFile(fake.X, remoteManifestFile); err != nil {
		t.Fatal(err)
	}
	commitFile(t, fake.X, fake.Projects[remoteManifestStr], "manifest", "2")
	writeFile(t, fake.X, fake.Projects[lastProject.Name], "file1", "file1")
	manifest.Imports = []project.Import{project.Import{
		Name:     remoteManifestStr,
		Remote:   fake.Projects[remoteManifestStr],
		Manifest: "manifest",
		Revision: rev,
	}}
	fake.WriteRemoteManifest(manifest)
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	// check all local projects
	for _, p := range localProjects {
		if err := dirExists(p.Path); err != nil {
			t.Fatalf("expected project to exist at path %q but none found", p.Path)
		}
		checkReadme(t, fake.X, p, "initial readme")
	}

	// check that remotemanifest is at correct revision
	remoteManifestPath := filepath.Join(fake.X.Root, remoteManifestStr)
	if err := dirExists(remoteManifestPath); err != nil {
		t.Fatalf("expected project to exist at path %q but none found", remoteManifestPath)
	}
	currentRev, _ := gitutil.New(fake.X, gitutil.RootDirOpt(remoteManifestPath)).CurrentRevision()
	if currentRev != rev {
		t.Fatalf("For project remotemanifest expected rev to be %q got %q", rev, currentRev)
	}
	// check last project revision
	currentRev, _ = gitutil.New(fake.X, gitutil.RootDirOpt(filepath.Join(fake.X.Root, lastProject.Path))).CurrentRevision()
	if currentRev != lastPRev {
		t.Fatalf("For project %q expected rev to be %q got %q", lastProject.Name, lastPRev, currentRev)
	}

	//unpin import
	manifest.Imports[0].Revision = ""
	fake.WriteRemoteManifest(manifest)
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	//check that projects advances
	currentRev, _ = gitutil.New(fake.X, gitutil.RootDirOpt(remoteManifestPath)).CurrentRevision()
	if currentRev == rev {
		t.Fatalf("For project remotemanifest expected rev to NOT be %q", rev)
	}
	// check last project revision
	currentRev, _ = gitutil.New(fake.X, gitutil.RootDirOpt(filepath.Join(fake.X.Root, lastProject.Path))).CurrentRevision()
	if currentRev == lastPRev {
		t.Fatalf("For project %q expected rev to NOT be %q", lastProject.Name, lastPRev)
	}
}

func TestLoadManifestFileRecursiveImport(t *testing.T) {
	_, fake, cleanup := setupUniverse(t)
	defer cleanup()

	manifest, err := fake.ReadRemoteManifest()
	if err != nil {
		t.Fatal(err)
	}

	// Remove last project from manifest
	lastProject := manifest.Projects[len(manifest.Projects)-1]
	manifest.Projects = manifest.Projects[:len(manifest.Projects)-1]
	remoteManifestStr := "remotemanifest"
	if err := fake.CreateRemoteProject(remoteManifestStr); err != nil {
		t.Fatal(err)
	}

	remoteManifest := &project.Manifest{
		Projects: []project.Project{lastProject, project.Project{
			Name:   remoteManifestStr,
			Path:   remoteManifestStr,
			Remote: fake.Projects[remoteManifestStr],
		}},
	}
	remoteManifestFile := filepath.Join(fake.Projects[remoteManifestStr], "manifest")
	if err := remoteManifest.ToFile(fake.X, remoteManifestFile); err != nil {
		t.Fatal(err)
	}
	commitFile(t, fake.X, fake.Projects[remoteManifestStr], "manifest", "1")
	rev, _ := gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[remoteManifestStr])).CurrentRevision()

	manifest.Imports = []project.Import{project.Import{
		Name:     remoteManifestStr,
		Remote:   fake.Projects[remoteManifestStr],
		Manifest: "manifest",
		Revision: rev,
	}}
	fake.WriteRemoteManifest(manifest)
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	// Write arbitrary revision
	manifest.Imports[0].Revision = "AB"
	fake.WriteRemoteManifest(manifest)

	// local fetch on manifest project
	gitLocal := gitutil.New(fake.X, gitutil.RootDirOpt(filepath.Join(fake.X.Root, jiritest.ManifestProjectPath)))
	if err := gitLocal.Fetch("origin"); err != nil {
		t.Fatal(err)
	}
	localProjects, err := project.LocalProjects(fake.X, project.FastScan)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := project.LoadManifestFile(fake.X, fake.X.JiriManifestFile(), localProjects, false); err != nil {
		t.Fatal(err)
	}
}

func TestRecursiveImportWithLocalImport(t *testing.T) {
	_, fake, cleanup := setupUniverse(t)
	defer cleanup()

	manifest, err := fake.ReadRemoteManifest()
	if err != nil {
		t.Fatal(err)
	}

	// Remove last project from manifest
	lastProject := manifest.Projects[len(manifest.Projects)-1]
	manifest.Projects = manifest.Projects[:len(manifest.Projects)-1]
	remoteManifestStr := "remotemanifest"
	if err := fake.CreateRemoteProject(remoteManifestStr); err != nil {
		t.Fatal(err)
	}
	// Fix last project rev
	lastPRev, _ := gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[lastProject.Name])).CurrentRevision()
	lastProject.Revision = lastPRev
	remoteManifest := &project.Manifest{
		Projects: []project.Project{lastProject, project.Project{
			Name:   remoteManifestStr,
			Path:   remoteManifestStr,
			Remote: fake.Projects[remoteManifestStr],
		}},
	}
	remoteManifestFile := filepath.Join(fake.Projects[remoteManifestStr], "manifest")
	if err := remoteManifest.ToFile(fake.X, remoteManifestFile); err != nil {
		t.Fatal(err)
	}
	commitFile(t, fake.X, fake.Projects[remoteManifestStr], "manifest", "1")
	rev, _ := gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[remoteManifestStr])).CurrentRevision()
	manifest.Imports = []project.Import{project.Import{
		Name:     remoteManifestStr,
		Remote:   fake.Projects[remoteManifestStr],
		Manifest: "manifest",
		Revision: rev,
	}}

	// unpin last project in next commit
	remoteManifest.Projects[0].Revision = ""
	if err := remoteManifest.ToFile(fake.X, remoteManifestFile); err != nil {
		t.Fatal(err)
	}
	commitFile(t, fake.X, fake.Projects[remoteManifestStr], "manifest", "2")
	// get latest revision
	rev, _ = gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[remoteManifestStr])).CurrentRevision()
	writeFile(t, fake.X, fake.Projects[lastProject.Name], "file1", "file1")
	// Get latest last project revision
	lastPRev, _ = gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[lastProject.Name])).CurrentRevision()
	fake.WriteRemoteManifest(manifest)
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	// make local change in top level manifest and unpin remote manifest
	manifest.Imports[0].Revision = ""
	if err := manifest.ToFile(fake.X, filepath.Join(fake.X.Root, jiritest.ManifestProjectPath, jiritest.ManifestFileName)); err != nil {
		t.Fatal(err)
	}
	if err := project.UpdateUniverse(fake.X, false, true /* localManifest */, false, false, false, false, false, project.DefaultHookTimeout, project.DefaultPackageTimeout); err != nil {
		t.Fatal(err)
	}

	remoteManifestPath := filepath.Join(fake.X.Root, remoteManifestStr)
	currentRev, _ := gitutil.New(fake.X, gitutil.RootDirOpt(remoteManifestPath)).CurrentRevision()
	if currentRev != rev {
		t.Fatalf("For project remotemanifest expected rev to be %q got %q", rev, currentRev)
	}
	// check last project revision
	currentRev, _ = gitutil.New(fake.X, gitutil.RootDirOpt(filepath.Join(fake.X.Root, lastProject.Path))).CurrentRevision()
	if currentRev != lastPRev {
		t.Fatalf("For project %q expected rev to be %q got %q", lastProject.Name, lastPRev, currentRev)
	}
}

func TestRecursiveImportWhenOriginalManifestIsImportedAgain(t *testing.T) {
	_, fake, cleanup := setupUniverse(t)
	defer cleanup()

	manifest, err := fake.ReadRemoteManifest()
	if err != nil {
		t.Fatal(err)
	}

	// Remove last project from manifest and add it to local import
	lastProject := manifest.Projects[len(manifest.Projects)-1]
	manifest.Projects = manifest.Projects[:len(manifest.Projects)-1]
	manifest.LocalImports = []project.LocalImport{project.LocalImport{
		File: "localmanifest",
	}}
	localManifest := project.Manifest{
		Projects: []project.Project{lastProject},
	}
	localManifestFile := filepath.Join(fake.Projects[jiritest.ManifestProjectName], "localmanifest")
	if err := localManifest.ToFile(fake.X, localManifestFile); err != nil {
		t.Fatal(err)
	}
	commitFile(t, fake.X, fake.Projects[jiritest.ManifestProjectName], "localmanifest", "1")

	remoteManifestStr := "remotemanifest"
	if err := fake.CreateRemoteProject(remoteManifestStr); err != nil {
		t.Fatal(err)
	}
	manifest.Imports = []project.Import{project.Import{
		Name:     remoteManifestStr,
		Remote:   fake.Projects[remoteManifestStr],
		Manifest: "manifest",
	}}
	fake.WriteRemoteManifest(manifest)

	// Fix last project rev
	remoteManifest := &project.Manifest{
		Projects: []project.Project{project.Project{
			Name:   remoteManifestStr,
			Path:   remoteManifestStr,
			Remote: fake.Projects[remoteManifestStr],
		}},
		Imports: []project.Import{project.Import{
			Name:     jiritest.ManifestProjectName,
			Remote:   fake.Projects[jiritest.ManifestProjectName],
			Manifest: "localmanifest",
		}},
	}
	remoteManifestFile := filepath.Join(fake.Projects[remoteManifestStr], "manifest")
	if err := remoteManifest.ToFile(fake.X, remoteManifestFile); err != nil {
		t.Fatal(err)
	}
	commitFile(t, fake.X, fake.Projects[remoteManifestStr], "manifest", "1")

	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	//pin last project and don't commit
	lastPRev, _ := gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[lastProject.Name])).CurrentRevision()
	localManifest.Projects[0].Revision = lastPRev
	if err := localManifest.ToFile(fake.X, filepath.Join(fake.X.Root, jiritest.ManifestProjectPath, "localmanifest")); err != nil {
		t.Fatal(err)
	}

	// Add new commit to last project
	writeFile(t, fake.X, fake.Projects[lastProject.Name], "file1", "file1")
	if err := project.UpdateUniverse(fake.X, false, true /* localManifest */, false, false, false, false, false, project.DefaultHookTimeout, project.DefaultPackageTimeout); err != nil {
		t.Fatal(err)
	}
	// check last project revision
	currentRev, _ := gitutil.New(fake.X, gitutil.RootDirOpt(filepath.Join(fake.X.Root, lastProject.Path))).CurrentRevision()
	if currentRev != lastPRev {
		t.Fatalf("For project %q expected rev to be %q got %q", lastProject.Name, lastPRev, currentRev)
	}
}

func TestProjectUpdateWhenIgnore(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	lc := project.LocalConfig{Ignore: true}
	project.WriteLocalConfig(fake.X, localProjects[1], lc)
	// Commit to master branch of a project 1.
	writeReadme(t, fake.X, fake.Projects[localProjects[1].Name], "master commit")
	gitRemote := gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[localProjects[1].Name]))
	remoteRev, _ := gitRemote.CurrentRevision()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	gitLocal := gitutil.New(fake.X, gitutil.RootDirOpt(localProjects[1].Path))
	localRev, _ := gitLocal.CurrentRevision()

	if remoteRev == localRev {
		t.Fatal("local project should not be updated")
	}
}

func TestLocalProjectWithConfig(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	project.WriteUpdateHistorySnapshot(fake.X, "", nil, nil, false)

	lc := project.LocalConfig{Ignore: true}
	project.WriteLocalConfig(fake.X, localProjects[1], lc)
	scanModes := []project.ScanMode{project.FullScan, project.FastScan}
	for _, scanMode := range scanModes {
		newLocalProjects, err := project.LocalProjects(fake.X, scanMode)
		if err != nil {
			t.Fatal(err)
		}
		for k, p := range newLocalProjects {
			expectedIgnore := k == localProjects[1].Key()
			if p.LocalConfig.Ignore != expectedIgnore {
				t.Errorf("local config ignore: got %t, want %t", p.LocalConfig.Ignore, expectedIgnore)
			}

			if p.LocalConfig.NoUpdate != false {
				t.Errorf("local config no-update: got %t, want %t", p.LocalConfig.NoUpdate, false)
			}

			if p.LocalConfig.NoRebase != false {
				t.Errorf("local config no-rebase: got %t, want %t", p.LocalConfig.NoUpdate, false)
			}
		}
	}
}

func TestProjectUpdateWhenNoRebase(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	lc := project.LocalConfig{NoRebase: true}
	project.WriteLocalConfig(fake.X, localProjects[1], lc)
	// Commit to master branch of a project 1.
	writeReadme(t, fake.X, fake.Projects[localProjects[1].Name], "master commit")
	gitRemote := gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[localProjects[1].Name]))
	remoteRev, _ := gitRemote.CurrentRevision()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	gitLocal := gitutil.New(fake.X, gitutil.RootDirOpt(localProjects[1].Path))
	localRev, _ := gitLocal.CurrentRevision()

	if remoteRev != localRev {
		t.Fatal("local project should be updated")
	}
}

func TestBranchUpdateWhenNoRebase(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()

	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	gitLocal := gitutil.New(fake.X, gitutil.RootDirOpt(localProjects[1].Path))
	gitLocal.CheckoutBranch("master")

	lc := project.LocalConfig{NoRebase: true}
	project.WriteLocalConfig(fake.X, localProjects[1], lc)
	// Commit to master branch of a project 1.
	writeReadme(t, fake.X, fake.Projects[localProjects[1].Name], "master commit")
	gitRemote := gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[localProjects[1].Name]))
	remoteRev, _ := gitRemote.CurrentRevision()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	localRev, _ := gitLocal.CurrentRevision()

	if remoteRev == localRev {
		t.Fatal("local branch master should not be updated")
	}
}

// TestHookLoadSimple tests that manifest is loaded correctly
// with correct project path in hook
func TestHookLoadSimple(t *testing.T) {
	p, fake, cleanup := setupUniverse(t)
	defer cleanup()
	err := fake.AddHook(project.Hook{Name: "hook1",
		Action:      "action.sh",
		ProjectName: p[0].Name})

	if err != nil {
		t.Fatal(err)
	}
	err = fake.UpdateUniverse(false)
	if err == nil {
		t.Fatal("run hook should throw error as there is no action.sh script")
	}
}

// TestRunHookFlag tests that hook is not executed when flag is false
func TestRunHookFlag(t *testing.T) {
	p, fake, cleanup := setupUniverse(t)
	defer cleanup()
	err := fake.AddHook(project.Hook{Name: "hook1",
		Action:      "action.sh",
		ProjectName: p[0].Name})

	if err != nil {
		t.Fatal(err)
	}
	if err := project.UpdateUniverse(fake.X, false, false, true /*rebaseTracked*/, false, false, false /*run-hooks*/, false /*run-packages*/, project.DefaultHookTimeout, project.DefaultPackageTimeout); err != nil {
		t.Fatal(err)
	}
}

// TestHookLoadError tests that manifest load
// throws error for invalid hook
func TestHookLoadError(t *testing.T) {
	_, fake, cleanup := setupUniverse(t)
	defer cleanup()
	err := fake.AddHook(project.Hook{Name: "hook1",
		Action:      "action",
		ProjectName: "non-existant"})

	if err != nil {
		t.Fatal(err)
	}
	err = fake.UpdateUniverse(false)
	if err == nil {
		t.Fatal("Update universe should throw error for the hook")
	}
	if !strings.Contains(err.Error(), "invalid hook") {
		t.Fatal(err)
	}
}

// TestUpdateUniverseWithRevision checks that UpdateUniverse will pull remote
// projects at the specified revision.
func TestUpdateUniverseWithRevision(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()

	// Set project 1's revision in the manifest to the current revision.
	g := gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[localProjects[1].Name]))
	rev, err := g.CurrentRevision()
	if err != nil {
		t.Fatal(err)
	}
	m, err := fake.ReadRemoteManifest()
	if err != nil {
		t.Fatal(err)
	}
	projects := []project.Project{}
	for _, p := range m.Projects {
		if p.Name == localProjects[1].Name {
			p.Revision = rev
		}
		projects = append(projects, p)
	}
	m.Projects = projects
	if err := fake.WriteRemoteManifest(m); err != nil {
		t.Fatal(err)
	}
	// Update README in all projects.
	for _, remoteProjectDir := range fake.Projects {
		writeReadme(t, fake.X, remoteProjectDir, "new revision")
	}
	// Check that calling UpdateUniverse() updates all projects except for
	// project 1.
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	for i, p := range localProjects {
		if i == 1 {
			checkReadme(t, fake.X, p, "initial readme")
		} else {
			checkReadme(t, fake.X, p, "new revision")
		}
	}
}

// TestUpdateUniverseWithBadRevision checks that UpdateUniverse
// will not leave bad state behind.
//func TestUpdateUniverseWithBadRevision(t *testing.T) {
//	localProjects, fake, cleanup := setupUniverse(t)
//	defer cleanup()
//
//	m, err := fake.ReadRemoteManifest()
//	if err != nil {
//		t.Fatal(err)
//	}
//	projects := []project.Project{}
//	for _, p := range m.Projects {
//		if p.Name == localProjects[1].Name {
//			p.Revision = "badrev"
//		}
//		projects = append(projects, p)
//	}
//	m.Projects = projects
//	if err := fake.WriteRemoteManifest(m); err != nil {
//		t.Fatal(err)
//	}
//
//	if err := fake.UpdateUniverse(false); err == nil {
//		t.Fatal("should have thrown error")
//	}
//
//	if err := dirExists(localProjects[1].Path); err == nil {
//		t.Fatalf("expected project %q at path %q not to exist but it did", localProjects[1].Name, localProjects[1].Path)
//	}
//
//}

func commitChanges(t *testing.T, jirix *jiri.X, dir string) {
	scm := gitutil.New(jirix, gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"), gitutil.RootDirOpt(dir))
	if err := scm.AddUpdatedFiles(); err != nil {
		t.Fatal(err)
	}
	if err := scm.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestSubDirToNestedProj checks that UpdateUniverse will correctly update when
// nested folder is converted to nested project
func TestSubDirToNestedProj(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()

	folderName := "nested_folder"
	nestedFolderPath := filepath.Join(fake.Projects[localProjects[1].Name], folderName)
	os.MkdirAll(nestedFolderPath, os.FileMode(0755))
	writeReadme(t, fake.X, nestedFolderPath, "nested folder")

	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(nestedFolderPath)
	commitChanges(t, fake.X, fake.Projects[localProjects[1].Name])

	// Create nested project
	if err := fake.CreateRemoteProject(folderName); err != nil {
		t.Fatal(err)
	}
	writeReadme(t, fake.X, fake.Projects[folderName], "nested folder")
	p := project.Project{
		Name:   folderName,
		Path:   filepath.Join(localProjects[1].Path, folderName),
		Remote: fake.Projects[folderName],
	}
	if err := fake.AddProject(p); err != nil {
		t.Fatal(err)
	}

	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	checkReadme(t, fake.X, p, "nested folder")
}

// TestMoveNestedProjects checks that UpdateUniverse will correctly move nested projects
func TestMoveNestedProjects(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()

	folderName := "nested_proj"
	// Create nested project
	if err := fake.CreateRemoteProject(folderName); err != nil {
		t.Fatal(err)
	}
	writeReadme(t, fake.X, fake.Projects[folderName], "nested folder")
	p := project.Project{
		Name:   folderName,
		Path:   filepath.Join(localProjects[1].Path, folderName),
		Remote: fake.Projects[folderName],
	}
	if err := fake.AddProject(p); err != nil {
		t.Fatal(err)
	}

	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	oldProjectPath := localProjects[1].Path
	localProjects[1].Path = filepath.Join(fake.X.Root, "new-project-path")
	p.Path = filepath.Join(localProjects[1].Path, folderName)
	m, err := fake.ReadRemoteManifest()
	if err != nil {
		t.Fatal(err)
	}
	projects := []project.Project{}
	for _, proj := range m.Projects {
		if proj.Name == localProjects[1].Name {
			proj.Path = localProjects[1].Path
		}
		if proj.Name == p.Name {
			proj.Path = p.Path
		}
		projects = append(projects, proj)
	}
	m.Projects = projects
	if err := fake.WriteRemoteManifest(m); err != nil {
		t.Fatal(err)
	}

	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	checkReadme(t, fake.X, localProjects[1], "initial readme")
	checkReadme(t, fake.X, p, "nested folder")
	if err := dirExists(oldProjectPath); err == nil {
		t.Fatalf("expected project %q at path %q not to exist but it did", localProjects[1].Name, oldProjectPath)
	}
}

// TestUpdateUniverseWithUncommitted checks that uncommitted files are not droped
// by UpdateUniverse(). This ensures that the "git reset --hard" mechanism used
// for pointing the master branch to a fixed revision does not lose work in
// progress.
func TestUpdateUniverseWithUncommitted(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	// Create an uncommitted file in project 1.
	file, perm, want := filepath.Join(localProjects[1].Path, "uncommitted_file"), os.FileMode(0644), []byte("uncommitted work")
	if err := ioutil.WriteFile(file, want, perm); err != nil {
		t.Fatalf("WriteFile(%v, %v) failed: %v", file, err, perm)
	}
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	got, err := ioutil.ReadFile(file)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if bytes.Compare(got, want) != 0 {
		t.Fatalf("unexpected content %v:\ngot\n%s\nwant\n%s\n", localProjects[1], got, want)
	}
}

// TestUpdateUniverseMovedProject checks that UpdateUniverse can move a
// project.
func TestUpdateUniverseMovedProject(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	// Update the local path at which project 1 is located.
	m, err := fake.ReadRemoteManifest()
	if err != nil {
		t.Fatal(err)
	}
	oldProjectPath := localProjects[1].Path
	localProjects[1].Path = filepath.Join(fake.X.Root, "new-project-path")
	projects := []project.Project{}
	for _, p := range m.Projects {
		if p.Name == localProjects[1].Name {
			p.Path = localProjects[1].Path
		}
		projects = append(projects, p)
	}
	m.Projects = projects
	if err := fake.WriteRemoteManifest(m); err != nil {
		t.Fatal(err)
	}
	// Check that UpdateUniverse() moves the local copy of the project 1.
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	if err := dirExists(oldProjectPath); err == nil {
		t.Fatalf("expected project %q at path %q not to exist but it did", localProjects[1].Name, oldProjectPath)
	}
	if err := dirExists(localProjects[2].Path); err != nil {
		t.Fatalf("expected project %q at path %q to exist but it did not", localProjects[1].Name, localProjects[1].Path)
	}
	checkReadme(t, fake.X, localProjects[1], "initial readme")
}

// TestUpdateUniverseChangeRemote checks that UpdateUniverse can change remote
// of a project.
func TestUpdateUniverseChangeRemote(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	changedRemoteDir := fake.Projects[localProjects[1].Name] + "-remote-changed"
	if err := os.Rename(fake.Projects[localProjects[1].Name], changedRemoteDir); err != nil {
		t.Fatal(err)
	}

	writeReadme(t, fake.X, changedRemoteDir, "new commit")

	// Update the local path at which project 1 is located.
	m, err := fake.ReadRemoteManifest()
	if err != nil {
		t.Fatal(err)
	}
	projects := []project.Project{}
	for _, p := range m.Projects {
		if p.Name == localProjects[1].Name {
			p.Remote = changedRemoteDir
		}
		projects = append(projects, p)
	}
	m.Projects = projects
	if err := fake.WriteRemoteManifest(m); err != nil {
		t.Fatal(err)
	}
	// Check that UpdateUniverse() moves the local copy of the project 1.
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	checkReadme(t, fake.X, localProjects[1], "new commit")
}

func TestIgnoredProjectsNotMoved(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	// Update the local path at which project 1 is located.
	m, err := fake.ReadRemoteManifest()
	if err != nil {
		t.Fatal(err)
	}
	lc := project.LocalConfig{Ignore: true}
	project.WriteLocalConfig(fake.X, localProjects[1], lc)
	oldProjectPath := localProjects[1].Path
	localProjects[1].Path = filepath.Join(fake.X.Root, "new-project-path")
	projects := []project.Project{}
	for _, p := range m.Projects {
		if p.Name == localProjects[1].Name {
			p.Path = localProjects[1].Path
		}
		projects = append(projects, p)
	}
	m.Projects = projects
	if err := fake.WriteRemoteManifest(m); err != nil {
		t.Fatal(err)
	}

	// Check that UpdateUniverse() does not move the local copy of the project 1.
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	if err := dirExists(oldProjectPath); err != nil {
		t.Fatalf("expected project %q at path %q to exist but it did not: %s", localProjects[1].Name, oldProjectPath, err)
	}
	if err := dirExists(localProjects[2].Path); err != nil {
		t.Fatalf("expected project %q at path %q to not exist but it did", localProjects[1].Name, localProjects[1].Path)
	}
}

// TestUpdateUniverseRenamedProject checks that UpdateUniverse can update
// renamed project.
func TestUpdateUniverseRenamedProject(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	m, err := fake.ReadRemoteManifest()
	if err != nil {
		t.Fatal(err)
	}
	oldProjectName := localProjects[1].Name
	localProjects[1].Name = localProjects[1].Name + "new"
	projects := []project.Project{}
	for _, p := range m.Projects {
		if p.Name == oldProjectName {
			p.Name = localProjects[1].Name
		}
		projects = append(projects, p)
	}
	m.Projects = projects
	if err := fake.WriteRemoteManifest(m); err != nil {
		t.Fatal(err)
	}

	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	newLocalProjects, err := project.LocalProjects(fake.X, project.FullScan)
	if err != nil {
		t.Fatal(err)
	}
	projectFound := false
	for _, p := range newLocalProjects {
		if p.Name == localProjects[1].Name {
			projectFound = true
		}
	}
	if !projectFound {
		t.Fatalf("Project with updated name(%v) not found", localProjects[1].Name)
	}
}

// testUpdateUniverseDeletedProject checks that UpdateUniverse will delete a
// project if gc=true.
func testUpdateUniverseDeletedProject(t *testing.T, testDirtyProjectDelete, testProjectWithBranch bool) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	// Delete project 1.
	m, err := fake.ReadRemoteManifest()
	if err != nil {
		t.Fatal(err)
	}
	projects := []project.Project{}
	if testDirtyProjectDelete {
		writeUncommitedFile(t, fake.X, localProjects[4].Path, "extra", "")
	} else if testProjectWithBranch {
		// Create and checkout master.
		git := gitutil.New(fake.X, gitutil.RootDirOpt(localProjects[4].Path))
		if err := git.CreateAndCheckoutBranch("master"); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range m.Projects {
		skip := false
		for i := 1; i <= 5; i++ {
			if p.Name == localProjects[i].Name {
				skip = true
			}
		}
		if skip {
			continue
		}
		projects = append(projects, p)
	}
	m.Projects = projects
	if err := fake.WriteRemoteManifest(m); err != nil {
		t.Fatal(err)
	}
	// Check that UpdateUniverse() with gc=false does not delete the local copy
	// of the project.
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		if err := dirExists(localProjects[i].Path); err != nil {
			t.Fatalf("expected project %q at path %q to exist but it did not", localProjects[i].Name, localProjects[i].Path)
		}
		checkReadme(t, fake.X, localProjects[i], "initial readme")
	}
	// Check that UpdateUniverse() with gc=true does delete the local copy of
	// the project.
	if err := fake.UpdateUniverse(true); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		err := dirExists(localProjects[i].Path)
		if (testProjectWithBranch || testDirtyProjectDelete) && i >= 2 && i <= 4 {
			if err != nil {
				t.Fatalf("expected project %q at path %q to exist but it did not", localProjects[i].Name, localProjects[i].Path)
			}
		} else if err == nil {
			t.Fatalf("expected project %q at path %q not to exist but it did", localProjects[i].Name, localProjects[i].Path)
		}
	}
}

func TestUpdateUniverseDeletedProject(t *testing.T) {
	testUpdateUniverseDeletedProject(t, false, false)
	testUpdateUniverseDeletedProject(t, true, false)
	testUpdateUniverseDeletedProject(t, false, true)
}

func TestIgnoredProjectsNotDeleted(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	// Delete project 1.
	m, err := fake.ReadRemoteManifest()
	if err != nil {
		t.Fatal(err)
	}
	projects := []project.Project{}
	for _, p := range m.Projects {
		if p.Name == localProjects[1].Name {
			continue
		}
		projects = append(projects, p)
	}
	m.Projects = projects
	if err := fake.WriteRemoteManifest(m); err != nil {
		t.Fatal(err)
	}
	lc := project.LocalConfig{Ignore: true}
	project.WriteLocalConfig(fake.X, localProjects[1], lc)
	if err := fake.UpdateUniverse(true); err != nil {
		t.Fatal(err)
	}
	if err := dirExists(localProjects[1].Path); err != nil {
		t.Fatalf("expected project %q at path %q to exist but it did not: %s", localProjects[1].Name, localProjects[1].Path, err)
	}
}

// TestUpdateUniverseNewProjectSamePath checks that UpdateUniverse can handle a
// new project with the same path as a deleted project, but a different path.
func TestUpdateUniverseNewProjectSamePath(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	// Delete a project 1 and create a new one with a different name but the
	// same path.
	m, err := fake.ReadRemoteManifest()
	if err != nil {
		t.Fatal(err)
	}
	newProjectName := "new-project-name"
	projects := []project.Project{}
	for _, p := range m.Projects {
		if p.Path == localProjects[1].Path {
			p.Name = newProjectName
		}
		projects = append(projects, p)
	}
	localProjects[1].Name = newProjectName
	m.Projects = projects
	if err := fake.WriteRemoteManifest(m); err != nil {
		t.Fatal(err)
	}
	// Check that UpdateUniverse() does not fail.
	if err := fake.UpdateUniverse(true); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateUniverseRemoteBranch checks that UpdateUniverse can pull from a
// non-master remote branch.
func TestUpdateUniverseRemoteBranch(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	// Commit to master branch of a project 1.
	writeReadme(t, fake.X, fake.Projects[localProjects[1].Name], "master commit")
	// Create and checkout a new branch of project 1 and make a new commit.
	git := gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[localProjects[1].Name]))
	if err := git.CreateAndCheckoutBranch("non-master"); err != nil {
		t.Fatal(err)
	}
	writeReadme(t, fake.X, fake.Projects[localProjects[1].Name], "non-master commit")
	// Point the manifest to the new non-master branch.
	m, err := fake.ReadRemoteManifest()
	if err != nil {
		t.Fatal(err)
	}
	projects := []project.Project{}
	for _, p := range m.Projects {
		if p.Name == localProjects[1].Name {
			p.RemoteBranch = "non-master"
		}
		projects = append(projects, p)
	}
	m.Projects = projects
	if err := fake.WriteRemoteManifest(m); err != nil {
		t.Fatal(err)
	}
	// Check that UpdateUniverse pulls the commit from the non-master branch.
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}
	checkReadme(t, fake.X, localProjects[1], "non-master commit")
}

// TestUpdateWhenRemoteChangesRebased checks that UpdateUniverse can pull from a
// non-master remote branch if the local changes were rebased somewhere else(gerrit)
// before being pushed to remote
func TestUpdateWhenRemoteChangesRebased(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	gitRemote := gitutil.New(fake.X, gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"), gitutil.RootDirOpt(fake.Projects[localProjects[1].Name]))
	if err := gitRemote.CreateAndCheckoutBranch("non-master"); err != nil {
		t.Fatal(err)
	}

	gitLocal := gitutil.New(fake.X, gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"), gitutil.RootDirOpt(localProjects[1].Path))
	if err := gitLocal.Fetch("origin", gitutil.PruneOpt(true)); err != nil {
		t.Fatal(err)
	}

	// checkout branch in local repo
	if err := gitLocal.CheckoutBranch("non-master"); err != nil {
		t.Fatal(err)
	}

	// Create commits in remote repo
	writeReadme(t, fake.X, fake.Projects[localProjects[1].Name], "non-master commit")
	writeFile(t, fake.X, fake.Projects[localProjects[1].Name], "file1", "file1")
	file1CommitRev, _ := gitRemote.CurrentRevision()
	writeFile(t, fake.X, fake.Projects[localProjects[1].Name], "file2", "file2")
	file2CommitRev, _ := gitRemote.CurrentRevision()

	if err := gitLocal.Fetch("origin", gitutil.PruneOpt(true)); err != nil {
		t.Fatal(err)
	}

	// Cherry pick creation of file1, so that it acts like been rebased on remote repo
	// As there is a commit creating README on remote not in local repo
	if err := gitLocal.CherryPick(file1CommitRev); err != nil {
		t.Fatal(err)
	}

	if err := project.UpdateUniverse(fake.X, false, false, true /*rebaseTracked*/, false, false, true /*run-hooks*/, true /*run-packages*/, project.DefaultHookTimeout, project.DefaultPackageTimeout); err != nil {
		t.Fatal(err)
	}

	// It rebased properly and pulled latest changes
	localRev, _ := gitLocal.CurrentRevision()
	if file2CommitRev != localRev {
		t.Fatalf("Current commit is %v, it should be %v\n", localRev, file2CommitRev)
	}
}

func TestUpdateWhenConflictMerge(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	gitRemote := gitutil.New(fake.X, gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"), gitutil.RootDirOpt(fake.Projects[localProjects[1].Name]))
	if err := gitRemote.CreateAndCheckoutBranch("non-master"); err != nil {
		t.Fatal(err)
	}

	gitLocal := gitutil.New(fake.X, gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"), gitutil.RootDirOpt(localProjects[1].Path))
	if err := gitLocal.Fetch("origin", gitutil.PruneOpt(true)); err != nil {
		t.Fatal(err)
	}

	// checkout branch in local repo
	if err := gitLocal.CheckoutBranch("non-master"); err != nil {
		t.Fatal(err)
	}

	// Create commits in remote repo
	writeReadme(t, fake.X, fake.Projects[localProjects[1].Name], "non-master commit")
	writeFile(t, fake.X, fake.Projects[localProjects[1].Name], "file1", "file1")
	file1CommitRev, _ := gitRemote.CurrentRevision()
	writeFile(t, fake.X, fake.Projects[localProjects[1].Name], "file2", "file2")

	if err := gitLocal.Fetch("origin", gitutil.PruneOpt(true)); err != nil {
		t.Fatal(err)
	}

	// Cherry pick creation of file1, so that it acts like been rebased on remote repo
	// This would act like conflicting merge
	if err := gitLocal.CherryPick(file1CommitRev); err != nil {
		t.Fatal(err)
	}
	rev, _ := gitLocal.CurrentRevision()

	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	localRev, _ := gitLocal.CurrentRevision()
	if rev != localRev {
		t.Fatalf("Current commit is %v, it should be %v\n", localRev, rev)
	}
	checkJiriRevFiles(t, fake.X, localProjects[1])
}

func TestTagNotContainedInBranch(t *testing.T) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	gitRemote := gitutil.New(fake.X, gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"), gitutil.RootDirOpt(fake.Projects[localProjects[1].Name]))
	if err := gitRemote.CreateAndCheckoutBranch("non-master"); err != nil {
		t.Fatal(err)
	}

	// Create commits in remote repo
	writeReadme(t, fake.X, fake.Projects[localProjects[1].Name], "non-master commit")
	writeFile(t, fake.X, fake.Projects[localProjects[1].Name], "file1", "file1")
	file1CommitRev, _ := gitRemote.CurrentRevision()
	if err := gitRemote.CreateLightweightTag("testtag"); err != nil {
		t.Fatalf("Creating tag: %s", err)

	}
	if err := gitRemote.CheckoutBranch("master"); err != nil {
		t.Fatal(err)
	}
	if err := gitRemote.DeleteBranch("non-master", gitutil.ForceOpt(true)); err != nil {
		t.Fatal(err)
	}

	m, err := fake.ReadRemoteManifest()
	if err != nil {
		t.Fatal(err)
	}
	projects := []project.Project{}
	for _, p := range m.Projects {
		if p.Name == localProjects[1].Name {
			p.Revision = "testtag"
		}
		projects = append(projects, p)
	}
	m.Projects = projects
	if err := fake.WriteRemoteManifest(m); err != nil {
		t.Fatal(err)
	}

	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	gitLocal := gitutil.New(fake.X, gitutil.RootDirOpt(localProjects[1].Path))
	// It rebased properly and pulled latest changes
	localRev, _ := gitLocal.CurrentRevision()
	if file1CommitRev != localRev {
		t.Fatalf("Current commit is %v, it should be %v\n", localRev, file1CommitRev)
	}
}

// TestCheckoutSnapshotUrl tests checking out snapshot functionality from a url
func TestCheckoutSnapshotUrl(t *testing.T) {
	testCheckoutSnapshot(t, true)
}

// TestCheckoutSnapshotFileSystem tests checking out snapshot functionality from filesystem
func TestCheckoutSnapshotFileSystem(t *testing.T) {
	testCheckoutSnapshot(t, false)
}

func testCheckoutSnapshot(t *testing.T, testURL bool) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	var oldCommitRevs []string
	var latestCommitRevs []string

	for i, localProject := range localProjects {
		gitRemote := gitutil.New(fake.X, gitutil.RootDirOpt(fake.Projects[localProject.Name]))
		writeFile(t, fake.X, fake.Projects[localProject.Name], "file1"+strconv.Itoa(i), "file1"+strconv.Itoa(i))
		file1CommitRev, _ := gitRemote.CurrentRevision()
		oldCommitRevs = append(oldCommitRevs, file1CommitRev)
		writeFile(t, fake.X, fake.Projects[localProject.Name], "file2"+strconv.Itoa(i), "file2"+strconv.Itoa(i))
		file2CommitRev, _ := gitRemote.CurrentRevision()
		latestCommitRevs = append(latestCommitRevs, file2CommitRev)
	}

	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	for i, localProject := range localProjects {
		gitLocal := gitutil.New(fake.X, gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"), gitutil.RootDirOpt(localProject.Path))
		rev, _ := gitLocal.CurrentRevision()
		if rev != latestCommitRevs[i] {
			t.Fatalf("Current commit for project %q is %v, it should be %v\n", localProject.Name, rev, latestCommitRevs[i])
		}

		// Test case when local repo in on a branch
		if i == 1 {
			if err := gitLocal.CheckoutBranch("master"); err != nil {
				t.Fatal(err)
			}
		}
	}
	dir, err := ioutil.TempDir("", "snap")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	manifest := &project.Manifest{Version: project.ManifestVersion}
	for _, localProject := range localProjects {
		manifest.Projects = append(manifest.Projects, localProject)
	}
	manifest.Projects[0].Revision = oldCommitRevs[0]
	manifest.Projects[1].Revision = oldCommitRevs[1]

	// Test case when snapshot specifies latest revision
	manifest.Projects[2].Revision = latestCommitRevs[2]

	manifest.Projects[3].Revision = oldCommitRevs[3]
	manifest.Projects[4].Revision = latestCommitRevs[4]
	manifest.Projects[5].Revision = oldCommitRevs[5]
	manifest.Projects[6].Revision = latestCommitRevs[6]
	snapshotFile := filepath.Join(dir, "snapshot")
	manifest.ToFile(fake.X, snapshotFile)
	if testURL {
		snapBytes, err := ioutil.ReadFile(snapshotFile)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "html/text")
			fmt.Fprintln(w, string(snapBytes[:]))
		}))
		defer server.Close()

		project.CheckoutSnapshot(fake.X, server.URL, false, true /*run-hooks*/, true /*run-packages*/, project.DefaultHookTimeout, project.DefaultPackageTimeout)
	} else {
		project.CheckoutSnapshot(fake.X, snapshotFile, false, true /*run-hooks*/, true /*run-packages*/, project.DefaultHookTimeout, project.DefaultPackageTimeout)
	}
	sort.Sort(project.ProjectsByPath(localProjects))
	for i, localProject := range localProjects {
		gitLocal := gitutil.New(fake.X, gitutil.RootDirOpt(localProject.Path))
		rev, _ := gitLocal.CurrentRevision()
		expectedRev := manifest.Projects[i].Revision
		if rev != expectedRev {
			t.Fatalf("Current commit for project %q is %v, it should be %v\n", localProject.Name, rev, expectedRev)
		}
	}
}

func testLocalBranchesAreUpdated(t *testing.T, shouldLocalBeOnABranch, rebaseAll bool) {
	localProjects, fake, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	gitRemote := gitutil.New(fake.X, gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"), gitutil.RootDirOpt(fake.Projects[localProjects[1].Name]))
	if err := gitRemote.CreateAndCheckoutBranch("non-master"); err != nil {
		t.Fatal(err)
	}

	// This will fetch non-master to local
	if err := fake.UpdateUniverse(false); err != nil {
		t.Fatal(err)
	}

	writeReadme(t, fake.X, fake.Projects[localProjects[1].Name], "non-master commit")

	if err := gitRemote.CheckoutBranch("master"); err != nil {
		t.Fatal(err)
	}
	writeReadme(t, fake.X, fake.Projects[localProjects[1].Name], "master commit")

	gitLocal := gitutil.New(fake.X, gitutil.UserNameOpt("John Doe"), gitutil.UserEmailOpt("john.doe@example.com"), gitutil.RootDirOpt(localProjects[1].Path))

	// This will create a local branch non-master
	if err := gitLocal.CheckoutBranch("non-master"); err != nil {
		t.Fatal(err)
	}

	// Go back to detached HEAD
	if !shouldLocalBeOnABranch {
		if err := gitLocal.CheckoutBranch("HEAD", gitutil.DetachOpt(true)); err != nil {
			t.Fatal(err)
		}
	}

	if err := project.UpdateUniverse(fake.X, false, false, false, false, rebaseAll, true /*run-hooks*/, true /*run-packages*/, project.DefaultHookTimeout, project.DefaultPackageTimeout); err != nil {
		t.Fatal(err)
	}

	if shouldLocalBeOnABranch && gitLocal.IsOnBranch() == false {
		t.Fatalf("local repo should be on the branch after update")
	} else if !shouldLocalBeOnABranch && gitLocal.IsOnBranch() == true {
		t.Fatalf("local repo should be on detached head after update")
	}

	projects, err := project.LocalProjects(fake.X, project.FastScan)
	if err != nil {
		t.Fatal(err)
	}

	states, err := project.GetProjectStates(fake.X, projects, false)
	if err != nil {
		t.Fatal(err)
	}

	state := states[localProjects[1].Key()]
	if shouldLocalBeOnABranch && state.CurrentBranch.Name != "non-master" {
		t.Fatalf("local repo should be on branch(non-master) it is on %q", state.CurrentBranch.Name)
	}

	if rebaseAll {
		for _, branch := range state.Branches {
			if branch.Tracking != nil {
				if branch.Revision != branch.Tracking.Revision {
					t.Fatalf("branch %q has different revision then remote branch %q", branch.Name, branch.Tracking.Name)
				}
			}
		}
	} else {
		for _, branch := range state.Branches {
			if branch.Tracking != nil {
				if branch.Name == state.CurrentBranch.Name {
					if branch.Revision != branch.Tracking.Revision {
						t.Fatalf("branch %q has different revision then remote branch %q", branch.Name, branch.Tracking.Name)
					}
				} else if branch.Revision == branch.Tracking.Revision {
					t.Fatalf("branch %q should have different revision then remote branch %q", branch.Name, branch.Tracking.Name)
				}
			}
		}
	}
}

// TestLocalBranchesAreUpdatedWhenOnHead test that all the local branches are
// updated on jiri update when local repo is on detached head
func TestLocalBranchesAreUpdatedWhenOnHead(t *testing.T) {
	testLocalBranchesAreUpdated(t, false, true)
}

// TestLocalBranchesAreUpdatedWhenOnBranch test that all the local branches are
// updated on jiri update when local repo is on a branch
func TestLocalBranchesAreUpdatedWhenOnBranch(t *testing.T) {
	testLocalBranchesAreUpdated(t, true, true)
}

// TestLocalBranchesNotUpdatedWhenOnHead test that all the local branches are not
// updated on jiri update when local repo is on detached head and rebaseAll is false
func TestLocalBranchesNotUpdatedWhenOnHead(t *testing.T) {
	testLocalBranchesAreUpdated(t, false, false)
}

// TestLocalBranchesAreUpdatedWhenOnBranch test that all the local branches are not
// updated on jiri update when local repo is on a branch and rebaseAll is false
func TestLocalBranchesNotUpdatedWhenOnBranch(t *testing.T) {
	testLocalBranchesAreUpdated(t, true, false)
}

func TestFileImportCycle(t *testing.T) {
	jirix, cleanup := jiritest.NewX(t)
	defer cleanup()

	// Set up the cycle .jiri_manifest -> A -> B -> A
	jiriManifest := project.Manifest{
		LocalImports: []project.LocalImport{
			{File: "A"},
		},
	}
	manifestA := project.Manifest{
		LocalImports: []project.LocalImport{
			{File: "B"},
		},
	}
	manifestB := project.Manifest{
		LocalImports: []project.LocalImport{
			{File: "A"},
		},
	}
	if err := jiriManifest.ToFile(jirix, jirix.JiriManifestFile()); err != nil {
		t.Fatal(err)
	}
	if err := manifestA.ToFile(jirix, filepath.Join(jirix.Root, "A")); err != nil {
		t.Fatal(err)
	}
	if err := manifestB.ToFile(jirix, filepath.Join(jirix.Root, "B")); err != nil {
		t.Fatal(err)
	}

	// The update should complain about the cycle.
	err := project.UpdateUniverse(jirix, false, false, false, false, false, true /*run-hooks*/, true /*run-packages*/, project.DefaultHookTimeout, project.DefaultPackageTimeout)
	if got, want := fmt.Sprint(err), "import cycle detected in local manifest files"; !strings.Contains(got, want) {
		t.Errorf("got error %v, want substr %v", got, want)
	}
}

func TestRemoteImportCycle(t *testing.T) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()

	// Set up two remote manifest projects, remote1 and remote1.
	if err := fake.CreateRemoteProject("remote1"); err != nil {
		t.Fatal(err)
	}
	if err := fake.CreateRemoteProject("remote2"); err != nil {
		t.Fatal(err)
	}
	remote1 := fake.Projects["remote1"]
	remote2 := fake.Projects["remote2"]

	fileA, fileB := filepath.Join(remote1, "A"), filepath.Join(remote2, "B")

	// Set up the cycle .jiri_manifest -> remote1+A -> remote2+B -> remote1+A
	jiriManifest := project.Manifest{
		Imports: []project.Import{
			{Manifest: "A", Name: "n1", Remote: remote1},
		},
	}
	manifestA := project.Manifest{
		Imports: []project.Import{
			{Manifest: "B", Name: "n2", Remote: remote2},
		},
	}
	manifestB := project.Manifest{
		Imports: []project.Import{
			{Manifest: "A", Name: "n3", Remote: remote1},
		},
	}
	if err := jiriManifest.ToFile(fake.X, fake.X.JiriManifestFile()); err != nil {
		t.Fatal(err)
	}
	if err := manifestA.ToFile(fake.X, fileA); err != nil {
		t.Fatal(err)
	}
	if err := manifestB.ToFile(fake.X, fileB); err != nil {
		t.Fatal(err)
	}
	commitFile(t, fake.X, remote1, fileA, "commit A")
	commitFile(t, fake.X, remote2, fileB, "commit B")

	// The update should complain about the cycle.
	err := project.UpdateUniverse(fake.X, false, false, false, false, false, true /*run-hooks*/, true /*run-packages*/, project.DefaultHookTimeout, project.DefaultPackageTimeout)
	if got, want := fmt.Sprint(err), "import cycle detected in remote manifest imports"; !strings.Contains(got, want) {
		t.Errorf("got error %v, want substr %v", got, want)
	}
}

func TestFileAndRemoteImportCycle(t *testing.T) {
	fake, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()

	// Set up two remote manifest projects, remote1 and remote2.
	// Set up two remote manifest projects, remote1 and remote1.
	if err := fake.CreateRemoteProject("remote1"); err != nil {
		t.Fatal(err)
	}
	if err := fake.CreateRemoteProject("remote2"); err != nil {
		t.Fatal(err)
	}
	remote1 := fake.Projects["remote1"]
	remote2 := fake.Projects["remote2"]
	fileA, fileD := filepath.Join(remote1, "A"), filepath.Join(remote1, "D")
	fileB, fileC := filepath.Join(remote2, "B"), filepath.Join(remote2, "C")

	// Set up the cycle .jiri_manifest -> remote1+A -> remote2+B -> C -> remote1+D -> A
	jiriManifest := project.Manifest{
		Imports: []project.Import{
			{Manifest: "A", Root: "r1", Name: "n1", Remote: remote1},
		},
	}
	manifestA := project.Manifest{
		Imports: []project.Import{
			{Manifest: "B", Root: "r2", Name: "n2", Remote: remote2},
		},
	}
	manifestB := project.Manifest{
		LocalImports: []project.LocalImport{
			{File: "C"},
		},
	}
	manifestC := project.Manifest{
		Imports: []project.Import{
			{Manifest: "D", Root: "r3", Name: "n3", Remote: remote1},
		},
	}
	manifestD := project.Manifest{
		LocalImports: []project.LocalImport{
			{File: "A"},
		},
	}
	if err := jiriManifest.ToFile(fake.X, fake.X.JiriManifestFile()); err != nil {
		t.Fatal(err)
	}
	if err := manifestA.ToFile(fake.X, fileA); err != nil {
		t.Fatal(err)
	}
	if err := manifestB.ToFile(fake.X, fileB); err != nil {
		t.Fatal(err)
	}
	if err := manifestC.ToFile(fake.X, fileC); err != nil {
		t.Fatal(err)
	}
	if err := manifestD.ToFile(fake.X, fileD); err != nil {
		t.Fatal(err)
	}
	commitFile(t, fake.X, remote1, fileA, "commit A")
	commitFile(t, fake.X, remote2, fileB, "commit B")
	commitFile(t, fake.X, remote2, fileC, "commit C")
	commitFile(t, fake.X, remote1, fileD, "commit D")

	// The update should complain about the cycle.
	err := project.UpdateUniverse(fake.X, false, false, false, false, false, true /*run-hooks*/, true /*run-packages*/, project.DefaultHookTimeout, project.DefaultPackageTimeout)
	if got, want := fmt.Sprint(err), "import cycle detected"; !strings.Contains(got, want) {
		t.Errorf("got error %v, want substr %v", got, want)
	}
}

func TestManifestToFromBytes(t *testing.T) {
	tests := []struct {
		Manifest project.Manifest
		XML      string
	}{
		{
			project.Manifest{},
			`<manifest>
</manifest>
`,
		},
		{
			project.Manifest{
				Imports: []project.Import{
					{
						Manifest:     "manifest1",
						Name:         "remoteimport1",
						Remote:       "remote1",
						Revision:     "HEAD",
						RemoteBranch: "master",
					},
					{
						Manifest:     "manifest2",
						Name:         "remoteimport2",
						Remote:       "remote2",
						Revision:     "HEAD",
						RemoteBranch: "branch2",
					},
					{
						Manifest:     "manifest3",
						Name:         "remoteimport3",
						Remote:       "remote3",
						Revision:     "rev3",
						RemoteBranch: "branch3",
					},
				},
				LocalImports: []project.LocalImport{
					{File: "fileimport"},
				},
				Projects: []project.Project{
					{
						Name:         "project1",
						Path:         "path1",
						Remote:       "remote1",
						RemoteBranch: "master",
						Revision:     "HEAD",
						GerritHost:   "https://test-review.googlesource.com",
						GitHooks:     "path/to/githooks",
					},
					{
						Name:         "project2",
						Path:         "path2",
						Remote:       "remote2",
						RemoteBranch: "branch2",
						Revision:     "rev2",
					},
				},
				Hooks: []project.Hook{
					{
						Name:        "testhook",
						ProjectName: "project1",
						Action:      "action.sh",
					},
				},
			},
			`<manifest>
  <imports>
    <import manifest="manifest1" name="remoteimport1" remote="remote1"/>
    <import manifest="manifest2" name="remoteimport2" remote="remote2" remotebranch="branch2"/>
    <import manifest="manifest3" name="remoteimport3" remote="remote3" revision="rev3" remotebranch="branch3"/>
    <localimport file="fileimport"/>
  </imports>
  <projects>
    <project name="project1" path="path1" remote="remote1" gerrithost="https://test-review.googlesource.com" githooks="path/to/githooks"/>
    <project name="project2" path="path2" remote="remote2" remotebranch="branch2" revision="rev2"/>
  </projects>
  <hooks>
    <hook name="testhook" action="action.sh" project="project1"/>
  </hooks>
</manifest>
`,
		},
	}
	for _, test := range tests {
		gotBytes, err := test.Manifest.ToBytes()
		if err != nil {
			t.Errorf("%+v ToBytes failed: %v", test.Manifest, err)
		}
		if got, want := string(gotBytes), test.XML; got != want {
			t.Errorf("%+v ToBytes GOT\n%v\nWANT\n%v", test.Manifest, got, want)
		}
		manifest, err := project.ManifestFromBytes([]byte(test.XML))
		if err != nil {
			t.Errorf("%+v FromBytes failed: %v", test.Manifest, err)
		}
		if got, want := manifest, &test.Manifest; !reflect.DeepEqual(got, want) {
			t.Errorf("%+v FromBytes GOT\n%#v\nWANT\n%#v", test.Manifest, got, want)
		}
	}
}

func TestProjectToFromFile(t *testing.T) {
	jirix, cleanup := jiritest.NewX(t)
	defer cleanup()

	tests := []struct {
		Project project.Project
		XML     string
	}{
		{
			// Default fields are dropped when marshaled, and added when unmarshaled.
			project.Project{
				Name:         "project1",
				Path:         filepath.Join(jirix.Root, "path1"),
				Remote:       "remote1",
				RemoteBranch: "master",
				Revision:     "HEAD",
			},
			`<project name="project1" path="path1" remote="remote1"/>
`,
		},
		{
			project.Project{
				Name:         "project2",
				Path:         filepath.Join(jirix.Root, "path2"),
				GitHooks:     filepath.Join(jirix.Root, "git-hooks"),
				Remote:       "remote2",
				RemoteBranch: "branch2",
				Revision:     "rev2",
			},
			`<project name="project2" path="path2" remote="remote2" remotebranch="branch2" revision="rev2" githooks="git-hooks"/>
`,
		},
	}
	for index, test := range tests {
		filename := filepath.Join(jirix.Root, fmt.Sprintf("test-%d", index))
		if err := test.Project.ToFile(jirix, filename); err != nil {
			t.Errorf("%+v ToFile failed: %v", test.Project, err)
		}
		gotBytes, err := ioutil.ReadFile(filename)
		if err != nil {
			t.Errorf("%+v ReadFile failed: %v", test.Project, err)
		}
		if got, want := string(gotBytes), test.XML; got != want {
			t.Errorf("%+v ToFile GOT\n%v\nWANT\n%v", test.Project, got, want)
		}
		project, err := project.ProjectFromFile(jirix, filename)
		if err != nil {
			t.Errorf("%+v FromFile failed: %v", test.Project, err)
		}
		if got, want := project, &test.Project; !reflect.DeepEqual(got, want) {
			t.Errorf("%+v FromFile got %#v, want %#v", test.Project, got, want)
		}
	}
}

func TestMarshalAndUnmarshalLockEntries(t *testing.T) {

	projectLock0 := project.ProjectLock{"https://dart.googlesource.com/web_socket_channel.git", "dart", "1.0.9"}
	pkgLock0 := project.PackageLock{"fuchsia/go/mac-amd64", "3c33b55c1a75b900536c91181805bb8668857341"}

	testProjectLocks0 := project.ProjectLocks{
		projectLock0.Key(): projectLock0,
	}
	testPkgLocks0 := project.PackageLocks{
		pkgLock0.Key(): pkgLock0,
	}

	jsonData, err := project.MarshalLockEntries(testProjectLocks0, testPkgLocks0)

	if err != nil {
		t.Errorf("marshalling lockfile failed due to error: %v", err)
	}

	projectLocks, pkgLocks, err := project.UnmarshalLockEntries(jsonData)
	if err != nil {
		t.Errorf("unmarshalling lockfile failed due to error: %v", err)
	}

	if !reflect.DeepEqual(projectLocks, testProjectLocks0) {
		t.Errorf("unmarshalled project locks do not match test data, expecting %v, got %v", testProjectLocks0, projectLocks)
	}

	if !reflect.DeepEqual(pkgLocks, testPkgLocks0) {
		t.Errorf("unmarshalled locks do not match test data, expecting %v, got %v", testPkgLocks0, pkgLocks)
	}

	jsonData = []byte(`
[
	{
		"repository_url": "https://dart.googlesource.com/web_socket_channel.git",
		"name": "dart",
		"revision": "1.0.9"
	},
	{
		"repository_url": "https://dart.googlesource.com/web_socket_channel.git",
		"name": "dart",
		"revision": "1.1.0"
	}
]`)

	if _, _, err := project.UnmarshalLockEntries(jsonData); err == nil {
		t.Errorf("unmarshalling lockfile with conflicting data should fail but it did not happen")
	} else {
		if !strings.Contains(err.Error(), "has more than 1") {
			t.Errorf("unmarshalling lockfile with conflicting data failed due to unrelated error: %v", err)
		}
	}

}

func TestGetPath(t *testing.T) {
	testPkgs := []project.Package{
		project.Package{Name: "test0", Version: "version", Path: "A/test0"},
		project.Package{Name: "test1/${platform}", Version: "version", Path: ""},
		project.Package{Name: "test2/${os}-${arch}", Version: "version", Path: ""},
		project.Package{Name: "test3/${platform=linux-armv6l}", Version: "version", Path: ""},
	}
	testResults := []string{
		"A/test0",
		"prebuilt/test1/",
		"prebuilt/test2/",
		"prebuilt",
	}

	for i, v := range testPkgs {
		defaultPath, err := v.GetPath()
		if err != nil {
			t.Errorf("TestGetPath failed due to error: %v", err)
		}
		if strings.HasSuffix(testResults[i], "/") {
			testResults[i] += cipd.CipdPlatform.String()
		}
		if testResults[i] != defaultPath {
			t.Errorf("expecting %q, got %q", testResults[i], defaultPath)
		}
	}
}
