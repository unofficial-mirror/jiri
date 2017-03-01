// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"fmt"
	git2go "github.com/libgit2/git2go"
)

type Git struct {
	rootDir string
}

func NewGit(path string) *Git {
	return &Git{
		rootDir: path,
	}
}

func (g *Git) CurrentRevision() (string, error) {
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return "", err
	}
	defer repo.Free()
	head, err := repo.Head()
	if err != nil {
		return "", err
	}
	defer head.Free()
	return head.Target().String(), nil
}

// Fetch fetches refs and tags from the given remote.
func (g *Git) Fetch(remote string, opts ...FetchOpt) error {
	return g.FetchRefspec(remote, "", opts...)
}

// FetchRefspec fetches refs and tags from the given remote for a particular refspec.
func (g *Git) FetchRefspec(remoteName, refspec string, opts ...FetchOpt) error {
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return err
	}
	defer repo.Free()
	if remoteName == "" {
		return fmt.Errorf("No remote passed")
	}
	remote, err := repo.Remotes.Lookup(remoteName)
	if err != nil {
		return err
	}
	defer remote.Free()
	fetchOptions := &git2go.FetchOptions{}
	tags := false
	prune := false
	for _, opt := range opts {
		switch typedOpt := opt.(type) {
		case TagsOpt:
			tags = bool(typedOpt)
		case PruneOpt:
			prune = bool(typedOpt)
		}
	}
	refspecList := []string{}
	if refspec != "" {
		refspecList = []string{refspec}
	}
	if prune {
		fetchOptions.Prune = git2go.FetchPruneOn
	}
	if tags {
		fetchOptions.DownloadTags = git2go.DownloadTagsAll
	}
	return remote.Fetch(refspecList, fetchOptions, "")
}

func (g *Git) SetRemoteUrl(remote, url string) error {
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return err
	}
	defer repo.Free()
	return repo.Remotes.SetUrl(remote, url)
}

type Reference struct {
	Name     string
	Revision string
	IsHead   bool
}

type Branch struct {
	*Reference
	Tracking *Reference
}

func (g *Git) GetAllBranchesInfo() ([]Branch, error) {
	var branches []Branch
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return nil, err
	}
	defer repo.Free()
	bi, err := repo.NewBranchIterator(git2go.BranchAll)
	if err != nil {
		return nil, err
	}
	err = bi.ForEach(func(b *git2go.Branch, bt git2go.BranchType) error {
		if bt == git2go.BranchRemote {
			return nil
		}
		isHead, err := b.IsHead()
		if err != nil {
			return err
		}
		name, err := b.Name()
		if err != nil {
			return err
		}
		revision := ""
		if t := b.Target(); t != nil {
			revision = t.String()
		}
		branch := Branch{
			&Reference{
				Name:     name,
				Revision: revision,
				IsHead:   isHead,
			}, nil,
		}
		if u, err := b.Upstream(); err != nil && !git2go.IsErrorCode(err, git2go.ErrNotFound) {
			return err
		} else if u != nil {
			defer u.Free()
			branch.Tracking = &Reference{
				Name: u.Shorthand(),
				Revision: u.Target().String(),
			}
		}
		branches = append(branches, branch)
		return nil
	})
	return branches, err
}
