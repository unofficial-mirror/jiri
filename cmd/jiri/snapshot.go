// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"time"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/project"
)

var (
	snapshotGcFlag  bool
	timeFormatFlag  string
)

func init() {
	cmdSnapshotCheckout.Flags.BoolVar(&snapshotGcFlag, "gc", false, "Garbage collect obsolete repositories.")
	cmdSnapshotCreate.Flags.StringVar(&timeFormatFlag, "time-format", time.RFC3339, "Time format for snapshot file name.")
}

var cmdSnapshot = &cmdline.Command{
	Name:  "snapshot",
	Short: "Manage project snapshots",
	Long: `
The "jiri snapshot" command can be used to manage project snapshots.
In particular, it can be used to create new snapshots and to list
existing snapshots.
`,
	Children: []*cmdline.Command{cmdSnapshotCheckout, cmdSnapshotCreate},
}

// cmdSnapshotCreate represents the "jiri snapshot create" command.
var cmdSnapshotCreate = &cmdline.Command{
	Runner: jiri.RunnerFunc(runSnapshotCreate),
	Name:   "create",
	Short:  "Create a new project snapshot",
	Long: `
The "jiri snapshot create <snapshot>" command captures the current project state
in a manifest.
`,
	ArgsName: "<snapshot>",
	ArgsLong: "<snapshot> is the snapshot manifest file.",
}

func runSnapshotCreate(jirix *jiri.X, args []string) error {
	if len(args) != 1 {
		return jirix.UsageErrorf("unexpected number of arguments")
	}
	return project.CreateSnapshot(jirix, args[0])
}

// cmdSnapshotCheckout represents the "jiri snapshot checkout" command.
var cmdSnapshotCheckout = &cmdline.Command{
	Runner: jiri.RunnerFunc(runSnapshotCheckout),
	Name:   "checkout",
	Short:  "Checkout a project snapshot",
	Long: `
The "jiri snapshot checkout <snapshot>" command restores local project state to
the state in the given snapshot manifest.
`,
	ArgsName: "<snapshot>",
	ArgsLong: "<snapshot> is the snapshot manifest file.",
}

func runSnapshotCheckout(jirix *jiri.X, args []string) error {
	if len(args) != 1 {
		return jirix.UsageErrorf("unexpected number of arguments")
	}
	return project.CheckoutSnapshot(jirix, args[0], snapshotGcFlag)
}
