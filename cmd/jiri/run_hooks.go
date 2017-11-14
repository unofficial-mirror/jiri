// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/project"
	"fuchsia.googlesource.com/jiri/tool"
)

var runHooksFlags struct {
	localManifest bool
	hookTimeout   uint
	attempts      uint
}

var cmdRunHooks = &cmdline.Command{
	Runner: jiri.RunnerFunc(runHooks),
	Name:   "run-hooks",
	Short:  "Run hooks using local manifest",
	Long: `
Run hooks using local manifest JIRI_HEAD version if -local-manifest flag is
false, else it runs hooks using current manifest checkout version.
`,
}

func init() {
	tool.InitializeProjectFlags(&cmdRunHooks.Flags)
	cmdRunHooks.Flags.BoolVar(&runHooksFlags.localManifest, "local-manifest", false, "Use local checked out manifest.")
	cmdRunHooks.Flags.UintVar(&runHooksFlags.hookTimeout, "hook-timeout", project.DefaultHookTimeout, "Timeout in minutes for running the hooks operation.")
	cmdRunHooks.Flags.UintVar(&runHooksFlags.attempts, "attempts", 1, "Number of attempts before failing.")
}

func runHooks(jirix *jiri.X, args []string) error {
	localProjects, err := project.LocalProjects(jirix, project.FastScan)
	if err != nil {
		return err
	}
	if runHooksFlags.attempts < 1 {
		return jirix.UsageErrorf("Number of attempts should be >= 1")
	}
	jirix.Attempts = runHooksFlags.attempts

	// Get hooks.
	_, hooks, err := project.LoadManifestFile(jirix, jirix.JiriManifestFile(), localProjects, runHooksFlags.localManifest)
	return project.RunHooks(jirix, hooks, runHooksFlags.hookTimeout)
}
