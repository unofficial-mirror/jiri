// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package project

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/log"
	"fuchsia.googlesource.com/jiri/osutil"
)

// fsUpdates is used to track filesystem updates made by operations.
// TODO(nlacasse): Currently we only use fsUpdates to track deletions so that
// jiri can delete and create a project in the same directory in one update.
// There are lots of other cases that should be covered though, like detecting
// when two projects would be created in the same directory.
type fsUpdates struct {
	deletedDirs map[string]bool
}

func newFsUpdates() *fsUpdates {
	return &fsUpdates{
		deletedDirs: map[string]bool{},
	}
}

func (u *fsUpdates) deleteDir(dir string) {
	dir = filepath.Clean(dir)
	u.deletedDirs[dir] = true
}

func (u *fsUpdates) isDeleted(dir string) bool {
	_, ok := u.deletedDirs[filepath.Clean(dir)]
	return ok
}

type operation interface {
	// Project identifies the project this operation pertains to.
	Project() Project
	// Kind returns the kind of operation.
	Kind() string
	// Run executes the operation.
	Run(jirix *jiri.X) error
	// String returns a string representation of the operation.
	String() string
	// Test checks whether the operation would fail.
	Test(jirix *jiri.X, updates *fsUpdates) error
}

// commonOperation represents a project operation.
type commonOperation struct {
	// project holds information about the project such as its
	// name, local path, and the protocol it uses for version
	// control.
	project Project
	// destination is the new project path.
	destination string
	// source is the current project path.
	source string
	// state is the state of the local project
	state ProjectState
}

func (op commonOperation) Project() Project {
	return op.project
}

// createOperation represents the creation of a project.
type createOperation struct {
	commonOperation
}

func (op createOperation) Kind() string {
	return "create"
}

func (op createOperation) checkoutProject(jirix *jiri.X, cache string) error {
	var err error
	remote := rewriteRemote(jirix, op.project.Remote)
	scm := gitutil.New(jirix, gitutil.RootDirOpt(op.project.Path))
	// Hack to make fuchsia.git happen
	if op.destination == jirix.Root {
		if err = scm.Init(op.destination); err != nil {
			return err
		}
		if err = scm.AddOrReplaceRemote("origin", remote); err != nil {
			return err
		}
		// This appears to be set to 0 via some quirk of `git init`.
		if err := scm.Config("core.repositoryformatversion", "1"); err != nil {
			return err
		}
		if jirix.Partial {
			if err := scm.Config("extensions.partialClone", "origin"); err != nil {
				return err
			}
			if err := scm.AddOrReplacePartialRemote("origin", remote); err != nil {
				return err
			}
		}
		// We must specify a refspec here in order for patch to be able to set
		// upstream to 'origin/master'.
		if err := scm.Config("remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
			return err
		}
		if cache != "" {
			objPath := "objects"
			if jirix.Partial {
				objPath = ".git/objects"
			}
			if err := ioutil.WriteFile(filepath.Join(op.destination, ".git/objects/info/alternates"), []byte(filepath.Join(cache, objPath) + "\n"), 0644); err != nil {
				return err
			}
		}
		if err = fetchAll(jirix, op.project); err != nil {
			return err
		}
	} else {
		r := remote
		if cache != "" {
			r = cache
			defer func() {
				if err := scm.AddOrReplaceRemote("origin", remote); err != nil {
						jirix.Logger.Errorf("failed to set remote back to %v for project %+v", remote, op.project)
				}
			}()
		}
		opts := []gitutil.CloneOpt{gitutil.NoCheckoutOpt(true)}
		if op.project.HistoryDepth > 0 {
			opts = append(opts, gitutil.DepthOpt(op.project.HistoryDepth))
		} else {
			// Shallow clones can not be used as as local git reference
			opts = append(opts, gitutil.ReferenceOpt(cache))
		}
		// Passing --filter=blob:none for a local clone is a no-op.
		if (cache == r || cache == "") && jirix.Partial {
			opts = append(opts, gitutil.OmitBlobsOpt(true))
		}
		if err = clone(jirix, r, op.destination, opts...); err != nil {
			return err
		}
	}

	if err := os.Chmod(op.destination, os.FileMode(0755)); err != nil {
		return fmtError(err)
	}

	if err := checkoutHeadRevision(jirix, op.project, false); err != nil {
		return err
	}

	if err := writeMetadata(jirix, op.project, op.project.Path); err != nil {
		return err
	}
	// Delete inital branch(es)
	if branches, _, err := scm.GetBranches(); err != nil {
		jirix.Logger.Warningf("not able to get branches for newly created project %s(%s)\n\n", op.project.Name, op.project.Path)
	} else {
		scm := gitutil.New(jirix, gitutil.RootDirOpt(op.project.Path))
		for _, b := range branches {
			if err := scm.DeleteBranch(b); err != nil {
				jirix.Logger.Warningf("not able to delete branch %s for project %s(%s)\n\n", b, op.project.Name, op.project.Path)
			}
		}
	}
	return nil
}

