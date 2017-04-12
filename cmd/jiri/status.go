// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/git"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/project"
)

var statusFlags struct {
	changes   bool
	checkHead bool
	branch    string
	commits   bool
}

var cmdStatus = &cmdline.Command{
	Runner: jiri.RunnerFunc(runStatus),
	Name:   "status",
	Short:  "Prints status of all the projects",
	Long: `
Prints status for the the projects. It runs git status -s across all the projects
and prints it if there are some changes. It also shows status if the project is on
a rev other then the one according to manifest(Named as JIRI_HEAD in git)
`,
}

func init() {
	flags := &cmdStatus.Flags
	flags.BoolVar(&statusFlags.changes, "changes", true, "Display projects with tracked or un-tracked changes.")
	flags.BoolVar(&statusFlags.checkHead, "check-head", true, "Display projects that are not on HEAD/pinned revisions.")
	flags.BoolVar(&statusFlags.commits, "commits", true, "Display commits not merged with remote. This only works when project is on a local branch.")
	flags.StringVar(&statusFlags.branch, "branch", "", "Display all projects only on this branch along with thier status.")
}

func colorFormatGitLog(jirix *jiri.X, log string) string {
	strs := strings.SplitN(log, " ", 2)
	strs[0] = jirix.Color.Green(strs[0])
	return strings.Join(strs, " ")
}

func colorFormatGitiStatusLog(jirix *jiri.X, log string) string {
	strs := strings.SplitN(log, " ", 2)
	strs[0] = jirix.Color.Red(strs[0])
	return strings.Join(strs, " ")
}

func runStatus(jirix *jiri.X, args []string) error {
	localProjects, err := project.LocalProjects(jirix, project.FastScan)
	if err != nil {
		return err
	}
	remoteProjects, _, err := project.LoadManifestFile(jirix, jirix.JiriManifestFile(), localProjects, false /*localManifest*/)
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
	for key, localProject := range localProjects {
		remoteProject, _ := remoteProjects[key]
		state, ok := states[key]
		if !ok {
			// this should not happen
			panic(fmt.Sprintf("State not found for project %q", localProject.Name))
		}
		if statusFlags.branch != "" && (statusFlags.branch != state.CurrentBranch.Name) {
			continue
		}
		changes, headRev, extraCommits, err := getStatus(jirix, localProject, remoteProject, state.CurrentBranch.Name)
		if err != nil {
			return fmt.Errorf("Error while getting status for project %q :%s", localProject.Name, err)
		}
		revisionMessage := ""
		git := gitutil.New(jirix, gitutil.RootDirOpt(state.Project.Path))
		currentLog, err := git.OneLineLog(state.CurrentBranch.Revision)
		if err != nil {
			return fmt.Errorf("Error while getting status for project %q :%s", localProject.Name, err)
		}
		currentLog = colorFormatGitLog(jirix, currentLog)
		if statusFlags.checkHead {
			if headRev == "" {
				revisionMessage = "Can't find project in manifest, can't get revision status"
			} else if headRev != state.CurrentBranch.Revision {
				headLog, err := git.OneLineLog(headRev)
				if err != nil {
					return fmt.Errorf("Error while getting status for project %q :%s", localProject.Name, err)
				}
				headLog = colorFormatGitLog(jirix, headLog)
				revisionMessage = fmt.Sprintf("\n%s: %s", jirix.Color.Yellow("JIRI_HEAD"), headLog)
				revisionMessage = fmt.Sprintf("%s\n%s: %s", revisionMessage, jirix.Color.Yellow("Current Revision"), currentLog)
			}
		}
		if statusFlags.branch != "" || changes != "" || revisionMessage != "" ||
			len(extraCommits) != 0 {
			relativePath, err := filepath.Rel(cDir, localProject.Path)
			if err != nil {
				return err
			}
			fmt.Printf("%s: %s", jirix.Color.Yellow(relativePath), revisionMessage)
			fmt.Println()
			branch := state.CurrentBranch.Name
			if branch == "" {
				branch = fmt.Sprintf("DETACHED-HEAD(%s)", currentLog)
			}
			fmt.Printf("%s: %s\n", jirix.Color.Yellow("Branch"), branch)
			if len(extraCommits) != 0 {
				fmt.Printf("%s: %d commit(s) not merged to remote\n", jirix.Color.Yellow("Commits"), len(extraCommits))
				for _, commitLog := range extraCommits {
					fmt.Println(colorFormatGitLog(jirix, commitLog))
				}
			}
			if changes != "" {
				changesArr := strings.Split(changes, "\n")
				for _, change := range changesArr {
					fmt.Println(colorFormatGitiStatusLog(jirix, change))
				}
			}
			fmt.Println()
		}

	}
	return nil
}

func getStatus(jirix *jiri.X, local project.Project, remote project.Project, currentBranch string) (string, string, []string, error) {
	var extraCommits []string
	headRev := ""
	changes := ""
	scm := gitutil.New(jirix, gitutil.RootDirOpt(local.Path))
	g := git.NewGit(local.Path)
	var err error
	if statusFlags.changes {
		changes, err = scm.ShortStatus()
		if err != nil {
			return "", "", nil, err
		}
	}
	if statusFlags.checkHead && remote.Name != "" {
		headRev, err = project.GetHeadRevision(jirix, remote)
		if err != nil {
			return "", "", nil, err
		}
		if headRev, err = g.CurrentRevisionForRef(headRev); err != nil {
			return "", "", nil, fmt.Errorf("Cannot find revision for ref %q for project %q: %s", headRev, local.Name, err)
		}
	}

	if currentBranch != "" && statusFlags.commits {
		commits, err := scm.ExtraCommits(statusFlags.branch, "origin")
		if err != nil {
			return "", "", nil, err
		}
		for _, commit := range commits {
			log, err := scm.OneLineLog(commit)
			if err != nil {
				return "", "", nil, err
			}
			extraCommits = append(extraCommits, log)

		}

	}
	return changes, headRev, extraCommits, nil
}
