// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/project"
)

var branchFlags struct {
	deleteFlag      bool
	forceDeleteFlag bool
	listFlag     bool
}

var cmdBranch = &cmdline.Command{
	Runner: jiri.RunnerFunc(runBranch),
	Name:   "branch",
	Short:  "Show or delete branches",
	Long: `
Show all the projects having branch <branch> .If -d or -D is passed, <branch>
is deleted `,
	ArgsName: "<branch>",
	ArgsLong: "<branch> is the name branch",
}

func init() {
	flags := &cmdBranch.Flags
	flags.BoolVar(&branchFlags.deleteFlag, "d", false, "Delete branch from project. Similar to running 'git branch -d <branch-name>'")
	flags.BoolVar(&branchFlags.forceDeleteFlag, "D", false, "Force delete branch from project. Similar to running 'git branch -D <branch-name>'")
	flags.BoolVar(&branchFlags.listFlag, "list", false, "Show only projects with current branch <branch>")
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
	foundProj := false
	cDir, err := os.Getwd()
	if err != nil {
		return err
	}
	for _, state := range states {
		relativePath, err := filepath.Rel(cDir, state.Project.Path)
		if err != nil {
			return err
		}
		if branchFlags.listFlag {
			if state.CurrentBranch.Name == branch {
				fmt.Printf("%s(%s)\n", state.Project.Name, relativePath)
				foundProj = true
			}
		} else {
			for _, b := range state.Branches {
				if b.Name == branch {
					fmt.Printf("%s(%s)\n", state.Project.Name, relativePath)
					foundProj = true
					break
				}
			}
		}
	}
	jirix.TimerPop()

	if !foundProj {
		fmt.Println(jirix.Color.Red("Cannot find any project with branch %q\n", branch))
	}
	return nil
}

func runBranch(jirix *jiri.X, args []string) error {
	if len(args) == 0 {
		return jirix.UsageErrorf("Please specify branch")
	}
	if len(args) > 1 {
		return jirix.UsageErrorf("Please provide only one branch")
	}
	if !branchFlags.deleteFlag && !branchFlags.forceDeleteFlag {
		return displayProjects(jirix, args[0])
	}
	return deleteBranches(jirix, args[0])
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
	projectMap := make(project.Projects)
	jirix.TimerPush("Build Map")
	for key, state := range states {
		for _, branch := range state.Branches {
			if branch.Name == branchToDelete {
				projectMap[key] = state.Project
				break
			}
		}
	}
	jirix.TimerPop()

	if len(projectMap) == 0 {
		fmt.Printf("Cannot find any project with branch %q\n", branchToDelete)
		return nil
	}

	jirix.TimerPush("Process")
	errors := false
	for _, localProject := range projectMap {
		relativePath, err := filepath.Rel(cDir, localProject.Path)
		if err != nil {
			return err
		}
		fmt.Printf("Project %s(%s): ", localProject.Name, relativePath)
		git := gitutil.New(jirix, gitutil.RootDirOpt(localProject.Path))
		if err := git.DeleteBranch(branchToDelete, gitutil.ForceOpt(branchFlags.forceDeleteFlag)); err != nil {
			errors = true
			fmt.Printf(jirix.Color.Red("Error while deleting branch: %s\n", err))
		} else {
			fmt.Printf(jirix.Color.Green("Branch deleted\n"))
		}
	}
	jirix.TimerPop()

	if errors {
		fmt.Println(jirix.Color.Yellow("Please check errors above"))
	}
	return nil
}
