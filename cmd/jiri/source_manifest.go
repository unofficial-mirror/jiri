// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"go.fuchsia.dev/jiri"
	"go.fuchsia.dev/jiri/cmdline"
	"go.fuchsia.dev/jiri/project"
)

var cmdSourceManifest = &cmdline.Command{
	Runner: jiri.RunnerFunc(runSourceManifest),
	Name:   "source-manifest",
	Short:  "Create a new source-manifest from current checkout",
	Long: `
This command captures the current project state in a source-manifest format.
See https://github.com/luci/recipes-py/blob/master/recipe_engine/source_manifest.proto
for its format.
`,
	ArgsName: "<source-manifest>",
	ArgsLong: "<source-manifest> is the source-manifest file.",
}

func runSourceManifest(jirix *jiri.X, args []string) error {
	jirix.TimerPush("create source manifest")
	defer jirix.TimerPop()

	if len(args) != 1 {
		return jirix.UsageErrorf("unexpected number of arguments")
	}

	localProjects, err := project.LocalProjects(jirix, project.FullScan)
	if err != nil {
		return err
	}

	sm, mErr := project.NewSourceManifest(jirix, localProjects)
	if mErr != nil {
		return mErr
	}
	return sm.ToFile(jirix, args[0])
}
