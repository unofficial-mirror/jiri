// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package project

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/gerrit"
	"fuchsia.googlesource.com/jiri/gitutil"
)

const (
	SourceManifestVersion = int32(0)
)

// This was created using proto file: https://github.com/luci/recipes-py/blob/master/recipe_engine/source_manifest.proto.
type SourceManifest_GitCheckout struct {
	// The canonicalized URL of the original repo that is considered the “source
	// of truth” for the source code. Ex.
	//   https://chromium.googlesource.com/chromium/tools/build.git
	//   https://github.com/luci/recipes-py
	RepoUrl string `json:"repo_url,omitempty"`

	// If different from repo_url, this can be the URL of the repo that the source
	// was actually fetched from (i.e. a mirror). Ex.
	//   https://chromium.googlesource.com/external/github.com/luci/recipes-py
	//
	// If this is empty, it's presumed to be equal to repo_url.
	FetchUrl string `json:"fetch_url,omitempty"`

	// The fully resolved revision (commit hash) of the source. Ex.
	//   3617b0eea7ec74b8e731a23fed2f4070cbc284c4
	//
	// Note that this is the raw revision bytes, not their hex-encoded form.
	Revision string `json:"revision,omitempty"`

	// The ref that the task used to resolve/fetch the revision of the source
	// (if any). Ex.
	//   refs/heads/master
	//   refs/changes/04/511804/4
	//
	// This should always be a ref on the hosted repo (not any local alias
	// like 'refs/remotes/...').
	//
	// This should always be an absolute ref (i.e. starts with 'refs/'). An
	// example of a non-absolute ref would be 'master'.
	FetchRef string `json:"fetch_ref,omitempty"`
}

type SourceManifest_Directory struct {
	GitCheckout *SourceManifest_GitCheckout `json:"git_checkout,omitempty"`
}

type SourceManifest struct {
	// Version will increment on backwards-incompatible changes only. Backwards
	// compatible changes will not alter this version number.
	//
	// Currently, the only valid version number is 0.
	Version int32 `json:"version"`

	// Map of local file system directory path (with forward slashes) to
	// a Directory message containing one or more deployments.
	//
	// The local path is relative to some job-specific root. This should be used
	// for informational/display/organization purposes, and should not be used as
	// a global primary key. i.e. if you depend on chromium/src.git being in
	// a folder called “src”, I will find you and make really angry faces at you
	// until you change it...（╬ಠ益ಠ). Instead, implementations should consider
	// indexing by e.g. git repository URL or cipd package name as more better
	// primary keys.
	Directories map[string]*SourceManifest_Directory `json:"directories"`
}

func getCLRefByCommit(jirix *jiri.X, gerritHost, revision string) (string, error) {
	hostUrl, err := url.Parse(gerritHost)
	if err != nil {
		return "", fmt.Errorf("invalid gerrit host %q: %s", gerritHost, err)
	}
	g := gerrit.New(jirix, hostUrl)
	cls, err := g.ListChangesByCommit(revision)
	if err != nil {
		return "", fmt.Errorf("not able to get CL for revision %s: %s", revision, err)
	}
	for _, c := range cls {
		if v, ok := c.Revisions[revision]; ok {
			return v.Fetch.Ref, nil
		}
	}
	return "", nil
}

func NewSourceManifest(jirix *jiri.X, projects Projects) (*SourceManifest, MultiError) {
	jirix.TimerPush("create source manifest")
	defer jirix.TimerPop()

	workQueue := make(chan Project, len(projects))
	for _, proj := range projects {
		if err := proj.relativizePaths(jirix.Root); err != nil {
			return nil, MultiError{err}
		}
		workQueue <- proj
	}
	close(workQueue)
	errs := make(chan error, len(projects))
	sm := &SourceManifest{
		Version:     SourceManifestVersion,
		Directories: make(map[string]*SourceManifest_Directory),
	}
	var mux sync.Mutex
	processProject := func(proj Project) error {
		gc := &SourceManifest_GitCheckout{
			RepoUrl: proj.Remote,
		}
		scm := gitutil.New(jirix, gitutil.RootDirOpt(filepath.Join(jirix.Root, proj.Path)))
		if rev, err := scm.CurrentRevision(); err != nil {
			return err
		} else {
			gc.Revision = rev
		}
		if proj.RemoteBranch == "" {
			proj.RemoteBranch = "master"
		}
		branchMap, err := scm.ListRemoteBranchesContainingRef(gc.Revision)
		if err != nil {
			return err
		}
		if branchMap["origin/"+proj.RemoteBranch] {
			gc.FetchRef = "refs/heads/" + proj.RemoteBranch
		} else {
			for b, _ := range branchMap {
				if strings.HasPrefix(b, "origin/HEAD ") {
					continue
				}
				if strings.HasPrefix(b, "origin") {
					gc.FetchRef = "refs/heads/" + strings.TrimLeft(b, "origin/")
					break
				}
			}

			// Try getting from gerrit
			if gc.FetchRef == "" && proj.GerritHost != "" {
				if ref, err := getCLRefByCommit(jirix, proj.GerritHost, gc.Revision); err != nil {
					// Don't fail
					jirix.Logger.Debugf("Error while fetching from gerrit for project %q: %s", proj.Name, err)
				} else if ref == "" {
					jirix.Logger.Debugf("Cannot get ref for project: %q, revision: %q", proj.Name, gc.Revision)
				} else {
					gc.FetchRef = ref
				}
			}
		}
		mux.Lock()
		sm.Directories[proj.Path] = &SourceManifest_Directory{GitCheckout: gc}
		mux.Unlock()
		return nil
	}

	var wg sync.WaitGroup
	for i := uint(0); i < jirix.Jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range workQueue {
				if err := processProject(p); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	var multiErr MultiError
	for err := range errs {
		multiErr = append(multiErr, err)
	}
	return sm, multiErr
}

func (sm *SourceManifest) ToFile(jirix *jiri.X, filename string) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		return fmtError(err)
	}
	out, err := json.MarshalIndent(sm, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize JSON output: %s\n", err)
	}

	err = ioutil.WriteFile(filename, out, 0600)
	if err != nil {
		return fmt.Errorf("failed write JSON output to %s: %s\n", filename, err)
	}

	return nil
}
