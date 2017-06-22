// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/gerrit"
	"fuchsia.googlesource.com/jiri/git"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/project"
)

var branchFlags struct {
	deleteFlag                bool
	deleteMergedClsFlag       bool
	deleteMergedFlag          bool
	forceDeleteFlag           bool
	listFlag                  bool
	overrideProjectConfigFlag bool
}

type MultiError []error

func (m MultiError) Error() string {
	s := []string{}
	for _, e := range m {
		if e != nil {
			s = append(s, e.Error())
		}
	}
	return strings.Join(s, "\n")
}

func (m MultiError) String() string {
	return m.Error()
}

var cmdBranch = &cmdline.Command{
	Runner: jiri.RunnerFunc(runBranch),
	Name:   "branch",
	Short:  "Show or delete branches",
	Long: `
Show all the projects having branch <branch> .If -d or -D is passed, <branch>
is deleted. if <branch> is not passed, show all projects which have branches other than "master"`,
	ArgsName: "<branch>",
	ArgsLong: "<branch> is the name branch",
}

func init() {
	flags := &cmdBranch.Flags
	flags.BoolVar(&branchFlags.deleteFlag, "d", false, "Delete branch from project. Similar to running 'git branch -d <branch-name>'")
	flags.BoolVar(&branchFlags.forceDeleteFlag, "D", false, "Force delete branch from project. Similar to running 'git branch -D <branch-name>'")
	flags.BoolVar(&branchFlags.listFlag, "list", false, "Show only projects with current branch <branch>")
	flags.BoolVar(&branchFlags.overrideProjectConfigFlag, "override-pc", false, "Overrrides project config's ignore and noupdate flag and deletes the branch.")
	flags.BoolVar(&branchFlags.deleteMergedFlag, "delete-merged", false, "Delete merged branches. Merged branches are the tracked branches merged with their tracking remote or un-tracked branches merged with the branch specified in manifest(default master). If <branch> is provided, it will only delete branch <branch> if merged.")
	flags.BoolVar(&branchFlags.deleteMergedClsFlag, "delete-merged-cl", false, "Implies -delete-merged. It also parses commit messages for ChangeID and checks with gerrit if those changes have been merged and deletes those branches. It will ignore a branch if it differs with remote by more than 10 commits.")
}

func displayProjects(jirix *jiri.X, branch string) error {
	localProjects, err := project.LocalProjects(jirix, project.FastScan)
	if err != nil {
		return err
	}
	jirix.TimerPush("Get states")
	states, err := project.GetProjectStates(jirix, localProjects, false)
	if err != nil {
		return err
	}

	jirix.TimerPop()
	cDir, err := os.Getwd()
	if err != nil {
		return err
	}
	var keys project.ProjectKeys
	for key, _ := range states {
		keys = append(keys, key)
	}
	sort.Sort(keys)
	for _, key := range keys {
		state := states[key]
		relativePath, err := filepath.Rel(cDir, state.Project.Path)
		if err != nil {
			return err
		}
		if branch == "" {
			var branches []string
			master := ""
			for _, b := range state.Branches {
				name := b.Name
				if state.CurrentBranch.Name == b.Name {
					name = "*" + jirix.Color.Green("%s", b.Name)
				}
				if b.Name != "master" {
					branches = append(branches, name)
				} else {
					master = name
				}
			}
			if len(branches) != 0 {
				if master != "" {
					branches = append(branches, master)
				}
				fmt.Printf("%s: %s(%s)\n", jirix.Color.Yellow("Project"), state.Project.Name, relativePath)
				fmt.Printf("%s: %s\n\n", jirix.Color.Yellow("Branch(es)"), strings.Join(branches, ", "))
			}

		} else if branchFlags.listFlag {
			if state.CurrentBranch.Name == branch {
				fmt.Printf("%s(%s)\n", state.Project.Name, relativePath)
			}
		} else {
			for _, b := range state.Branches {
				if b.Name == branch {
					fmt.Printf("%s(%s)\n", state.Project.Name, relativePath)
					break
				}
			}
		}
	}
	jirix.TimerPop()
	return nil
}

func runBranch(jirix *jiri.X, args []string) error {
	branch := ""
	if len(args) > 1 {
		return jirix.UsageErrorf("Please provide only one branch")
	} else if len(args) == 1 {
		branch = args[0]
	}
	if branchFlags.deleteFlag || branchFlags.forceDeleteFlag {
		if branch == "" {
			return jirix.UsageErrorf("Please provide branch to delete")
		}
		return deleteBranches(jirix, branch)
	}
	if branchFlags.deleteMergedClsFlag {
		return deleteMergedBranches(jirix, branch, true)
	}
	if branchFlags.deleteMergedFlag {
		return deleteMergedBranches(jirix, branch, false)
	}
	return displayProjects(jirix, branch)
}

