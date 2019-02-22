// Copyright 2018 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"fuchsia.googlesource.com/jiri/project"
)

func TestPrefixTree(t *testing.T) {
	_, fakeroot, cleanup := setupUniverse(t)
	defer cleanup()

	projects := []project.Project{
		project.Project{Name: "root", Path: "."},
		project.Project{Name: "a", Path: "a"},
		project.Project{Name: "b", Path: "b"},
		project.Project{Name: "c/d/e", Path: "c/d/e"},
		project.Project{Name: "c/d/f", Path: "c/d/f"},
		project.Project{Name: "c/d", Path: "c/d"},
		project.Project{Name: "c", Path: "c"},
	}
	expectedDropped := []project.Project{
		projects[0],
		projects[3],
		projects[4],
		projects[5],
	}

	// Fill projects into prefix tree
	root := projectTree{nil, make(map[string]*projectTree)}
	dropped := make(project.Projects)
	treeRoot := projectTreeRoot{&root, dropped}
	for _, v := range projects {
		if err := treeRoot.add(fakeroot.X, v); err != nil {
			t.Errorf("adding project to prefixTree failed due to error: %v", err)
			break
		}
	}

	// generate logs when test failed
	failedDropped := func() {
		t.Logf("wrong nested projects list")
		t.Logf("expecting: ")
		for _, v := range expectedDropped {
			t.Logf("\tproject:%q", v.Path)
		}
		t.Logf("got:")
		for _, v := range treeRoot.dropped {
			t.Logf("\tproject:%q", v.Path)
		}
		t.Fail()
	}

	// Verify nested projects
	if len(treeRoot.dropped) != len(expectedDropped) {
		failedDropped()
	}
	for _, v := range expectedDropped {
		if _, ok := treeRoot.dropped[v.Key()]; !ok {
			failedDropped()
			break
		}
	}

	// Verify the shape of prefix tree
	if len(root.children) == 3 {
		prefixes := []string{"a", "b", "c"}
		for _, v := range prefixes {
			if _, ok := root.children[v]; !ok {
				t.Errorf("root node does not contain project %q", v)
			}
		}
		for _, v := range root.children {
			if len(v.children) != 0 {
				t.Errorf("more than 1 level of nodes found in prefix tree")
			}
		}
	} else {
		t.Errorf("expecting %v first level nodes, but got %v", 3, len(root.children))
	}
}

func TestGitModules(t *testing.T) {
	goldenScript := []byte(`#!/bin/sh
git update-index --add --cacheinfo 160000 87326c54332e5be21eda2173bb001aaee73a9ab7 "manifest"
git update-index --add --cacheinfo 160000 87f863bcbc7cd2177bac17c61e31093de6eeed28 "path-0"
git update-index --add --cacheinfo 160000 87f863bcbc7cd2177bac17c61e31093de6eeed28 "path-1"
git update-index --add --cacheinfo 160000 87f863bcbc7cd2177bac17c61e31093de6eeed28 "path-2"`)

	goldenModule := []byte(`[submodule "manifest"]
	branch = 87326c54332e5be21eda2173bb001aaee73a9ab7
	path = manifest
	url = /tmp/115893653/manifest
[submodule "project-0"]
	branch = 87f863bcbc7cd2177bac17c61e31093de6eeed28
	path = path-0
	url = /tmp/115893653/project-0
[submodule "project-1"]
	branch = 87f863bcbc7cd2177bac17c61e31093de6eeed28
	path = path-1
	url = /tmp/115893653/project-1
[submodule "project-2"]
	branch = 87f863bcbc7cd2177bac17c61e31093de6eeed28
	path = path-2
	url = /tmp/115893653/project-2`)

	// Setup fake workspace and update $JIRI_ROOT
	_, fakeroot, cleanup := setupUniverse(t)
	defer cleanup()
	if err := fakeroot.UpdateUniverse(false); err != nil {
		t.Errorf("%v", err)
	}

	localProjects, err := project.LocalProjects(fakeroot.X, project.FullScan)
	if err != nil {
		t.Errorf("scanning local fake project failed due to error %v", err)
	}

	pathMap := make(map[string]project.Project)
	for _, v := range localProjects {
		v.Path, err = makePathRel(fakeroot.X.Root, v.Path)
		if err != nil {
			t.Errorf("path relativation failed due to error %v", err)
		}
		pathMap[v.Path] = v
	}

	tempDir, err := ioutil.TempDir("", "gitmodules")
	if err != nil {
		t.Errorf(".gitmodules generation failed due to error %v", err)
	}
	defer os.RemoveAll(tempDir)

	genGitModuleFlags.genScript = path.Join(tempDir, "setup.sh")
	err = runGenGitModule(fakeroot.X, []string{path.Join(tempDir, ".gitmodules")})
	if err != nil {
		t.Errorf(".gitmodules generation failed due to error %v", err)
	}

	// Read and verify content of generated script
	data, err := ioutil.ReadFile(genGitModuleFlags.genScript)
	if err != nil {
		t.Errorf("reading generated script file failed due to error: %v", err)
	}
	t.Logf("generated script content \n%s\n", string(data))

	if err := verifyScript(goldenScript, data); err != nil {
		t.Errorf("verifying generated script failed due to error: %v", err)
	}

	// Read and verify content of generated .gitmodules file
	data, err = ioutil.ReadFile(path.Join(tempDir, ".gitmodules"))
	if err != nil {
		t.Errorf("reading generated .gitmodules file failed due to error: %v", err)
	}
	t.Logf("generated gitmodule content \n%s\n", string(data))

	if err := verifyModules(goldenModule, data); err != nil {
		t.Errorf("verifying generated .gitmodules failed due to error: %v", err)
	}
}

