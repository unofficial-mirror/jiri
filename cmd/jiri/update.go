// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"time"

	"go.fuchsia.dev/jiri"
	"go.fuchsia.dev/jiri/cmdline"
	"go.fuchsia.dev/jiri/project"
	"go.fuchsia.dev/jiri/retry"
)

var (
	gcFlag               bool
	localManifestFlag    bool
	attemptsFlag         uint
	autoupdateFlag       bool
	forceAutoupdateFlag  bool
	rebaseUntrackedFlag  bool
	hookTimeoutFlag      uint
	fetchPkgsTimeoutFlag uint
	rebaseAllFlag        bool
	rebaseCurrentFlag    bool
	rebaseTrackedFlag    bool
	runHooksFlag         bool
	fetchPkgsFlag        bool
	overrideOptionalFlag bool
)

const (
	MIN_EXECUTION_TIMING_THRESHOLD time.Duration = time.Duration(30) * time.Minute        // 30 min
	MAX_EXECUTION_TIMING_THRESHOLD time.Duration = time.Duration(2) * time.Hour * 24 * 14 // 2 weeks
)

func init() {
	cmdUpdate.Flags.BoolVar(&gcFlag, "gc", false, "Garbage collect obsolete repositories.")
	cmdUpdate.Flags.BoolVar(&localManifestFlag, "local-manifest", false, "Use local manifest")
	cmdUpdate.Flags.UintVar(&attemptsFlag, "attempts", 3, "Number of attempts before failing.")
	cmdUpdate.Flags.BoolVar(&autoupdateFlag, "autoupdate", true, "Automatically update to the new version.")
	cmdUpdate.Flags.BoolVar(&forceAutoupdateFlag, "force-autoupdate", false, "Always update to the current version.")
	cmdUpdate.Flags.BoolVar(&rebaseUntrackedFlag, "rebase-untracked", false, "Rebase untracked branches onto HEAD.")
	cmdUpdate.Flags.UintVar(&hookTimeoutFlag, "hook-timeout", project.DefaultHookTimeout, "Timeout in minutes for running the hooks operation.")
	cmdUpdate.Flags.UintVar(&fetchPkgsTimeoutFlag, "fetch-packages-timeout", project.DefaultPackageTimeout, "Timeout in minutes for fetching prebuilt packages using cipd.")
	cmdUpdate.Flags.BoolVar(&rebaseAllFlag, "rebase-all", false, "Rebase all tracked branches. Also rebase all untracked branches if -rebase-untracked is passed")
	cmdUpdate.Flags.BoolVar(&rebaseCurrentFlag, "rebase-current", false, "Deprecated. Implies -rebase-tracked. Would be removed in future.")
	cmdUpdate.Flags.BoolVar(&rebaseTrackedFlag, "rebase-tracked", false, "Rebase current tracked branches instead of fast-forwarding them.")
	cmdUpdate.Flags.BoolVar(&runHooksFlag, "run-hooks", true, "Run hooks after updating sources.")
	cmdUpdate.Flags.BoolVar(&fetchPkgsFlag, "fetch-packages", true, "Use cipd to fetch packages.")
	cmdUpdate.Flags.BoolVar(&overrideOptionalFlag, "override-optional", false, "Override existing optional attributes in the snapshot file with current jiri settings")
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
		jirix.OverrideOptional = overrideOptionalFlag
		if err := project.CheckoutSnapshot(jirix, args[0], gcFlag, runHooksFlag, fetchPkgsFlag, hookTimeoutFlag, fetchPkgsTimeoutFlag); err != nil {
			return err
		}
	} else {
		lastSnapshot := jirix.UpdateHistoryLatestLink()
		duration := time.Duration(0)
		if info, err := os.Stat(lastSnapshot); err == nil {
			duration = time.Since(info.ModTime())
			if duration < MIN_EXECUTION_TIMING_THRESHOLD || duration > MAX_EXECUTION_TIMING_THRESHOLD {
				duration = time.Duration(0)
			}
		}

		err := project.UpdateUniverse(jirix, gcFlag, localManifestFlag,
			rebaseTrackedFlag, rebaseUntrackedFlag, rebaseAllFlag, runHooksFlag, fetchPkgsFlag, hookTimeoutFlag, fetchPkgsTimeoutFlag)
		if err2 := project.WriteUpdateHistorySnapshot(jirix, nil, nil, localManifestFlag); err2 != nil {
			if err != nil {
				return fmt.Errorf("while updating: %s, while writing history: %s", err, err2)
			}
			return fmt.Errorf("while writing history: %s", err2)
		}
		if err != nil {
			return err
		}

		// Only track on successful update
		if duration.Nanoseconds() > 0 {
			jirix.AnalyticsSession.AddCommandExecutionTiming("update", duration)
		}
	}

	if jirix.Failures() != 0 {
		return fmt.Errorf("Project update completed with non-fatal errors")
	}

	if err := project.WriteUpdateHistoryLog(jirix); err != nil {
		jirix.Logger.Errorf("Failed to save jiri logs: %v", err)
	}
	return nil
}
