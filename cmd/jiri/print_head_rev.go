// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/project"
	"fuchsia.googlesource.com/jiri/tool"
)

func init() {
	tool.InitializeProjectFlags(&cmdPrintHeadRev.Flags)
}

var cmdPrintHeadRev = &cmdline.Command{
	Runner: jiri.RunnerFunc(runPrintHeadRev),
	Name:   "print-head-rev",
	Short:  "Print head revision for current project",
	Long: `
Prints head revision for current project which can be pinned in manifest or actual latest revision.
`,
}

func runPrintHeadRev(jirix *jiri.X, args []string) error {
	if headRev, err := getHeadRev(jirix); err != nil {
		return err
	} else {
		fmt.Println(headRev)
	}
	return nil
}

func getHeadRev(jirix *jiri.X) (string, error) {
	p, err := currentProject(jirix)
	if err != nil {
		return "", err
	}
	localProjects, err := project.LocalProjects(jirix, project.FastScan)
	if err != nil {
		return "", err
	}
	remoteProjects, _, _, err := project.LoadUpdatedManifest(jirix, localProjects, true)
	if err != nil {
		return "", err
	}
	if remoteProject, ok := remoteProjects[p.Key()]; !ok {
		return "", fmt.Errorf("Project %q not found in manifest", p.Name)
	} else {
		if headRevision, err := project.GetHeadRevision(jirix, remoteProject); err != nil {
			return "", err
		} else {
			git := gitutil.New(jirix.NewSeq(), gitutil.RootDirOpt(p.Path))
			if headRevision, err = git.CurrentRevisionOfBranch(headRevision); err != nil {
				return "", err
			}
			return headRevision, nil
		}
	}
}