func (op createOperation) Run(jirix *jiri.X) (e error) {
	path, perm := filepath.Dir(op.destination), os.FileMode(0755)

	// Check the local file system.
	if op.destination != jirix.Root {
		if _, err := os.Stat(op.destination); err != nil {
			if !os.IsNotExist(err) {
				return fmtError(err)
			}
		} else {
			if isEmpty, err := isEmpty(op.destination); err != nil {
				return err
			} else if !isEmpty {
				return fmt.Errorf("cannot create %q as it already exists and is not empty", op.destination)
			} else {
				if err := os.RemoveAll(op.destination); err != nil {
					return fmt.Errorf("Not able to delete %q", op.destination)
				}
			}
		}

		if err := os.MkdirAll(path, perm); err != nil {
			return fmtError(err)
		}
	}

	cache, err := op.project.CacheDirPath(jirix)
	if err != nil {
		return err
	}
	if !isPathDir(cache) {
		cache = ""
	}

	if err := op.checkoutProject(jirix, cache); err != nil {
		if op.destination != jirix.Root {
			if err := os.RemoveAll(op.destination); err != nil {
				jirix.Logger.Warningf("Not able to remove %q after create failed: %s", op.destination, err)
			}
		}
		return err
	}
	return nil
}

func (op createOperation) String() string {
	return fmt.Sprintf("create project %q in %q and advance it to %q", op.project.Name, op.destination, fmtRevision(op.project.Revision))
}

func (op createOperation) Test(jirix *jiri.X, updates *fsUpdates) error {
	return nil
}

// deleteOperation represents the deletion of a project.
type deleteOperation struct {
	commonOperation
}

func (op deleteOperation) Kind() string {
	return "delete"
}

func (op deleteOperation) Run(jirix *jiri.X) error {
	if op.project.LocalConfig.Ignore {
		jirix.Logger.Warningf("Project %s(%s) won't be deleted due to it's local-config\n\n", op.project.Name, op.source)
		return nil
	}
	// Never delete projects with non-master branches, uncommitted
	// work, or untracked content.
	scm := gitutil.New(jirix, gitutil.RootDirOpt(op.project.Path))
	branches, _, err := scm.GetBranches()
	if err != nil {
		return fmt.Errorf("Cannot get branches for project %q: %s", op.Project().Name, err)
	}
	uncommitted, err := scm.HasUncommittedChanges()
	if err != nil {
		return fmt.Errorf("Cannot get uncommited changes for project %q: %s", op.Project().Name, err)
	}
	untracked, err := scm.HasUntrackedFiles()
	if err != nil {
		return fmt.Errorf("Cannot get untracked changes for project %q: %s", op.Project().Name, err)
	}
	extraBranches := false
	for _, branch := range branches {
		if !strings.Contains(branch, "HEAD detached") {
			extraBranches = true
			break
		}
	}

	if extraBranches || uncommitted || untracked {
		rmCommand := jirix.Color.Yellow("rm -rf %q", op.source)
		unManageCommand := jirix.Color.Yellow("rm -rf %q", filepath.Join(op.source, jiri.ProjectMetaDir))
		msg := ""
		if extraBranches {
			msg = fmt.Sprintf("Project %q won't be deleted as it contains branches", op.project.Name)
		} else {
			msg = fmt.Sprintf("Project %q won't be deleted as it might contain changes", op.project.Name)
		}
		msg += fmt.Sprintf("\nIf you no longer need it, invoke '%s'", rmCommand)
		msg += fmt.Sprintf("\nIf you no longer want jiri to manage it, invoke '%s'\n\n", unManageCommand)
		jirix.Logger.Warningf(msg)
		return nil
	}

	if err := os.RemoveAll(op.source); err != nil {
		return fmtError(err)
	}
	return removeEmptyParents(jirix, path.Dir(op.source))
}

