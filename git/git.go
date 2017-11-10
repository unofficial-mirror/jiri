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

func (g *Git) CurrentRevisionRaw() ([]byte, error) {
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return nil, err
	}
	defer repo.Free()
	head, err := repo.Head()
	if err != nil {
		return nil, err
	}
	defer head.Free()
	return head.Target()[:], nil
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

// BranchExists tests whether a branch with the given name exists in
// the local repository.
func (g *Git) BranchExists(branch string) (bool, error) {
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return false, err
	}
	defer repo.Free()
	if _, err := repo.LookupBranch(branch, git2go.BranchAll); err != nil {
		return false, nil
	}
	return true, nil
}

func (g *Git) CommitMsg(ref string) (string, error) {
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return "", err
	}
	defer repo.Free()
	obj, err := repo.RevparseSingle(ref)
	if err != nil {
		return "", err
	}
	defer obj.Free()
	c, err := obj.Peel(git2go.ObjectCommit)
	if err != nil {
		return "", err
	}
	defer c.Free()
	commit, err := c.AsCommit()
	if err != nil {
		return "", err
	}
	return commit.Message(), nil
}

// Fetch fetches refs and tags from the given remote.
func (g *Git) Fetch(remote string, opts ...FetchOpt) error {
	return g.FetchRefspec(remote, "", opts...)
}

func (g *Git) CreateLightweightTag(name string) error {
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return err
	}
	defer repo.Free()
	head, err := repo.Head()
	if err != nil {
		return err
	}
	defer head.Free()
	c, err := repo.LookupCommit(head.Target())
	if err != nil {
		return err
	}
	_, err = repo.Tags.CreateLightweight(name, c, false)
	return err
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

func (g *Git) ShortHash(ref string) (string, error) {
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return "", err
	}
	defer repo.Free()
	if obj, err := repo.RevparseSingle(ref); err != nil {
		return "", err
	} else {
		defer obj.Free()
		commit, err := obj.Peel(git2go.ObjectCommit)
		if err != nil {
			return "", err
		}
		return commit.ShortId()
	}
}

func (g *Git) UserInfoForCommit(ref string) (string, string, error) {
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return "", "", err
	}
	defer repo.Free()
	obj, err := repo.RevparseSingle(ref)
	if err != nil {
		return "", "", err
	}
	defer obj.Free()
	c, err := obj.Peel(git2go.ObjectCommit)
	if err != nil {
		return "", "", err
	}
	defer c.Free()
	commit, err := c.AsCommit()
	if err != nil {
		return "", "", err
	}
	defer commit.Free()
	return commit.Committer().Name, commit.Committer().Email, nil
}

// CurrentRevisionForRef gets current rev for ref/branch/tags
func (g *Git) CurrentRevisionForRef(ref string) (string, error) {
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return "", err
	}
	defer repo.Free()
	if obj, err := repo.RevparseSingle(ref); err != nil {
		return "", err
	} else {
		defer obj.Free()
		if obj.Type() == git2go.ObjectTag {
			tag, err := obj.AsTag()
			if err != nil {
				return "", err
			}
			defer tag.Free()
			return tag.TargetId().String(), nil
		}
		return obj.Id().String(), nil
	}
}

func (g *Git) MergedBranches(ref string) ([]string, error) {
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return nil, err
	}
	defer repo.Free()
	obj, err := repo.RevparseSingle(ref)
	if err != nil {
		return nil, err
	}
	defer obj.Free()
	baseCommit, err := obj.Peel(git2go.ObjectCommit)
	if err != nil {
		return nil, err
	}
	bi, err := repo.NewBranchIterator(git2go.BranchLocal)
	if err != nil {
		return nil, err
	}
	mergedBranches := []string{}
	err = bi.ForEach(func(b *git2go.Branch, bt git2go.BranchType) error {
		c := b.Target()
		if c == nil {
			// Ignore this branch
			return nil
		}
		if base, err := repo.MergeBase(c, baseCommit.Id()); err != nil {
			return err
		} else if base.String() == c.String() {
			name, err := b.Name()
			if err != nil {
				return err
			}
			mergedBranches = append(mergedBranches, name)
		}
		return nil
	})
	return mergedBranches, err
}

