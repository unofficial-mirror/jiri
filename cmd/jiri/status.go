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

var statusFlags struct {
	changes   bool
	checkHead bool
	branch    string
	commits   bool
	deleted   bool
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
	flags.StringVar(&statusFlags.branch, "branch", "", "Display all projects only on this branch along with their status.")
	flags.BoolVar(&statusFlags.deleted, "deleted", false, "List all deleted projects. Other flags would be ignored.")
	flags.BoolVar(&statusFlags.deleted, "d", false, "Same as -deleted.")
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
	if statusFlags.deleted {
		for key, localProject := range localProjects {
			if _, remoteOk := remoteProjects[key]; !remoteOk {
				relativePath, err := filepath.Rel(cDir, localProject.Path)
				if err != nil {
					return err
				}
				fmt.Printf("Name: '%s', Path: '%s'\n", jirix.Color.Red(localProject.Name), jirix.Color.Red(relativePath))
			}
			continue
		}
		return nil
	}
	states, err := project.GetProjectStates(jirix, localProjects, false)
	if err != nil {
		return err
	}
	var keys project.ProjectKeys
	for key, _ := range localProjects {
		keys = append(keys, key)
	}
	sort.Sort(keys)
	deletedProjects := 0
	for _, key := range keys {
		localProject := localProjects[key]
		remoteProject, foundRemote := remoteProjects[key]
		if !foundRemote {
			deletedProjects++
			continue
		}
		state, ok := states[key]
		if !ok {
			// this should not happen
			panic(fmt.Sprintf("State not found for project %q", localProject.Name))
		}
		if statusFlags.branch != "" && (statusFlags.branch != state.CurrentBranch.Name) {
			continue
		}
		relativePath, err := filepath.Rel(cDir, localProject.Path)
		if err != nil {
			return err
		}
		errorMsg := fmt.Sprintf("getting status for project %s(%s)", localProject.Name, relativePath)
		changes, headRev, extraCommits, err := getStatus(jirix, localProject, remoteProject, state.CurrentBranch)
		if err != nil {
			jirix.Logger.Errorf("%s :%s\n\n", errorMsg, err)
			jirix.IncrementFailures()
			continue
		}
		revisionMessage := ""
		git := gitutil.New(jirix, gitutil.RootDirOpt(state.Project.Path))
		currentLog, err := git.OneLineLog(state.CurrentBranch.Revision)
		if err != nil {
			jirix.Logger.Errorf("%s :%s\n\n", errorMsg, err)
			jirix.IncrementFailures()
			continue
		}
		currentLog = colorFormatGitLog(jirix, currentLog)
		if statusFlags.checkHead {
			if headRev != state.CurrentBranch.Revision {
				headLog, err := git.OneLineLog(headRev)
				if err != nil {
					jirix.Logger.Errorf("%s :%s\n\n", errorMsg, err)
					jirix.IncrementFailures()
					continue
				}
				headLog = colorFormatGitLog(jirix, headLog)
				revisionMessage = fmt.Sprintf("\n%s: %s", jirix.Color.Yellow("JIRI_HEAD"), headLog)
				revisionMessage = fmt.Sprintf("%s\n%s: %s", revisionMessage, jirix.Color.Yellow("Current Revision"), currentLog)
			}
		}
		if statusFlags.branch != "" || changes != "" || revisionMessage != "" ||
			len(extraCommits) != 0 {
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
	if deletedProjects != 0 {
		jirix.Logger.Warningf("Found %d deleted project(s), run with -d flag to list them.\n\n", deletedProjects)
	}
	if jirix.Failures() != 0 {
		return fmt.Errorf("completed with non-fatal errors")
	}
	return nil
}

func getStatus(jirix *jiri.X, local project.Project, remote project.Project, currentBranch project.BranchState) (string, string, []string, error) {
	var extraCommits []string
	headRev := ""
	changes := ""
	scm := gitutil.New(jirix, gitutil.RootDirOpt(local.Path))
	var err error
	if statusFlags.changes {
		changes, err = scm.ShortStatus()
		if err != nil {
			return "", "", nil, err
		}
	}
	if statusFlags.checkHead && remote.Name != "" {
		// try getting JIRI_HEAD first
		if r, err := scm.CurrentRevisionForRef("JIRI_HEAD"); err == nil {
			headRev = r
		} else {
			headRev, err = project.GetHeadRevision(jirix, remote)
			if err != nil {
				return "", "", nil, err
			}
			if r, err := scm.CurrentRevisionForRef(headRev); err != nil {
				return "", "", nil, fmt.Errorf("Cannot find revision for ref %q for project %q: %s", headRev, local.Name, err)
			} else {
				headRev = r
			}
		}
	}

	if currentBranch.Name != "" && statusFlags.commits {
		remoteBranch := "remotes/origin/" + remote.RemoteBranch
		if currentBranch.Tracking != nil {
			remoteBranch = currentBranch.Tracking.Name
		}
		commits, err := scm.ExtraCommits(currentBranch.Name, remoteBranch)
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