func removeEmptyParents(jirix *jiri.X, dir string) error {
	isEmpty := func(name string) (bool, error) {
		f, err := os.Open(name)
		if err != nil {
			return false, err
		}
		defer f.Close()
		_, err = f.Readdirnames(1)
		if err == io.EOF {
			// empty dir
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	}
	if jirix.Root == dir || dir == "" || dir == "." {
		return nil
	}
	empty, err := isEmpty(dir)
	if err != nil {
		return err
	}
	if empty {
		if err := os.Remove(dir); err != nil {
			return err
		}
		jirix.Logger.Debugf("gc deleted empty parent directory: %v", dir)
		return removeEmptyParents(jirix, path.Dir(dir))
	}
	return nil
}

func (op deleteOperation) String() string {
	return fmt.Sprintf("delete project %q from %q", op.project.Name, op.source)
}

func (op deleteOperation) Test(jirix *jiri.X, updates *fsUpdates) error {
	if _, err := os.Stat(op.source); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("cannot delete %q as it does not exist", op.source)
		}
		return fmtError(err)
	}
	updates.deleteDir(op.source)
	return nil
}

// moveOperation represents the relocation of a project.
type moveOperation struct {
	commonOperation
	rebaseTracked   bool
	rebaseUntracked bool
	rebaseAll       bool
	snapshot        bool
}

func (op moveOperation) Kind() string {
	return "move"
}

func (op moveOperation) Run(jirix *jiri.X) error {
	if op.project.LocalConfig.Ignore {
		jirix.Logger.Warningf("Project %s(%s) won't be moved or updated  due to it's local-config\n\n", op.project.Name, op.source)
		return nil
	}
	// If it was nested project it might have been moved with its parent project
	if op.source != op.destination {
		path, perm := filepath.Dir(op.destination), os.FileMode(0755)
		if err := os.MkdirAll(path, perm); err != nil {
			return fmtError(err)
		}
		if err := osutil.Rename(op.source, op.destination); err != nil {
			return fmtError(err)
		}
	}
	if err := syncProjectMaster(jirix, op.project, op.state, op.rebaseTracked, op.rebaseUntracked, op.rebaseAll, op.snapshot); err != nil {
		return err
	}
	return writeMetadata(jirix, op.project, op.project.Path)
}

func (op moveOperation) String() string {
	return fmt.Sprintf("move project %q located in %q to %q and advance it to %q", op.project.Name, op.source, op.destination, fmtRevision(op.project.Revision))
}

func (op moveOperation) Test(jirix *jiri.X, updates *fsUpdates) error {
	if _, err := os.Stat(op.source); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("cannot move %q to %q as the source does not exist", op.source, op.destination)
		}
		return fmtError(err)
	}
	if _, err := os.Stat(op.destination); err != nil {
		if !os.IsNotExist(err) {
			return fmtError(err)
		}
	} else {
		return fmt.Errorf("cannot move %q to %q as the destination already exists", op.source, op.destination)
	}
	updates.deleteDir(op.source)
	return nil
}

// changeRemoteOperation represents the chnage of remote URL
type changeRemoteOperation struct {
	commonOperation
	rebaseTracked   bool
	rebaseUntracked bool
	rebaseAll       bool
	snapshot        bool
}

func (op changeRemoteOperation) Kind() string {
	return "change-remote"
}

