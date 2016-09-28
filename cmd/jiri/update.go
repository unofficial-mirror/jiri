// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/project"
	"fuchsia.googlesource.com/jiri/retry"
	"fuchsia.googlesource.com/jiri/tool"
)

var (
	gcFlag              bool
	attemptsFlag        int
	autoupdateFlag      bool
	forceAutoupdateFlag bool
	verboseUpdateFlag   bool
)

func init() {
	tool.InitializeProjectFlags(&cmdUpdate.Flags)

	cmdUpdate.Flags.BoolVar(&gcFlag, "gc", false, "Garbage collect obsolete repositories.")
	cmdUpdate.Flags.BoolVar(&verboseUpdateFlag, "verbose", false, "Show all update logs.")
	cmdUpdate.Flags.IntVar(&attemptsFlag, "attempts", 1, "Number of attempts before failing.")
	cmdUpdate.Flags.BoolVar(&autoupdateFlag, "autoupdate", true, "Automatically update to the new version.")
	cmdUpdate.Flags.BoolVar(&forceAutoupdateFlag, "force-autoupdate", false, "Always update to the current version.")
}

// cmdUpdate represents the "jiri update" command.
var cmdUpdate = &cmdline.Command{
	Runner: jiri.RunnerFunc(runUpdate),
	Name:   "update",
	Short:  "Update all jiri projects",
	Long: `
Updates all projects. The sequence in which the individual updates happen
guarantees that we end up with a consistent workspace. The set of projects
to update is described in the manifest.

Run "jiri help manifest" for details on manifests.
`,
}

func runUpdate(jirix *jiri.X, _ []string) error {
	if autoupdateFlag {
		// Try to update Jiri itself.
		err := jiri.Update(forceAutoupdateFlag)
		if err != nil {
			fmt.Printf("warning: automatic update failed: %v\n", err)
		}
	}

	// Update all projects to their latest version.
	// Attempt <attemptsFlag> times before failing.
	updateFn := func() error { return project.UpdateUniverse(jirix, gcFlag, verboseUpdateFlag) }
	if err := retry.Function(jirix.Context, updateFn, retry.AttemptsOpt(attemptsFlag)); err != nil {
		return err
	}
	return project.WriteUpdateHistorySnapshot(jirix, "")
}
