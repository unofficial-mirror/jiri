// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The following enables go generate to generate the doc.go file.
//go:generate go run fuchsia.googlesource.com/jiri/cmdline/testdata/gendoc.go -env="" .

package main

import (
	"fmt"
	"runtime"
	"syscall"

	"fuchsia.googlesource.com/jiri/cmdline"
)

func init() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	if runtime.GOOS == "darwin" {
		var rLimit syscall.Rlimit
		err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
		if err != nil {
			fmt.Println("Unable to obtain rlimit: ", err)
		}
		if rLimit.Cur < rLimit.Max {
			rLimit.Max = 999999
			rLimit.Cur = 999999
			err = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit)
			if err != nil {
				fmt.Println("Unable to increase rlimit: ", err)
			}
		}
	}
	cmdRoot = newCmdRoot()
}

func main() {
	cmdline.Main(cmdRoot)
}

// cmdRoot represents the root of the jiri tool.
var cmdRoot *cmdline.Command

// Use a factory to avoid an initialization loop between between the
// Runner functions in subcommands and the ParsedFlags field in the
// Command.
func newCmdRoot() *cmdline.Command {
	return &cmdline.Command{
		Name:  "jiri",
		Short: "Multi-purpose tool for multi-repo development",
		Long: `
Command jiri is a multi-purpose tool for multi-repo development.
`,
		LookPath: true,
		Children: []*cmdline.Command{
			cmdBranch,
			cmdGrep,
			cmdImport,
			cmdInit,
			cmdPatch,
			cmdProject,
			cmdProjectConfig,
			cmdRunP,
			cmdSelfUpdate,
			cmdSnapshot,
			cmdStatus,
			cmdUpdate,
			cmdUpload,
			cmdVersion,
		},
		Topics: []cmdline.Topic{
			topicFileSystem,
			topicManifest,
		},
	}
}

var topicFileSystem = cmdline.Topic{
	Name:  "filesystem",
	Short: "Description of jiri file system layout",
	Long: `
All data managed by the jiri tool is located in the file system under a root
directory, colloquially called the jiri root directory.  The file system layout
looks like this:

 [root]                              # root directory (name picked by user)
 [root]/.jiri_root                   # root metadata directory
 [root]/.jiri_root/bin               # contains jiri tool binary
 [root]/.jiri_root/update_history    # contains history of update snapshots
 [root]/.manifest                    # contains jiri manifests
 [root]/[project1]                   # project directory (name picked by user)
 [root]/[project1]/.jiri             # project metadata directory
 [root]/[project1]/.jiri/metadata.v2 # project metadata file
 [root]/[project1]/.jiri/<<cls>>     # project per-cl metadata directories
 [root]/[project1]/<<files>>         # project files
 [root]/[project2]...

The [root] and [projectN] directory names are picked by the user.  The <<cls>>
are named via jiri cl new, and the <<files>> are named as the user adds files
and directories to their project.  All other names above have special meaning to
the jiri tool, and cannot be changed; you must ensure your path names don't
collide with these special names.

To find the [root] directory, the jiri binary looks for the .jiri_root
directory, starting in the current working directory and walking up the
directory chain.  The search is terminated successfully when the .jiri_root
directory is found; it fails after it reaches the root of the file system.
Thus jiri must be invoked from the [root] directory or one of its
subdirectories.  To invoke jiri from a different directory, you can set the
-root flag to point to your [root] directory.

Keep in mind that when "jiri update" is run, the jiri tool itself is
automatically updated along with all projects.  Note that if you have multiple
[root] directories on your file system, you must remember to run the jiri
binary corresponding to your [root] directory.  Things may fail if you mix
things up, since the jiri binary is updated with each call to "jiri update",
and you may encounter version mismatches between the jiri binary and the
various metadata files or other logic.

The jiri binary is located at [root]/.jiri_root/bin/jiri
`,
}

var topicManifest = cmdline.Topic{
	Name:  "manifest",
	Short: "Description of manifest files",
	Long: `
Jiri manifest files describe the set of projects that get synced when running
"jiri update".

The first manifest file that jiri reads is in [root]/.jiri_manifest.  This
manifest **must** exist for the jiri tool to work.

Usually the manifest in [root]/.jiri_manifest will import other manifests from
remote repositories via <import> tags, but it can contain its own list of
projects as well.

Manifests have the following XML schema:

<manifest>
  <imports>
    <import remote="https://vanadium.googlesource.com/manifest"
            manifest="public"
            name="manifest"
    />
    <localimport file="/path/to/local/manifest"/>
    ...
  </imports>
  <projects>
    <project name="my-project"
             path="path/where/project/lives"
             protocol="git"
             remote="https://github.com/myorg/foo"
             revision="ed42c05d8688ab23"
             remotebranch="my-branch"
             gerrithost="https://myorg-review.googlesource.com"
             githooks="path/to/githooks-dir"
    />
    ...
  </projects>
  <hooks>
    <hook name="update"
          project="mojo/public"
          action="update.sh"/>
    ...
  </hooks>

</manifest>

The <import> and <localimport> tags can be used to share common projects across
multiple manifests.

A <localimport> tag should be used when the manifest being imported and the
importing manifest are both in the same repository, or when neither one is in a
repository.  The "file" attribute is the path to the manifest file being
imported.  It can be absolute, or relative to the importing manifest file.

If the manifest being imported and the importing manifest are in different
repositories then an <import> tag must be used, with the following attributes:

* remote (required) - The remote url of the repository containing the
manifest to be imported

* manifest (required) - The path of the manifest file to be imported,
relative to the repository root.

* name (optional) - The name of the project corresponding to the manifest
repository.  If your manifest contains a <project> with the same remote as
the manifest remote, then the "name" attribute of on the <import> tag should
match the "name" attribute on the <project>.  Otherwise, jiri will clone the
manifest repository on every update.

The <project> tags describe the projects to sync, and what state they should
sync to, accoring to the following attributes:

* name (required) - The name of the project.

* path (required) - The location where the project will be located, relative to
the jiri root.

* remote (required) - The remote url of the project repository.

* protocol (optional) - The protocol to use when cloning and syncing the repo.
Currently "git" is the default and only supported protocol.

* remotebranch (optional) - The remote branch that the project will sync to.
Defaults to "master".  The "remotebranch" attribute is ignored if "revision"
is specified.

* revision (optional) - The specific revision (usually a git SHA) that the
project will sync to.  If "revision" is  specified then the "remotebranch"
attribute is ignored.

* gerrithost (optional) - The url of the Gerrit host for the project.  If
specified, then running "jiri cl upload" will upload a CL to this Gerrit host.

* githooks (optional) - The path (relative to [root]) of a directory containing
git hooks that will be installed in the projects .git/hooks directory during
each update.

The <hook> tag describes the hooks that must be executed after every 'jiri update'
They are configured via the following attributes:

* name (required) - The name of the of the hook to identify it

* project (required) - The name of the project where the hook is present

* action (required) - Action to be performed inside the project.
It is mostly identified by a script
`,
}