var (
	changeIDRE = regexp.MustCompile("Change-Id: (I[0123456789abcdefABCDEF]{40})")
)

func deleteMergedBranches(jirix *jiri.X, branchToDelete string, deleteMergedCls bool) error {
	localProjects, err := project.LocalProjects(jirix, project.FastScan)
	if err != nil {
		return err
	}

	cDir, err := os.Getwd()
	if err != nil {
		return err
	}

	jirix.TimerPush("Get states")
	states, err := project.GetProjectStates(jirix, localProjects, false)
	if err != nil {
		return err
	}
	jirix.TimerPop()

	remoteProjects, _, err := project.LoadManifestFile(jirix, jirix.JiriManifestFile(), localProjects, false /*localManifest*/)
	if err != nil {
		return err
	}

	jirix.TimerPush("Process")
	processProject := func(key project.ProjectKey) {
		state, _ := states[key]
		remote, ok := remoteProjects[key]
		relativePath, err := filepath.Rel(cDir, state.Project.Path)
		if err != nil {
			relativePath = state.Project.Path
		}
		if !branchFlags.overrideProjectConfigFlag && (state.Project.LocalConfig.Ignore || state.Project.LocalConfig.NoUpdate) {
			jirix.Logger.Warningf(" Not processing project %s(%s) due to it's local-config. Use '-overrride-pc' flag\n\n", state.Project.Name, state.Project.Path)
			return
		}
		if !ok {
			jirix.Logger.Debugf("Not processing project %s(%s) as it was not found in manifest\n\n", state.Project.Name, relativePath)
			return
		}

		deletedBranches, mErr := deleteProjectMergedBranches(jirix, state.Project, remote, relativePath, branchToDelete)
		if deleteMergedCls {
			deletedBranches2, err2 := deleteProjectMergedClsBranches(jirix, state.Project, remote, relativePath, branchToDelete)
			for b, h := range deletedBranches2 {
				deletedBranches[b] = h
			}
			mErr = append(mErr, err2...)
		}

		if len(deletedBranches) != 0 || mErr != nil {
			buf := fmt.Sprintf("Project: %s(%s)\n", state.Project.Name, relativePath)
			if len(deletedBranches) != 0 {
				dbs := []string{}
				for b, h := range deletedBranches {
					dbs = append(dbs, fmt.Sprintf("%s(%s)", b, h))
				}
				buf = buf + fmt.Sprintf("%s: %s\n", jirix.Color.Green("Deleted branch(es)"), strings.Join(dbs, ", "))

				if _, ok := deletedBranches[state.CurrentBranch.Name]; ok {
					buf = buf + fmt.Sprintf("Current branch \"%s\" was deleted and project was put on JIRI_HEAD\n", jirix.Color.Yellow(state.CurrentBranch.Name))
				}
			}
			if mErr != nil {
				jirix.IncrementFailures()
				buf = buf + fmt.Sprintf("%s\n", mErr)
				jirix.Logger.Errorf("%s\n", buf)
			} else {
				jirix.Logger.Infof("%s\n", buf)
			}
		}
	}

	workQueue := make(chan project.ProjectKey, len(states))
	for key, _ := range states {
		workQueue <- key
	}
	close(workQueue)

	var wg sync.WaitGroup
	for i := uint(0); i < jirix.Jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range workQueue {
				processProject(key)
			}
		}()
	}

	wg.Wait()
	jirix.TimerPop()

	if jirix.Failures() != 0 {
		return fmt.Errorf("Branch deletion completed with non-fatal errors.")
	}
	return nil
}