func (g *Git) SetUpstream(branch, upstream string) error {
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return err
	}
	defer repo.Free()
	b, err := repo.LookupBranch(branch, git2go.BranchLocal)
	if err != nil {
		return err
	}
	return b.SetUpstream(upstream)
}

func (g *Git) CreateBranchFromRef(branch, ref string) error {
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return err
	}
	defer repo.Free()
	obj, err := repo.RevparseSingle(ref)
	if err != nil {
		return err
	}
	defer obj.Free()
	c, err := obj.Peel(git2go.ObjectCommit)
	if err != nil {
		return err
	}
	defer c.Free()
	commit, err := c.AsCommit()
	if err != nil {
		return err
	}
	defer commit.Free()
	_, err = repo.CreateBranch(branch, commit, false)
	return err
}

func (g *Git) HasUntrackedFiles() (bool, error) {
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return false, err
	}
	defer repo.Free()
	opts := &git2go.StatusOptions{}
	opts.Show = git2go.StatusShowIndexAndWorkdir
	opts.Flags = git2go.StatusOptIncludeUntracked

	statusList, err := repo.StatusList(opts)
	if err != nil {
		return false, err
	}

	defer statusList.Free()
	entryCount, err := statusList.EntryCount()
	if err != nil {
		return false, err
	}
	for i := 0; i < entryCount; i++ {
		entry, err := statusList.ByIndex(i)
		if err != nil {
			return false, err
		}
		if (entry.Status & git2go.StatusWtNew) > 0 {
			return true, nil
		}
	}
	return false, nil
}
func (g *Git) HasUncommittedChanges() (bool, error) {
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return false, err
	}
	defer repo.Free()
	opts := &git2go.StatusOptions{}
	opts.Show = git2go.StatusShowIndexAndWorkdir

	statusList, err := repo.StatusList(opts)
	if err != nil {
		return false, err
	}

	defer statusList.Free()
	entryCount, err := statusList.EntryCount()
	if err != nil {
		return false, err
	}
	uncommitedFlag := git2go.StatusWtModified | git2go.StatusWtDeleted |
		git2go.StatusWtTypeChange | git2go.StatusIndexModified |
		git2go.StatusIndexNew | git2go.StatusIndexDeleted |
		git2go.StatusIndexTypeChange | git2go.StatusConflicted

	for i := 0; i < entryCount; i++ {
		entry, err := statusList.ByIndex(i)
		if err != nil {
			return false, err
		}
		if (entry.Status & uncommitedFlag) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// GetBranches returns a slice of the local branches of the current
// repository, followed by the name of the current branch.
func (g *Git) GetBranches() ([]string, string, error) {
	branches, current := []string{}, ""
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return nil, "", err
	}
	defer repo.Free()
	bi, err := repo.NewBranchIterator(git2go.BranchLocal)
	if err != nil {
		return nil, "", err
	}
	err = bi.ForEach(func(b *git2go.Branch, bt git2go.BranchType) error {
		isHead, err := b.IsHead()
		if err != nil {
			return err
		}
		name, err := b.Name()
		if err != nil {
			return err
		}
		branches = append(branches, name)
		if isHead {
			current = name
		}
		return nil
	})
	return branches, current, nil
}

func (g *Git) GetAllBranchesInfo() ([]Branch, error) {
	var branches []Branch
	repo, err := git2go.OpenRepository(g.rootDir)
	if err != nil {
		return nil, err
	}
	defer repo.Free()
	bi, err := repo.NewBranchIterator(git2go.BranchLocal)
	if err != nil {
		return nil, err
	}
	err = bi.ForEach(func(b *git2go.Branch, bt git2go.BranchType) error {
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
				Name:     u.Shorthand(),
				Revision: u.Target().String(),
			}
		}
		branches = append(branches, branch)
		return nil
	})
	return branches, err
}
