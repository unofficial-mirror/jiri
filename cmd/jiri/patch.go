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
	"fuchsia.googlesource.com/jiri/git"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/project"
)

var (
	patchRebaseFlag  bool
	patchTopicFlag   bool
	patchBranchFlag  string
	patchDeleteFlag  bool
	patchHostFlag    string
	patchForceFlag   bool
	cherryPickFlag   bool
	detachedHeadFlag bool
)

func init() {
	cmdPatch.Flags.StringVar(&patchBranchFlag, "branch", "", "Name of the branch the patch will be applied to")
	cmdPatch.Flags.BoolVar(&patchDeleteFlag, "delete", false, "Delete the existing branch if already exists")
	cmdPatch.Flags.BoolVar(&patchForceFlag, "force", false, "Use force when deleting the existing branch")
	cmdPatch.Flags.BoolVar(&patchRebaseFlag, "rebase", false, "Rebase the change after downloading")
	cmdPatch.Flags.StringVar(&patchHostFlag, "host", "", `Gerrit host to use. Defaults to gerrit host specified in manifest.`)
	cmdPatch.Flags.BoolVar(&patchTopicFlag, "topic", false, `Patch whole topic.`)
	cmdPatch.Flags.BoolVar(&cherryPickFlag, "cherry-pick", false, `Cherry-pick patches instead of checking out.`)
	cmdPatch.Flags.BoolVar(&detachedHeadFlag, "no-branch", false, `Don't create the branch for the patch.`)
}

// cmdPatch represents the "jiri patch" command.
var cmdPatch = &cmdline.Command{
	Runner: jiri.RunnerFunc(runPatch),
	Name:   "patch",
	Short:  "Patch in the existing change",
	Long: `
Command "patch" applies the existing changelist to the current project. The
change can be identified either using change ID, in which case the latest
patchset will be used, or the the full reference. By default patch will be
checked-out on a new branch.

A new branch will be created to apply the patch to. The default name of this
branch is "change/<changeset>/<patchset>", but this can be overriden using the
-branch flag. The command will fail if the branch already exists. The -delete
flag will delete the branch if already exists. Use the -force flag to force
deleting the branch even if it contains unmerged changes).

if -topic flag is true jiri will fetch whole topic and will try to apply to
individual projects. Patch will assume topic is of form {USER}-{BRANCH} and
will try to create branch name out of it. If this fails default branch name
will be same as topic. Currently patch does not support the scenario when
change "B" is created on top of "A" and both have same topic.
`,
	ArgsName: "<change or topic>",
	ArgsLong: "<change or topic> is a change ID, full reference or topic when -topic is true.",
}

