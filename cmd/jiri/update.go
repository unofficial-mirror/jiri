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
	localManifestFlag   bool
	attemptsFlag        int
	autoupdateFlag      bool
	forceAutoupdateFlag bool
	verboseUpdateFlag   bool
	rebaseUntrackedFlag bool
)

func init() {
	tool.InitializeProjectFlags(&cmdUpdate.Flags)

	cmdUpdate.Flags.BoolVar(&gcFlag, "gc", false, "Garbage collect obsolete repositories.")
	cmdUpdate.Flags.BoolVar(&localManifestFlag, "local-manifest", false, "Use local manifest")
	cmdUpdate.Flags.BoolVar(&verboseUpdateFlag, "verbose", false, "Show all update logs.")
	cmdUpdate.Flags.IntVar(&attemptsFlag, "attempts", 1, "Number of attempts before failing.")
	cmdUpdate.Flags.BoolVar(&autoupdateFlag, "autoupdate", true, "Automatically update to the new version.")
	cmdUpdate.Flags.BoolVar(&forceAutoupdateFlag, "force-autoupdate", false, "Always update to the current version.")
	cmdUpdate.Flags.BoolVar(&rebaseUntrackedFlag, "rebase-untracked", false, "Rebase untracked branches onto HEAD.")
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
	ArgsName: "<snapshot>",
	ArgsLong: "<snapshot> is the snapshot manifest file.",
}

func runUpdate(jirix *jiri.X, args []string) error {
	if len(args) > 1 {
		return jirix.UsageErrorf("unexpected number of arguments")
	}

	if autoupdateFlag {
		// Try to update Jiri itself.
		err := jiri.Update(forceAutoupdateFlag)
		if err != nil {
			fmt.Printf("warning: automatic update failed: %v\n", err)
		}
	}

	// Update all projects to their latest version.
	// Attempt <attemptsFlag> times before failing.
	if err := retry.Function(jirix.Context, func() error {
		if len(args) > 0 {
			return project.CheckoutSnapshot(jirix, args[0], gcFlag)
		} else {
			return project.UpdateUniverse(jirix, gcFlag, verboseUpdateFlag, localManifestFlag, rebaseUntrackedFlag)
		}
	}, retry.AttemptsOpt(attemptsFlag)); err != nil {
		return err
	}
	return project.WriteUpdateHistorySnapshot(jirix, "", localManifestFlag)
}
