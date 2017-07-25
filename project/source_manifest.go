// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package project

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"

	"fuchsia.googlesource.com/jiri"
)

type SCMType int16

const (
	GIT                   = SCMType(0)
	SourceManifestVersion = 1
)

type SourceCheckout struct {
	SCMType     SCMType `json:"scm_type"`
	RepoURL     string  `json:"repo_url"`
	Revision    string  `json:"revision"`
	LocalPath   string  `json:"local_path"`
	TrackingRef string  `json:"tracking_ref,omitempty"`
}

type SourceManifest struct {
	Version   int              `json:"version"`
	Checkouts []SourceCheckout `json:"checkouts"`
}

func NewSourceManifest(jirix *jiri.X, projects Projects) (*SourceManifest, error) {
	p := make([]Project, len(projects))
	i := 0
	for _, proj := range projects {
		if err := proj.relativizePaths(jirix.Root); err != nil {
			return nil, err
		}
		p[i] = proj
		i++
	}
	sm := &SourceManifest{
		Version:   SourceManifestVersion,
		Checkouts: make([]SourceCheckout, len(p)),
	}
	sort.Sort(ProjectsByPath(p))
	for i, proj := range p {
		sc := SourceCheckout{
			SCMType:   GIT,
			RepoURL:   proj.Remote,
			Revision:  proj.Revision,
			LocalPath: proj.Path,
		}
		if proj.RemoteBranch != "" && proj.RemoteBranch != "master" {
			sc.TrackingRef = "refs/heads/" + proj.RemoteBranch
		}
		sm.Checkouts[i] = sc
	}
	return sm, nil
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