func (op changeRemoteOperation) Run(jirix *jiri.X) error {
	if op.project.LocalConfig.Ignore || op.project.LocalConfig.NoUpdate {
		jirix.Logger.Warningf("Project %s(%s) won't be updated due to it's local-config. It has a changed remote\n\n", op.project.Name, op.project.Path)
		return nil
	}
	git := gitutil.New(jirix, gitutil.RootDirOpt(op.project.Path))
	tempRemote := "new-remote-origin"
	if err := git.AddRemote(tempRemote, op.project.Remote); err != nil {
		return err
	}
	defer git.DeleteRemote(tempRemote)

	if err := fetch(jirix, op.project.Path, tempRemote); err != nil {
		return err
	}

	// Check for all leaf commits in new remote
	for _, branch := range op.state.Branches {
		if containingBranches, err := git.GetRemoteBranchesContaining(branch.Revision); err != nil {
			return err
		} else {
			foundBranch := false
			for _, remoteBranchName := range containingBranches {
				if strings.HasPrefix(remoteBranchName, tempRemote) {
					foundBranch = true
					break
				}
			}
			if !foundBranch {
				jirix.Logger.Errorf("Note: For project %q(%v), remote url has changed. Its branch %q is on a commit", op.project.Name, op.project.Path, branch.Name)
				jirix.Logger.Errorf("which is not in new remote(%v). Please manually reset your branches or move", op.project.Remote)
				jirix.Logger.Errorf("your project folder out of the root and try again")
				return nil
			}

		}
	}

	// Everything ok, change the remote url
	if err := git.SetRemoteUrl("origin", op.project.Remote); err != nil {
		return err
	}

	if err := fetch(jirix, op.project.Path, "", gitutil.AllOpt(true), gitutil.PruneOpt(true)); err != nil {
		return err
	}

	if err := syncProjectMaster(jirix, op.project, op.state, op.rebaseTracked, op.rebaseUntracked, op.rebaseAll, op.snapshot); err != nil {
		return err
	}

	return writeMetadata(jirix, op.project, op.project.Path)
}

func (op changeRemoteOperation) String() string {
	return fmt.Sprintf("Change remote of project %q to %q and update it to %q", op.project.Name, op.project.Remote, fmtRevision(op.project.Revision))
}

func (op changeRemoteOperation) Test(jirix *jiri.X, _ *fsUpdates) error {
	return nil
}

// updateOperation represents the update of a project.
type updateOperation struct {
	commonOperation
	rebaseTracked   bool
	rebaseUntracked bool
	rebaseAll       bool
	snapshot        bool
}

func (op updateOperation) Kind() string {
	return "update"
}

func (op updateOperation) Run(jirix *jiri.X) error {
	if err := syncProjectMaster(jirix, op.project, op.state, op.rebaseTracked, op.rebaseUntracked, op.rebaseAll, op.snapshot); err != nil {
		return err
	}
	return writeMetadata(jirix, op.project, op.project.Path)
}

func (op updateOperation) String() string {
	return fmt.Sprintf("advance/rebase project %q located in %q to %q", op.project.Name, op.source, fmtRevision(op.project.Revision))
}

func (op updateOperation) Test(jirix *jiri.X, _ *fsUpdates) error {
	return nil
}

// nullOperation represents a noop.  It is used for logging and adding project
// information to the current manifest.
type nullOperation struct {
	commonOperation
}

func (op nullOperation) Kind() string {
	return "null"
}

func (op nullOperation) Run(jirix *jiri.X) error {
	return writeMetadata(jirix, op.project, op.project.Path)
}

func (op nullOperation) String() string {
	return fmt.Sprintf("project %q located in %q at revision %q is up-to-date", op.project.Name, op.source, fmtRevision(op.project.Revision))
}

func (op nullOperation) Test(jirix *jiri.X, _ *fsUpdates) error {
	return nil
}

// operations is a sortable collection of operations
type operations []operation

// Len returns the length of the collection.
func (ops operations) Len() int {
	return len(ops)
}

