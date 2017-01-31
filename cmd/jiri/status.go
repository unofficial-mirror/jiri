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
	"fuchsia.googlesource.com/jiri/tool"
)

func init() {
	tool.InitializeProjectFlags(&cmdStatus.Flags)
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

func runStatus(jirix *jiri.X, args []string) error {
	localProjects, err := project.LocalProjects(jirix, project.FastScan)
	if err != nil {
		return err
	}
	remoteProjects, _, _, err := project.LoadUpdatedManifest(jirix, localProjects, true)
	if err != nil {
		return err
	}
	cDir, err := os.Getwd()
	if err != nil {
		return err
	}
	for _, localProject := range localProjects {
		remoteProject, _ := remoteProjects[localProject.Key()]
		if changes, revisionMessage, err := getStatus(jirix, localProject, remoteProject); err != nil {
			return err
		} else {
			if changes != "" || revisionMessage != "" {
				relativePath, err := filepath.Rel(cDir, localProject.Path)
				if err != nil {
					return err
				}
				fmt.Printf("%v(%v): %v", localProject.Name, relativePath, revisionMessage)
				fmt.Println()
				if changes != "" {
					fmt.Println(changes)
				}
				fmt.Println()
			}
		}
	}
	return nil
}

func getStatus(jirix *jiri.X, local project.Project, remote project.Project) (string, string, error) {
	revisionMessage := ""
	git := gitutil.New(jirix.NewSeq(), gitutil.RootDirOpt(local.Path))
	changes, err := git.ShortStatus()
	if err != nil {
		return "", "", err
	}
	if remote.Name != "" {
		if expectedRev, err := project.GetHeadRevision(jirix, remote); err != nil {
			return "", "", err
		} else {
			if expectedRev, err = git.CurrentRevisionOfBranch(expectedRev); err != nil {
				return "", "", err
			}
			if currentRev, err := git.CurrentRevision(); err != nil {
				return "", "", err
			} else if expectedRev != currentRev {
				revisionMessage = fmt.Sprintf("Should be on revision %q, but is on revision %q", expectedRev, currentRev)
			}
		}
	} else {
		revisionMessage = "Can't find project in manifest, can't get revision status"
	}
	return changes, revisionMessage, nil
}
