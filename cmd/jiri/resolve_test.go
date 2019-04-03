// Copyright 2019 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"io/ioutil"
	"testing"

	"fuchsia.googlesource.com/jiri/jiritest"
	"fuchsia.googlesource.com/jiri/project"
)

func TestResolveProjects(t *testing.T) {
	_, fakeroot, cleanup := setupUniverse(t)
	defer cleanup()

	if err := fakeroot.UpdateUniverse(false); err != nil {
		t.Errorf("%v", err)
	}
	localProjects, err := project.LocalProjects(fakeroot.X, project.FastScan)
	projects, _, _, err := project.LoadManifestFile(fakeroot.X, fakeroot.X.JiriManifestFile(), localProjects, false)
	lockPath := fakeroot.X.Root + "/jiri.lock"
	resolveFlag.lockFilePath = lockPath
	resolveFlag.enablePackageLock = true
	resolveFlag.enableProjectLock = true
	args := []string{}
	if err := runResolve(fakeroot.X, args); err != nil {
		t.Errorf("resolve failed due to error %v", err)
	}
	data, err := ioutil.ReadFile(lockPath)
	if err != nil {
		t.Errorf("%+v", err)
	}

	projLocks, _, err := project.UnmarshalLockEntries(data)
	if err != nil {
		t.Errorf("parse generated lockfile failed due to error: %v", err)
	}

	if len(projects) != len(projLocks) {
		t.Errorf("expecting %v project locks, got %v", len(projects), len(projLocks))
	}

	for k, v := range projects {
		if projLock, ok := projLocks[project.ProjectLockKey(k)]; ok {
			if v.Revision != projLock.Revision {
				t.Errorf("expecting revision %q for project %q, got %q", v.Revision, v.Name, projLock.Revision)
			}
		} else {
			t.Errorf("project %q not found in lockfile", v.Name)
		}
	}
}

func TestResolvePackages(t *testing.T) {
	fakeroot, cleanup := jiritest.NewFakeJiriRoot(t)
	defer cleanup()
	// Replace the .jiri_manifest with package declarations
	pkgData := []byte(`
<manifest>
	<packages>
		<package name="gn/gn/${platform}"
             version="git_revision:bdb0fd02324b120cacde634a9235405061c8ea06"
             path="buildtools/{{.OS}}-x64"/>
    	<package name="infra/tools/luci/vpython/${platform}"
             version="git_revision:9a931a5307c46b16b1c12e01e8239d4a73830b89"
             path="buildtools/{{.OS}}-x64"/>
	</packages>
</manifest>
`)
	// Currently jiri is hard coded to only verify cipd packages for linux-amd64 and mac-amd64.
	// If new supported platform added, this test should be updated.
	expectedLocks := []project.PackageLock{
		project.PackageLock{
			PackageName: "gn/gn/linux-amd64",
			VersionTag:  "git_revision:bdb0fd02324b120cacde634a9235405061c8ea06",
			InstanceID:  "0uGjKAZkJXPZjtYktgEwHiNbwsut_qRsk7ZCGGxi82IC",
		},
		project.PackageLock{
			PackageName: "gn/gn/mac-amd64",
			VersionTag:  "git_revision:bdb0fd02324b120cacde634a9235405061c8ea06",
			InstanceID:  "rN2F641yR4Bj-H1q8OwC_RiqRpUYxy3hryzRfPER9wcC",
		},
		project.PackageLock{
			PackageName: "infra/tools/luci/vpython/linux-amd64",
			VersionTag:  "git_revision:9a931a5307c46b16b1c12e01e8239d4a73830b89",
			InstanceID:  "uCjugbKg6wMIF6_H_BHECZQdcGRebhnZ6LzSodPHQ7AC",
		},
		project.PackageLock{
			PackageName: "infra/tools/luci/vpython/mac-amd64",
			VersionTag:  "git_revision:9a931a5307c46b16b1c12e01e8239d4a73830b89",
			InstanceID:  "yAdok-mh5vfwq1vCAHprmejM9iE7R1t9Wn6RxrWmAAEC",
		},
	}
	if err := ioutil.WriteFile(fakeroot.X.JiriManifestFile(), pkgData, 0644); err != nil {
		t.Errorf("failed to write package information into .jiri_manifest due to error: %v", err)
	}
	lockPath := fakeroot.X.Root + "/jiri.lock"
	resolveFlag.lockFilePath = lockPath
	resolveFlag.enablePackageLock = true
	resolveFlag.enableProjectLock = true
	resolveFlag.enablePackageVersion = true
	args := []string{}
	if err := runResolve(fakeroot.X, args); err != nil {
		t.Errorf("resolve failed due to error: %v", err)
	}
	data, err := ioutil.ReadFile(lockPath)
	if err != nil {
		t.Errorf("read generated lockfile failed due to error: %v", err)
	}
	_, pkgLocks, err := project.UnmarshalLockEntries(data)
	if err != nil {
		t.Errorf("parse generated lockfile failed due to error: %v", err)
	}
	if len(expectedLocks) != len(pkgLocks) {
		t.Errorf("expecting %v locks, got %v", len(expectedLocks), len(pkgLocks))
	}
	for _, v := range expectedLocks {
		if pkgLock, ok := pkgLocks[v.Key()]; ok {
			if pkgLock != v {
				t.Errorf("expecting instance id %q for package %q, got %q", v.InstanceID, v.PackageName, pkgLock.InstanceID)
			}
		} else {
			t.Errorf("package %q not found in generated lockfile", v.PackageName)
		}
	}
}