// Less defines the order of operations. Operations are ordered first
// by their type and then by their project path.
//
// The order in which operation types are defined determines the order
// in which operations are performed. For correctness and also to
// minimize the chance of a conflict, the delete operations should
// happen before change-remote operations, which should happen before move
// operations. If two create operations make nested directories, the
// outermost should be created first.
func (ops operations) Less(i, j int) bool {
	vals := make([]int, 2)
	for idx, op := range []operation{ops[i], ops[j]} {
		switch op.Kind() {
		case "delete":
			vals[idx] = 0
		case "change-remote":
			vals[idx] = 1
		case "move":
			vals[idx] = 2
		case "create":
			vals[idx] = 3
		case "update":
			vals[idx] = 4
		case "null":
			vals[idx] = 5
		}
	}
	if vals[0] != vals[1] {
		return vals[0] < vals[1]
	}
	if vals[0] == 0 {
		// delete sub folder first
		return ops[i].Project().Path+string(filepath.Separator) > ops[j].Project().Path+string(filepath.Separator)
	} else {
		return ops[i].Project().Path+string(filepath.Separator) < ops[j].Project().Path+string(filepath.Separator)
	}
}

// Swap swaps two elements of the collection.
func (ops operations) Swap(i, j int) {
	ops[i], ops[j] = ops[j], ops[i]
}

// computeOperations inputs a set of projects to update and the set of
// current and new projects (as defined by contents of the local file
// system and manifest file respectively) and outputs a collection of
// operations that describe the actions needed to update the target
// projects.
func computeOperations(localProjects, remoteProjects Projects, states map[ProjectKey]*ProjectState, gc, rebaseTracked, rebaseUntracked, rebaseAll, snapshot bool) operations {
	result := operations{}
	allProjects := map[ProjectKey]bool{}
	for _, p := range localProjects {
		allProjects[p.Key()] = true
	}
	for _, p := range remoteProjects {
		allProjects[p.Key()] = true
	}
	for key, _ := range allProjects {
		var local, remote *Project
		var state *ProjectState
		if project, ok := localProjects[key]; ok {
			local = &project
		}
		if project, ok := remoteProjects[key]; ok {
			// update remote local config
			if local != nil {
				project.LocalConfig = local.LocalConfig
				remoteProjects[key] = project
			}
			remote = &project
		}
		if s, ok := states[key]; ok {
			state = s
		}
		result = append(result, computeOp(local, remote, state, gc, rebaseTracked, rebaseUntracked, rebaseAll, snapshot))
	}
	sort.Sort(result)
	return result
}

func computeOp(local, remote *Project, state *ProjectState, gc, rebaseTracked, rebaseUntracked, rebaseAll, snapshot bool) operation {
	switch {
	case local == nil && remote != nil:
		return createOperation{commonOperation{
			destination: remote.Path,
			project:     *remote,
			source:      "",
		}}
	case local != nil && remote == nil:
		return deleteOperation{commonOperation{
			destination: "",
			project:     *local,
			source:      local.Path,
		}}
	case local != nil && remote != nil:

		localBranchesNeedUpdating := false
		if !snapshot {
			cb := state.CurrentBranch
			if rebaseAll {
				for _, branch := range state.Branches {
					if branch.Tracking != nil {
						if branch.Revision != branch.Tracking.Revision {
							localBranchesNeedUpdating = true
							break
						}
					} else if rebaseUntracked && rebaseAll {
						// We put checks for untracked-branch updation in syncProjectMaster funtion
						localBranchesNeedUpdating = true
						break
					}
				}
			} else if cb.Name != "" && cb.Tracking != nil && cb.Revision != cb.Tracking.Revision {
				localBranchesNeedUpdating = true
			}
		}
		switch {
		case local.Remote != remote.Remote:
			return changeRemoteOperation{commonOperation{
				destination: remote.Path,
				project:     *remote,
				source:      local.Path,
				state:       *state,
			}, rebaseTracked, rebaseUntracked, rebaseAll, snapshot}
		case local.Path != remote.Path:
			// moveOperation also does an update, so we don't need to check the
			// revision here.
			return moveOperation{commonOperation{
				destination: remote.Path,
				project:     *remote,
				source:      local.Path,
				state:       *state,
			}, rebaseTracked, rebaseUntracked, rebaseAll, snapshot}
		case snapshot && local.Revision != remote.Revision:
			return updateOperation{commonOperation{
				destination: remote.Path,
				project:     *remote,
				source:      local.Path,
				state:       *state,
			}, rebaseTracked, rebaseUntracked, rebaseAll, snapshot}
		case localBranchesNeedUpdating || (state.CurrentBranch.Name == "" && local.Revision != remote.Revision):
			return updateOperation{commonOperation{
				destination: remote.Path,
				project:     *remote,
				source:      local.Path,
				state:       *state,
			}, rebaseTracked, rebaseUntracked, rebaseAll, snapshot}
		case state.CurrentBranch.Tracking == nil && local.Revision != remote.Revision:
			return updateOperation{commonOperation{
				destination: remote.Path,
				project:     *remote,
				source:      local.Path,
				state:       *state,
			}, rebaseTracked, rebaseUntracked, rebaseAll, snapshot}
		default:
			return nullOperation{commonOperation{
				destination: remote.Path,
				project:     *remote,
				source:      local.Path,
				state:       *state,
			}}
		}
	default:
		panic("jiri: computeOp called with nil local and remote")
	}
}

