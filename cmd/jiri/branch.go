// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/project"
)

var branchFlags struct {
	deleteFlag                bool
	forceDeleteFlag           bool
	listFlag                  bool
	overrideProjectConfigFlag bool
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
	if !branchFlags.deleteFlag && !branchFlags.forceDeleteFlag {
		return displayProjects(jirix, branch)
	}
	if branch == "" {
		return jirix.UsageErrorf("Please provide branch to delete")
	}
	return deleteBranches(jirix, branch)
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
	jirix.TimerPush("Get states")
	states, err := project.GetProjectStates(jirix, localProjects, false)
	if err != nil {
		return err
	}

	jirix.TimerPop()
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
				git := gitutil.New(jirix, gitutil.RootDirOpt(localProject.Path))

				if err := git.DeleteBranch(branchToDelete, gitutil.ForceOpt(branchFlags.forceDeleteFlag)); err != nil {
					errors = true
					fmt.Printf(jirix.Color.Red("Error while deleting branch: %s\n", err))
				} else {
					shortHash, err := git.GetShortHash(branch.Revision)
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