func deleteProjectMergedClsBranches(jirix *jiri.X, local project.Project, remote project.Project, relativePath, branchToDelete string) (map[string]string, MultiError) {
	deletedBranches := make(map[string]string)
	var retErr MultiError
	if remote.GerritHost == "" {
		return nil, nil
	}
	hostUrl, err := url.Parse(remote.GerritHost)
	if err != nil {
		retErr = append(retErr, err)
		return nil, retErr
	}
	gerrit := gerrit.New(jirix, hostUrl)
	scm := gitutil.New(jirix, gitutil.RootDirOpt(local.Path))
	g := git.NewGit(local.Path)
	branches, err := g.GetAllBranchesInfo()
	if err != nil {
		retErr = append(retErr, err)
		return nil, retErr
	}
	for _, b := range branches {
		if branchToDelete != "" && b.Name != branchToDelete {
			continue
		}
		if b.IsHead {
			untracked, err := g.HasUntrackedFiles()
			if err != nil {
				retErr = append(retErr, fmt.Errorf("Not deleting current branch %q as can't get changes: %s\n", b.Name, err))
				continue
			}
			uncommited, err := g.HasUncommittedChanges()
			if err != nil {
				retErr = append(retErr, fmt.Errorf("Not deleting current branch %q as can't get changes: %s\n", b.Name, err))
				continue
			}
			if untracked || uncommited {
				jirix.Logger.Debugf("Not deleting current branch %q for project %s(%s) as it has changes\n\n", b.Name, local.Name, relativePath)
				continue
			}
		}

		trackingBranch := ""
		if b.Tracking == nil {
			rb := remote.RemoteBranch
			if rb == "" {
				rb = "master"
			}
			trackingBranch = fmt.Sprintf("remotes/origin/%s", rb)
		} else {
			trackingBranch = b.Tracking.Name
		}

		extraCommits, err := scm.ExtraCommits(b.Name, trackingBranch)
		if err != nil {
			retErr = append(retErr, fmt.Errorf("Not deleting branch %q as can't get extra commits: %s\n", b.Name, err))
			continue
		}

		if len(extraCommits) > 10 {
			jirix.Logger.Debugf("Not deleting branch %q for project %s(%s) as it has more than 10 extra commits\n\n", b.Name, local.Name, relativePath)
			continue
		}

		deleteBranch := true
		for _, c := range extraCommits {
			deleteBranch = false
			log, err := g.CommitMsg(c)
			if err != nil {
				retErr = append(retErr, fmt.Errorf("Not deleting branch %q as can't get log for rev %q: %s\n", b.Name, c, err))
				break
			}
			changeID := changeIDRE.FindStringSubmatch(log)
			if len(changeID) != 2 {
				// Invalid/No Changeid
				break
			}
			c, err := gerrit.GetChangeByID(changeID[1])
			if err != nil {
				retErr = append(retErr, fmt.Errorf("Not deleting branch %q as can't get change %q: %s\n", b.Name, changeID[1], err))
				break
			}
			if c == nil || c.Submitted == "" {
				// Not merged
				break
			}
			deleteBranch = true
		}
		if !deleteBranch {
			continue
		}

		if b.IsHead {
			revision, err := project.GetHeadRevision(jirix, remote)
			if err != nil {
				retErr = append(retErr, fmt.Errorf("Not deleting current branch %q as can't get head revision: %s\n", b.Name, err))
				continue
			}
			if err := scm.CheckoutBranch(revision, gitutil.DetachOpt(true)); err != nil {
				retErr = append(retErr, fmt.Errorf("Not deleting current branch %q as can't checkout JIRI_HEAD: %s\n", b.Name, err))
				continue
			}
		}

		shortHash, err := g.ShortHash(b.Revision)
		if err != nil {
			retErr = append(retErr, fmt.Errorf("Not deleting current branch %q as can't short hash: %s\n", b.Name, err))
			continue
		}
		if err := scm.DeleteBranch(b.Name, gitutil.ForceOpt(true)); err != nil {
			retErr = append(retErr, fmt.Errorf("Cannot delete branch %q: %s\n", b.Name, err))
			if b.IsHead {
				if err := scm.CheckoutBranch(b.Name); err != nil {
					retErr = append(retErr, fmt.Errorf("Not able to put project back on branch %q: %s\n", b.Name, err))
				}
			}
			continue
		}
		deletedBranches[b.Name] = shortHash
	}
	return deletedBranches, retErr
}