// patchProject checks out the given change.
func patchProject(jirix *jiri.X, local project.Project, ref, branch, remote string) (bool, error) {
	scm := gitutil.New(jirix, gitutil.RootDirOpt(local.Path))
	g := git.NewGit(local.Path)
	if !detachedHeadFlag {
		if branch == "" {
			cl, ps, err := gerrit.ParseRefString(ref)
			if err != nil {
				return false, err
			}
			branch = fmt.Sprintf("change/%v/%v", cl, ps)
		}
		jirix.Logger.Infof("Patching project %s(%s) on branch %q\n", local.Name, local.Path, branch)
		branchExists, err := g.BranchExists(branch)
		if err != nil {
			return false, err
		}
		if branchExists {
			if patchDeleteFlag {
				_, currentBranch, err := g.GetBranches()
				if err != nil {
					return false, err
				}
				if currentBranch == branch {
					if err := scm.CheckoutBranch("remotes/origin/"+remote, gitutil.DetachOpt(true)); err != nil {
						return false, err
					}
				}
				if err := scm.DeleteBranch(branch, gitutil.ForceOpt(patchForceFlag)); err != nil {
					jirix.Logger.Errorf("Cannot delete branch %q: %s", branch, err)
					jirix.IncrementFailures()
					return false, nil
				}
			} else {
				jirix.Logger.Errorf("Branch %q already exists in project %q", branch, local.Name)
				jirix.IncrementFailures()
				return false, nil
			}
		}
	} else {
		jirix.Logger.Infof("Patching project %s(%s)\n", local.Name, local.Path)
	}
	if err := scm.FetchRefspec("origin", ref); err != nil {
		return false, err
	}
	branchBase := "FETCH_HEAD"
	lastRef := ""
	if cherryPickFlag {
		if state, err := project.GetProjectState(jirix, local, false); err != nil {
			return false, err
		} else {
			lastRef = state.CurrentBranch.Name
			if lastRef == "" {
				lastRef = state.CurrentBranch.Revision
			}
		}
		branchBase = "HEAD"
	}
	if !detachedHeadFlag {
		if err := g.CreateBranchFromRef(branch, branchBase); err != nil {
			return false, err
		}
		if err := g.SetUpstream(branch, "origin/"+remote); err != nil {
			return false, fmt.Errorf("setting upstream to 'origin/%s': %s", remote, err)
		}
		if err := scm.CheckoutBranch(branch); err != nil {
			return false, err
		}
	} else if err := scm.CheckoutBranch(branchBase); err != nil {
		return false, err
	}
	if cherryPickFlag {
		if err := scm.CherryPick("FETCH_HEAD"); err != nil {
			jirix.Logger.Errorf("Error: %s\n", err)
			jirix.IncrementFailures()

			jirix.Logger.Infof("Aborting and checking out last ref: %s\n", lastRef)

			// abort cherry-pick
			if err := scm.CherryPickAbort(); err != nil {
				jirix.Logger.Errorf("Cherry-pick abort failed. Error:%s\nPlease do it manually:'%s'\n\n", err,
					jirix.Color.Yellow("git -C %q cherry-pick --abort && git -C %q checkout %s", local.Path, local.Path, lastRef))
				return false, nil
			}

			// checkout last ref
			if err := scm.CheckoutBranch(lastRef); err != nil {
				jirix.Logger.Errorf("Not able to checkout last ref. Error:%s\nPlease do it manually:'%s'\n\n", err,
					jirix.Color.Yellow("git -C %q checkout %s", local.Path, lastRef))
				return false, nil
			}

			scm.DeleteBranch(branch, gitutil.ForceOpt(true))

			return false, nil
		}
	}
	jirix.Logger.Infof("Project patched\n")
	return true, nil
}

// rebaseProject rebases the current branch on top of a given branch.
func rebaseProject(jirix *jiri.X, project project.Project, change gerrit.Change) error {
	jirix.Logger.Infof("Rebasing project %s(%s)\n", project.Name, project.Path)
	scm := gitutil.New(jirix, gitutil.UserNameOpt(change.Owner.Name), gitutil.UserEmailOpt(change.Owner.Email), gitutil.RootDirOpt(project.Path))
	if err := scm.FetchRefspec("origin", change.Branch); err != nil {
		jirix.Logger.Errorf("Not able to fetch branch %q: %s", change.Branch, err)
		jirix.IncrementFailures()
		return nil
	}
	if err := scm.Rebase("origin/" + change.Branch); err != nil {
		if err := scm.RebaseAbort(); err != nil {
			return err
		}
		jirix.Logger.Errorf("Cannot rebase the change: %s", err)
		jirix.IncrementFailures()
		return nil
	}
	jirix.Logger.Infof("Project rebased\n")
	return nil
}