// This function creates worktree and runs create operation in parallel
func runCreateOperations(jirix *jiri.X, ops []createOperation) MultiError {
	jirix.TimerPush("create operations")
	defer jirix.TimerPop()
	count := len(ops)
	if count == 0 {
		return nil
	}

	type workTree struct {
		// dir is the top level directory in which operations will be performed
		dir string
		// op is an ordered list of operations that must be performed serially,
		// affecting dir
		ops []operation
		// after contains a tree of work that must be performed after ops
		after map[string]*workTree
	}
	head := &workTree{
		dir:   "",
		ops:   []operation{},
		after: make(map[string]*workTree),
	}

	for _, op := range ops {

		node := head
		parts := strings.Split(op.Project().Path, string(filepath.Separator))
		// walk down the file path tree, creating any work tree nodes as required
		for _, part := range parts {
			if part == "" {
				continue
			}
			next, ok := node.after[part]
			if !ok {
				next = &workTree{
					dir:   part,
					ops:   []operation{},
					after: make(map[string]*workTree),
				}
				node.after[part] = next
			}
			node = next
		}
		node.ops = append(node.ops, op)
	}

	workQueue := make(chan *workTree, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	processTree := func(tree *workTree) {
		defer wg.Done()
		for _, op := range tree.ops {
			logMsg := fmt.Sprintf("Creating project %q", op.Project().Name)
			task := jirix.Logger.AddTaskMsg(logMsg)
			jirix.Logger.Debugf("%v", op)
			if err := op.Run(jirix); err != nil {
				task.Done()
				errs <- fmt.Errorf("%s: %s", logMsg, err)
				return
			}
			task.Done()
		}
		for _, v := range tree.after {
			wg.Add(1)
			workQueue <- v
		}
	}
	wg.Add(1)
	workQueue <- head
	for i := uint(0); i < jirix.Jobs; i++ {
		go func() {
			for tree := range workQueue {
				processTree(tree)
			}
		}()
	}
	wg.Wait()
	close(workQueue)
	close(errs)

	var multiErr MultiError
	for err := range errs {
		multiErr = append(multiErr, err)
	}
	return multiErr
}

type PathTrie struct {
	current  string
	children map[string]*PathTrie
}

func NewPathTrie() *PathTrie {
	return &PathTrie{
		current:  "",
		children: make(map[string]*PathTrie),
	}
}
func (p *PathTrie) Contains(path string) bool {
	parts := strings.Split(path, string(filepath.Separator))
	node := p
	for _, part := range parts {
		if part == "" {
			continue
		}
		child, ok := node.children[part]
		if !ok {
			return false
		}
		node = child
	}
	return true
}

func (p *PathTrie) Insert(path string) {
	parts := strings.Split(path, string(filepath.Separator))
	node := p
	for _, part := range parts {
		if part == "" {
			continue
		}
		child, ok := node.children[part]
		if !ok {
			child = &PathTrie{
				current:  part,
				children: make(map[string]*PathTrie),
			}
			node.children[part] = child
		}
		node = child
	}
}

func runDeleteOperations(jirix *jiri.X, ops []deleteOperation, gc bool) error {
	jirix.TimerPush("delete operations")
	defer jirix.TimerPop()
	if len(ops) == 0 {
		return nil
	}
	notDeleted := NewPathTrie()
	if !gc {
		msg := fmt.Sprintf("%d project(s) is/are marked to be deleted. Run '%s' to delete them.", len(ops), jirix.Color.Yellow("jiri update -gc"))
		if jirix.Logger.LoggerLevel < log.DebugLevel {
			msg = fmt.Sprintf("%s\nOr run '%s' or '%s' to see the list of projects.", msg, jirix.Color.Yellow("jiri update -v"), jirix.Color.Yellow("jiri status -d"))
		}
		jirix.Logger.Warningf("%s\n\n", msg)
		if jirix.Logger.LoggerLevel >= log.DebugLevel {
			msg = "List of project(s) marked to be deleted:"
			for _, op := range ops {
				msg = fmt.Sprintf("%s\nName: %s, Path: '%s'", msg, jirix.Color.Yellow(op.project.Name), jirix.Color.Yellow(op.source))
			}
			jirix.Logger.Debugf("%s\n\n", msg)
		}
		return nil
	}
	for _, op := range ops {
		if notDeleted.Contains(op.Project().Path) {
			// not deleting project, add it to trie
			notDeleted.Insert(op.source)
			rmCommand := jirix.Color.Yellow("rm -rf %q", op.source)
			msg := fmt.Sprintf("Project %q won't be deleted because of its sub project(s)", op.project.Name)
			msg += fmt.Sprintf("\nIf you no longer need it, invoke '%s'\n\n", rmCommand)
			jirix.Logger.Warningf(msg)
			continue
		}
		logMsg := fmt.Sprintf("Deleting project %q", op.Project().Name)
		task := jirix.Logger.AddTaskMsg(logMsg)
		jirix.Logger.Debugf("%s", op)
		if err := op.Run(jirix); err != nil {
			task.Done()
			return fmt.Errorf("%s: %s", logMsg, err)
		}
		task.Done()
		if _, err := os.Stat(op.source); err == nil {
			// project not deleted, add it to trie
			notDeleted.Insert(op.source)
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("Checking if %q exists", op.source)
		}
	}
	return nil
}

func runMoveOperations(jirix *jiri.X, ops []moveOperation) error {
	jirix.TimerPush("move operations")
	defer jirix.TimerPop()
	parentSrcPath := ""
	parentDestPath := ""
	for _, op := range ops {
		if parentSrcPath != "" && strings.HasPrefix(op.source, parentSrcPath) {
			op.source = filepath.Join(parentDestPath, strings.Replace(op.source, parentSrcPath, "", 1))
		} else {
			parentSrcPath = op.source
			parentDestPath = op.destination
		}
		logMsg := fmt.Sprintf("Moving and updating project %q", op.Project().Name)
		task := jirix.Logger.AddTaskMsg(logMsg)
		jirix.Logger.Debugf("%s", op)
		if err := op.Run(jirix); err != nil {
			task.Done()
			return fmt.Errorf("%s: %s", logMsg, err)
		}
		task.Done()
	}
	return nil
}

func runCommonOperations(jirix *jiri.X, ops operations, loglevel log.LogLevel) error {
	jirix.TimerPush("common operations")
	defer jirix.TimerPop()
	for _, op := range ops {
		logMsg := fmt.Sprintf("Updating project %q", op.Project().Name)
		task := jirix.Logger.AddTaskMsg(logMsg)
		jirix.Logger.Logf(loglevel, "%s", op)
		if err := op.Run(jirix); err != nil {
			task.Done()
			return fmt.Errorf("%s: %s", logMsg, err)
		}
		task.Done()
	}
	return nil
}