func deleteProjectMergedBranches(jirix *jiri.X, local project.Project, remote project.Project, relativePath, branchToDelete string) (map[string]string, MultiError) {
	deletedBranches := make(map[string]string)
	var retErr MultiError
	var mergedBranches map[string]bool
	scm := gitutil.New(jirix, gitutil.RootDirOpt(local.Path))
	g := git.NewGit(local.Path)
	branches, err := g.GetAllBranchesInfo()
	if err != nil {
		retErr = append(retErr, err)
		return nil, retErr
	}
	for _, b := range branches {
		if branchToDelete != "" && b.Name != branchToDelete {
			continue
		}
		deleteForced := false

		if b.Tracking == nil {
			// check if this branch is merged
			if mergedBranches == nil {
				// populate
				mergedBranches = make(map[string]bool)
				rb := remote.RemoteBranch
				if rb == "" {
					rb = "master"
				}
				if mbs, err := g.MergedBranches("remotes/origin/" + rb); err != nil {
					retErr = append(retErr, fmt.Errorf("Not able to get merged un-tracked branches: %s\n", err))
					continue
				} else {
					for _, mb := range mbs {
						mergedBranches[mb] = true
					}
				}
			}
			if !mergedBranches[b.Name] {
				continue
			}
			deleteForced = true
		}

		if b.IsHead {
			untracked, err := g.HasUntrackedFiles()
			if err != nil {
				retErr = append(retErr, fmt.Errorf("Not deleting current branch %q as can't get changes: %s\n", b.Name, err))
				continue
			}
			uncommited, err := g.HasUncommittedChanges()
			if err != nil {
				retErr = append(retErr, fmt.Errorf("Not deleting current branch %q as can't get changes: %s\n", b.Name, err))
				continue
			}
			if untracked || uncommited {
				jirix.Logger.Debugf("Not deleting current branch %q for project %s(%s) as it has changes\n\n", b.Name, local.Name, relativePath)
				continue
			}
			revision, err := project.GetHeadRevision(jirix, remote)
			if err != nil {
				retErr = append(retErr, fmt.Errorf("Not deleting current branch %q as can't get head revision: %s\n", b.Name, err))
				continue
			}
			if err := scm.CheckoutBranch(revision, gitutil.DetachOpt(true)); err != nil {
				retErr = append(retErr, fmt.Errorf("Not deleting current branch %q as can't checkout JIRI_HEAD: %s\n", b.Name, err))
				continue
			}
		}

		shortHash, err := g.ShortHash(b.Revision)
		if err != nil {
			retErr = append(retErr, fmt.Errorf("Not deleting current branch %q as can't short hash: %s\n", b.Name, err))
			continue
		}
		if err := scm.DeleteBranch(b.Name, gitutil.ForceOpt(deleteForced)); err != nil {
			if deleteForced {
				retErr = append(retErr, fmt.Errorf("Cannot delete branch %q: %s\n", b.Name, err))
			}
			if b.IsHead {
				if err := scm.CheckoutBranch(b.Name); err != nil {
					retErr = append(retErr, fmt.Errorf("Not able to put project back on branch %q: %s\n", b.Name, err))
				}
			}
			continue
		}
		deletedBranches[b.Name] = shortHash
	}
	return deletedBranches, retErr
}

func deleteBranches(jirix *jiri.X, branchToDelete string) error {
	localProjects, err := project.LocalProjects(jirix, project.FastScan)
	if err != nil {
		return err
	}
	cDir, err := os.Getwd()
	if err != nil {
		return err
	}
	states, err := project.GetProjectStates(jirix, localProjects, false)
	if err != nil {
		return err
	}

	jirix.TimerPush("Process")
	errors := false
	projectFound := false
	var keys project.ProjectKeys
	for key, _ := range states {
		keys = append(keys, key)
	}
	sort.Sort(keys)
	for _, key := range keys {
		state := states[key]
		for _, branch := range state.Branches {
			if branch.Name == branchToDelete {
				projectFound = true
				localProject := state.Project
				relativePath, err := filepath.Rel(cDir, localProject.Path)
				if err != nil {
					return err
				}
				if !branchFlags.overrideProjectConfigFlag && (localProject.LocalConfig.Ignore || localProject.LocalConfig.NoUpdate) {
					jirix.Logger.Warningf("Project %s(%s): branch %q won't be deleted due to it's local-config. Use '-overrride-pc' flag\n\n", localProject.Name, localProject.Path, branchToDelete)
					break
				}
				fmt.Printf("Project %s(%s): ", localProject.Name, relativePath)
				scm := gitutil.New(jirix, gitutil.RootDirOpt(localProject.Path))

				if err := scm.DeleteBranch(branchToDelete, gitutil.ForceOpt(branchFlags.forceDeleteFlag)); err != nil {
					errors = true
					fmt.Printf(jirix.Color.Red("Error while deleting branch: %s\n", err))
				} else {
					shortHash, err := scm.GetShortHash(branch.Revision)
					if err != nil {
						return err
					}
					fmt.Printf("%s (was %s)\n", jirix.Color.Green("Deleted Branch %s", branchToDelete), jirix.Color.Yellow(shortHash))
				}
				break
			}
		}
	}
	jirix.TimerPop()

	if !projectFound {
		fmt.Printf("Cannot find any project with branch %q\n", branchToDelete)
		return nil
	}
	if errors {
		fmt.Println(jirix.Color.Yellow("Please check errors above"))
	}
	return nil
}
