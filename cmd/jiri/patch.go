// Copyright 2016 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/gerrit"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/project"
)

var (
	rebaseFlag bool
)

func init() {
	cmdPatch.Flags.StringVar(&branchFlag, "branch", "", "Name of the branch the patch will be applied to")
	cmdPatch.Flags.BoolVar(&deleteFlag, "delete", false, "Delete the existing branch if already exists")
	cmdPatch.Flags.BoolVar(&forceFlag, "force", false, "Use force when deleting the existing branch")
	cmdPatch.Flags.BoolVar(&rebaseFlag, "rebase", false, "Rebase the change after downloading")
	cmdPatch.Flags.StringVar(&hostFlag, "host", "", `Gerrit host to use. Defaults to gerrit host specified in manifest.`)
}

// cmdPatch represents the "jiri patch" command.
var cmdPatch = &cmdline.Command{
	Runner: jiri.RunnerFunc(runPatch),
	Name:   "patch",
	Short:  "Patch in the existing change",
	Long: `
Command "patch" applies the existing changelist to the current project. The
change can be identified either using change ID, in which case the latest
patchset will be used, or the the full reference.

A new branch will be created to apply the patch to. The default name of this
branch is "change/<changeset>/<patchset>", but this can be overriden using the
-branch flag. The command will fail if the branch already exists. The -delete
flag will delete the branch if already exists. Use the -force flag to force
deleting the branch even if it contains unmerged changes).
`,
	ArgsName: "<change>",
	ArgsLong: "<change> is a change ID or a full reference.",
}

// patchProject changes directory into the project directory, checks out the given
// change, then cds back to the original directory.
func patchProject(jirix *jiri.X, project project.Project, ref string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	defer os.Chdir(cwd)

	if err = os.Chdir(project.Path); err != nil {
		return err
	}

	var branch string
	if branchFlag != "" {
		branch = branchFlag
	} else {
		cl, ps, err := gerrit.ParseRefString(ref)
		if err != nil {
			return err
		}
		branch = fmt.Sprintf("change/%v/%v", cl, ps)
	}

	git := gitutil.New(jirix.NewSeq())
	if git.BranchExists(branch) {
		if deleteFlag {
			if err := git.CheckoutBranch("origin/master"); err != nil {
				return err
			}
			if err := git.DeleteBranch(branch, gitutil.ForceOpt(forceFlag)); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("branch %v already exists in project %q", branch, project.Name)
		}
	}
	if err := git.FetchRefspec("origin", ref); err != nil {
		return err
	}
	if err := git.CreateBranchWithUpstream(branch, "FETCH_HEAD"); err != nil {
		return err
	}
	if err := git.CheckoutBranch(branch); err != nil {
		return err
	}

	return nil
}

func runPatch(jirix *jiri.X, args []string) error {
	if expected, got := 1, len(args); expected != got {
		return jirix.UsageErrorf("unexpected number of arguments: expected %v, got %v", expected, got)
	}
	arg := args[0]

	cl, ps, err := gerrit.ParseRefString(arg)
	if err != nil {
		cl, err = strconv.Atoi(arg)
		if err != nil {
			return fmt.Errorf("invalid argument: %v", arg)
		}
	}

	var change gerrit.Change
	if p, err := currentProject(jirix); err == nil {
		host := hostFlag
		if host == "" {
			if p.GerritHost == "" {
				return fmt.Errorf("no Gerrit host; use the '--host' flag, or add a 'gerrithost' attribute for project %q", p.Name)
			}
			host = p.GerritHost
		}
		hostUrl, err := url.Parse(host)
		if err != nil {
			return fmt.Errorf("invalid Gerrit host %q: %v", host, err)
		}
		g := jirix.Gerrit(hostUrl)

		change, err := g.GetChange(cl)
		if err != nil {
			return err
		}
		if ps != -1 {
			if err := patchProject(jirix, p, arg); err != nil {
				return err
			}
		} else {
			if err := patchProject(jirix, p, change.Reference()); err != nil {
				return err
			}
		}
	} else {
		host := hostFlag
		if host == "" {
			return fmt.Errorf("no Gerrit host; use the '--host' flag")
		}
		hostUrl, err := url.Parse(host)
		if err != nil {
			return fmt.Errorf("invalid Gerrit host %q: %v", host, err)
		}
		g := jirix.Gerrit(hostUrl)

		change, err := g.GetChange(cl)
		if err != nil {
			return err
		}
		var ref string
		if ps != -1 {
			ref = arg
		} else {
			ref = change.Reference()
		}

		projects, _, err := project.LoadManifest(jirix)
		if err != nil {
			return err
		}

		for _, p := range projects {
			if strings.HasSuffix(p.Remote, change.Project) {
				if err := patchProject(jirix, p, ref); err != nil {
					return err
				}
				break
			}
		}
	}

	if rebaseFlag {
		git := gitutil.New(jirix.NewSeq())
		if err := git.Fetch("", gitutil.AllOpt(true), gitutil.PruneOpt(true)); err != nil {
			return err
		}
		if err = git.Rebase("origin/" + change.Branch); err != nil {
			if err := git.RebaseAbort(); err != nil {
				return err
			}
			return fmt.Errorf("Cannot rebase the branch: %v", err)
		}
	}

	return nil
}