func readlines(data []byte) ([]string, error) {
	var buffer bytes.Buffer
	retLines := make([]string, 0)
	if _, err := buffer.Write(data); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(&buffer)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		retLines = append(retLines, line)
	}
	return retLines, nil
}

func verifyModules(golden, tests []byte) error {
	goldenLines, err := readlines(golden)
	if err != nil {
		return err
	}
	testLines, err := readlines(tests)
	if err != nil {
		return err
	}
	if len(goldenLines) != len(testLines) {
		return fmt.Errorf("expecting %q non-empty/non-comment lines from generated .gitmodules, got %q lines", len(goldenLines), len(testLines))
	}
	for i := 0; i < len(goldenLines); i++ {
		goldenLine := goldenLines[i]
		testLine := testLines[i]
		if strings.HasPrefix(testLine, "branch = ") {
			revision := testLine[len("branch = "):]
			// revision should be 20 bytes in hex format
			if len(revision) != 40 {
				return fmt.Errorf("illegal revision hash in line %q", testLine)
			}
			if _, err := hex.DecodeString(revision); err != nil {
				return fmt.Errorf("illegal revision hash in line %q", testLine)
			}
			continue
		}
		if strings.HasPrefix(testLine, "url = ") {
			testPath := testLine[len("url = "):]
			goldenPath := goldenLine[len("url = "):]
			testPathFields := strings.Split(testPath, string(filepath.Separator))
			goldenPathFields := strings.Split(goldenPath, string(filepath.Separator))
			testPath = testPathFields[len(testPathFields)-1]
			goldenPath = goldenPathFields[len(goldenPathFields)-1]
			if testPath != goldenPath {
				return fmt.Errorf("path mismatch, expecting %q, got %q", goldenPath, testPath)
			}
			continue
		}
		if goldenLine != testLine {
			return fmt.Errorf("in generated .gitmodules file, expecting %q, got %q", goldenLine, testLine)
		}
	}
	return nil
}

func verifyScript(golden, tests []byte) error {
	goldenLines, err := readlines(golden)
	if err != nil {
		return err
	}
	testLines, err := readlines(tests)
	if err != nil {
		return err
	}
	if len(goldenLines) != len(testLines) {
		return fmt.Errorf("expecting %q non-empty/non-comment lines from generated script, got %q lines", len(goldenLines), len(testLines))
	}
	for i := 0; i < len(goldenLines); i++ {
		goldenLine := goldenLines[i]
		testLine := testLines[i]
		goldenFields := strings.Fields(goldenLine)
		testFields := strings.Fields(testLine)
		if len(goldenFields) != len(testFields) {
			return fmt.Errorf("format error at line %q in generated script, expecting something like %q", testLine, goldenLine)
		}
		// Any field except the revision hash should be exact match.
		for j := 0; j < 5; j++ {
			if goldenFields[j] != testFields[j] {
				return fmt.Errorf("command missmatch at line %q in generated script, expecting something like %q", testLine, goldenLine)
			}
		}
		if goldenFields[6] != testFields[6] {
			return fmt.Errorf("command missmatch at line %q in generated script, expecting something like %q", testLine, goldenLine)
		}
		// revision should be 20 bytes in hex format
		if len(testFields[5]) != 40 {
			return fmt.Errorf("illegal revision hash in line %q", testLine)
		}
		if _, err := hex.DecodeString(testFields[5]); err != nil {
			return fmt.Errorf("illegal revision hash in git command %q", testLine)
		}
	}
	return nil
}
