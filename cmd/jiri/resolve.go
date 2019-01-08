// Copyright 2018 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/project"
)

var resolveFlags struct {
	lockFilePath      string
	localManifestFlag bool
}

var cmdResolve = &cmdline.Command{
	Runner: jiri.RunnerFunc(runResolve),
	Name:   "resolve",
	Short:  "Generate jiri lockfile",
	Long: `
Generate jiri lockfile in json format for <manifest ...>. If no manifest
provided, jiri will use .jiri_manifest by default.
`,
	ArgsName: "<manifest ...>",
	ArgsLong: "<manifest ...> is a list of manifest files for lockfile generation",
}

func init() {
	flags := &cmdResolve.Flags
	flags.StringVar(&resolveFlags.lockFilePath, "output", "jiri.lock", "Path to the generated lockfile")
	flags.BoolVar(&resolveFlags.localManifestFlag, "local-manifest", false, "Use local manifest")
}

func runResolve(jirix *jiri.X, args []string) error {
	manifestFiles := make([]string, 0)
	if len(args) == 0 {
		// Use .jiri_manifest if no manifest file path is present
		manifestFiles = append(manifestFiles, jirix.JiriManifestFile())
	} else {
		for _, m := range args {
			manifestFiles = append(manifestFiles, m)
		}
	}
	return project.GenerateJiriLockFile(jirix, manifestFiles, resolveFlags.lockFilePath, resolveFlags.localManifestFlag)
}