func runPatch(jirix *jiri.X, args []string) error {
	if expected, got := 1, len(args); expected != got {
		return jirix.UsageErrorf("unexpected number of arguments: expected %v, got %v", expected, got)
	}
	arg := args[0]

	var cl int
	var ps int
	var err error
	if !patchTopicFlag {
		cl, ps, err = gerrit.ParseRefString(arg)
		if err != nil {
			cl, err = strconv.Atoi(arg)
			if err != nil {
				return fmt.Errorf("invalid argument: %v", arg)
			}
		}
	}

	p, perr := currentProject(jirix)
	if !patchTopicFlag && perr == nil {
		host := patchHostFlag
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
		g := gerrit.New(jirix, hostUrl)

		change, err := g.GetChange(cl)
		if err != nil {
			return err
		}
		branch := patchBranchFlag
		ok := false
		if ps != -1 {
			if ok, err = patchProject(jirix, p, arg, branch, change.Branch); err != nil {
				return err
			}
		} else {
			if ok, err = patchProject(jirix, p, change.Reference(), branch, change.Branch); err != nil {
				return err
			}
		}
		if ok && patchRebaseFlag {
			if err := rebaseProject(jirix, p, *change); err != nil {
				return err
			}
		}
	} else {
		host := patchHostFlag
		if host == "" && patchTopicFlag {
			if perr == nil {
				host = p.GerritHost
			}
			if host == "" {
				return fmt.Errorf("no Gerrit host; use the '--host' flag or run from inside a project with gerrit host")
			}
		} else if host == "" {
			return fmt.Errorf("no Gerrit host; use the '--host' flag")
		}
		hostUrl, err := url.Parse(host)
		if err != nil {
			return fmt.Errorf("invalid Gerrit host %q: %v", host, err)
		}
		g := gerrit.New(jirix, hostUrl)

		var changes gerrit.CLList
		branch := patchBranchFlag
		if patchTopicFlag {
			changes, err = g.ListOpenChangesByTopic(arg)
			if err != nil {
				return err
			}
			if len(changes) == 0 {
				return fmt.Errorf("No changes found with topic %q", arg)
			}
			ps = -1
			if branch == "" {
				userPrefix := os.Getenv("USER") + "-"
				if strings.HasPrefix(arg, userPrefix) {
					branch = strings.Replace(arg, userPrefix, "", 1)
				} else {
					branch = arg
				}
			}
		} else {
			change, err := g.GetChange(cl)
			if err != nil {
				return err
			}
			changes = append(changes, *change)
		}
		projects, err := project.LocalProjects(jirix, project.FastScan)
		if err != nil {
			return err
		}
		for _, change := range changes {
			var ref string
			if ps != -1 {
				ref = arg
			} else {
				ref = change.Reference()
			}
			var projectToPatch *project.Project
			var projectToPatchNoGerritHost *project.Project
			for _, p := range projects {
				if strings.HasSuffix(p.Remote, "/"+change.Project) {
					if p.GerritHost != host {

						if p.GerritHost == "" {
							cp := p
							projectToPatchNoGerritHost = &cp
							//skip for now
							continue
						} else {
							u, err := url.Parse(p.GerritHost)
							if err != nil {
								jirix.Logger.Warningf("invalid Gerrit host %q for project %s: %s", p.GerritHost, p.Name, err)
							}
							if u.Host != hostUrl.Host {
								jirix.Logger.Debugf("skipping project %s(%s) for CL %s\n\n", p.Name, p.Path, g.GetChangeURL(change.Number))
								continue
							}
						}
					}
					projectToPatch = &p
					break
				}
			}
			if projectToPatch == nil && projectToPatchNoGerritHost != nil {
				// Try to patch the project with no gerrit host
				projectToPatch = projectToPatchNoGerritHost
			}
			if projectToPatch != nil {
				if ok, err := patchProject(jirix, *projectToPatch, ref, branch, change.Branch); err != nil {
					return err
				} else if ok {
					if patchRebaseFlag {
						if err := rebaseProject(jirix, *projectToPatch, change); err != nil {
							return err
						}
					}
				}
				fmt.Println()
			} else {
				jirix.Logger.Errorf("Cannot find project to patch CL %s\n", g.GetChangeURL(change.Number))
				jirix.IncrementFailures()
				fmt.Println()
			}
		}
	}
	if jirix.Failures() != 0 {
		return fmt.Errorf("Patch failed")
	}
	return nil
}
