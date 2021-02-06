// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"go.fuchsia.dev/jiri"
	"go.fuchsia.dev/jiri/cmdline"
	"go.fuchsia.dev/jiri/project"
)

var cmdSnapshot = &cmdline.Command{
	Runner: jiri.RunnerFunc(runSnapshot),
	Name:   "snapshot",
	Short:  "Create a new project snapshot",
	Long: `
The "jiri snapshot <snapshot>" command captures the current project state
in a manifest.
`,
	ArgsName: "<snapshot>",
	ArgsLong: "<snapshot> is the snapshot manifest file.",
}

func runSnapshot(jirix *jiri.X, args []string) error {
	if len(args) != 1 {
		return jirix.UsageErrorf("unexpected number of arguments")
	}
	return project.CreateSnapshot(jirix, args[0], nil, nil, true)
}
