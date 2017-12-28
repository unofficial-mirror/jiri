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
	attemptsFlag        uint
	autoupdateFlag      bool
	forceAutoupdateFlag bool
	rebaseUntrackedFlag bool
	hookTimeoutFlag     uint
	rebaseAllFlag       bool
	rebaseCurrentFlag   bool
	rebaseTrackedFlag   bool
	runHooksFlag        bool
)

func init() {
	tool.InitializeProjectFlags(&cmdUpdate.Flags)

	cmdUpdate.Flags.BoolVar(&gcFlag, "gc", false, "Garbage collect obsolete repositories.")
	cmdUpdate.Flags.BoolVar(&localManifestFlag, "local-manifest", false, "Use local manifest")
	cmdUpdate.Flags.UintVar(&attemptsFlag, "attempts", 1, "Number of attempts before failing.")
	cmdUpdate.Flags.BoolVar(&autoupdateFlag, "autoupdate", true, "Automatically update to the new version.")
	cmdUpdate.Flags.BoolVar(&forceAutoupdateFlag, "force-autoupdate", false, "Always update to the current version.")
	cmdUpdate.Flags.BoolVar(&rebaseUntrackedFlag, "rebase-untracked", false, "Rebase untracked branches onto HEAD.")
	cmdUpdate.Flags.UintVar(&hookTimeoutFlag, "hook-timeout", project.DefaultHookTimeout, "Timeout in minutes for running the hooks operation.")
	cmdUpdate.Flags.BoolVar(&rebaseAllFlag, "rebase-all", false, "Rebase all tracked branches. Also rebase all untracked branches if -rebase-untracked is passed")
	cmdUpdate.Flags.BoolVar(&rebaseCurrentFlag, "rebase-current", false, "Deprecated. Implies -rebase-tracked. Would be removed in future.")
	cmdUpdate.Flags.BoolVar(&rebaseTrackedFlag, "rebase-tracked", false, "Rebase current tracked branches instead of fast-forwarding them.")
	cmdUpdate.Flags.BoolVar(&runHooksFlag, "run-hooks", true, "Run hooks after updating sources.")
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
	ArgsName: "<file or url>",
	ArgsLong: "<file or url> points to snapshot to checkout.",
}

func runUpdate(jirix *jiri.X, args []string) error {
	if len(args) > 1 {
		return jirix.UsageErrorf("unexpected number of arguments")
	}

	if attemptsFlag < 1 {
		return jirix.UsageErrorf("Number of attempts should be >= 1")
	}
	jirix.Attempts = attemptsFlag

	if autoupdateFlag {
		// Try to update Jiri itself.
		if err := retry.Function(jirix, func() error {
			return jiri.UpdateAndExecute(forceAutoupdateFlag)
		}, fmt.Sprintf("download jiri binary"), retry.AttemptsOpt(jirix.Attempts)); err != nil {
			fmt.Printf("warning: automatic update failed: %v\n", err)
		}
	}
	if rebaseCurrentFlag {
		jirix.Logger.Warningf("Flag -rebase-current has been deprecated, please use -rebase-tracked.\n\n")
		rebaseTrackedFlag = true
	}

	if len(args) > 0 {
		if err := project.CheckoutSnapshot(jirix, args[0], gcFlag, runHooksFlag, hookTimeoutFlag); err != nil {
			return err
		}
	} else {
		err := project.UpdateUniverse(jirix, gcFlag, localManifestFlag,
			rebaseTrackedFlag, rebaseUntrackedFlag, rebaseAllFlag, runHooksFlag, hookTimeoutFlag)
		if err2 := project.WriteUpdateHistorySnapshot(jirix, "", nil, localManifestFlag); err2 != nil {
			if err != nil {
				return fmt.Errorf("while updating: %s, while writing history: %s", err, err2)
			}
			return fmt.Errorf("while writing history: %s", err2)
		}
		if err != nil {
			return err
		}
	}

	if jirix.Failures() != 0 {
		return fmt.Errorf("Project update completed with non-fatal errors")
	}
	return nil
}
