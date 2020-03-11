// Copyright 2018 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"strings"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/project"
)

type resolveFlags struct {
	lockFilePath         string
	localManifestFlag    bool
	enablePackageLock    bool
	enableProjectLock    bool
	enablePackageVersion bool
	allowFloatingRefs    bool
	fullResolve          bool
	hostnameAllowList    string
}

func (r *resolveFlags) AllowFloatingRefs() bool {
	return r.allowFloatingRefs
}

func (r *resolveFlags) LockFilePath() string {
	return r.lockFilePath
}

func (r *resolveFlags) LocalManifest() bool {
	return r.localManifestFlag
}

func (r *resolveFlags) EnablePackageLock() bool {
	return r.enablePackageLock
}

func (r *resolveFlags) EnableProjectLock() bool {
	return r.enableProjectLock
}

func (r *resolveFlags) HostnameAllowList() []string {
	ret := make([]string, 0)
	hosts := strings.Split(r.hostnameAllowList, ",")
	for _, item := range hosts {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		ret = append(ret, item)
	}
	return ret
}

func (r *resolveFlags) FullResolve() bool {
	return r.fullResolve
}

var resolveFlag resolveFlags

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
	flags.StringVar(&resolveFlag.lockFilePath, "output", "jiri.lock", "Path to the generated lockfile")
	flags.BoolVar(&resolveFlag.localManifestFlag, "local-manifest", false, "Use local manifest")
	flags.BoolVar(&resolveFlag.enablePackageLock, "enable-package-lock", true, "Enable resolving packages in lockfile")
	flags.BoolVar(&resolveFlag.enableProjectLock, "enable-project-lock", false, "Enable resolving projects in lockfile")
	flags.BoolVar(&resolveFlag.allowFloatingRefs, "allow-floating-refs", false, "Allow packages to be pinned to floating refs such as \"latest\"")
	flags.StringVar(&resolveFlag.hostnameAllowList, "allow-hosts", "", "List of hostnames that can be used in the url of a repository, seperated by comma. It will not be enforced if it is left empty.")
	flags.BoolVar(&resolveFlag.fullResolve, "full-resolve", false, "Resolve all project and packages, not just those are changed.")
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
	// While revision pins for projects can be updated by 'jiri edit',
	// instance IDs of packages can only be updated by 'jiri resolve' due
	// to the way how cipd works. Since roller is using 'jiri resolve'
	// to update a single jiri.lock file each time, it will cause conflicting
	// instance ids between updated 'jiri.lock' and un-updated 'jiri.lock' files.
	// Jiri will halt when detecting conflicts in locks. So to make it work,
	// we need to temporarily disable the conflicts detection.
	jirix.IgnoreLockConflicts = true
	return project.GenerateJiriLockFile(jirix, manifestFiles, &resolveFlag)
}
