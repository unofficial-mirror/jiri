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
	"sync/atomic"

	"go.fuchsia.dev/jiri"
	"go.fuchsia.dev/jiri/cmdline"
	"go.fuchsia.dev/jiri/gerrit"
	"go.fuchsia.dev/jiri/gitutil"
	"go.fuchsia.dev/jiri/project"
)

var (
	patchRebaseFlag     bool
	patchRebaseRevision string
	patchRebaseBranch   string
	patchTopicFlag      bool
	patchBranchFlag     string
	patchDeleteFlag     bool
	patchHostFlag       string
	patchForceFlag      bool
	cherryPickFlag      bool
	detachedHeadFlag    bool
	patchProjectFlag    string
	rebaseFailures      uint32
)

func init() {
	cmdPatch.Flags.StringVar(&patchBranchFlag, "branch", "", "Name of the branch the patch will be applied to")
	cmdPatch.Flags.BoolVar(&patchDeleteFlag, "delete", false, "Delete the existing branch if already exists")
	cmdPatch.Flags.BoolVar(&patchForceFlag, "force", false, "Use force when deleting the existing branch")
	cmdPatch.Flags.BoolVar(&patchRebaseFlag, "rebase", false, "Rebase the change after downloading")
	cmdPatch.Flags.StringVar(&patchRebaseRevision, "rebase-revision", "", "Rebase the change to a specific revision after downloading")
	cmdPatch.Flags.StringVar(&patchRebaseBranch, "rebase-branch", "", "The branch to rebase the change onto")
	cmdPatch.Flags.StringVar(&patchHostFlag, "host", "", `Gerrit host to use. Defaults to gerrit host specified in manifest.`)
	cmdPatch.Flags.StringVar(&patchProjectFlag, "project", "", `Project to apply patch to. This cannot be passed with topic flag.`)
	cmdPatch.Flags.BoolVar(&patchTopicFlag, "topic", false, `Patch whole topic.`)
	cmdPatch.Flags.BoolVar(&cherryPickFlag, "cherry-pick", false, `Cherry-pick patches instead of checking out.`)
	cmdPatch.Flags.BoolVar(&detachedHeadFlag, "no-branch", false, `Don't create the branch for the patch.`)
}

// Use special address codes for errors that are addressable by the user. The
// recipes will use this to detect when the failure should be considered an
// infrastructure failure vs a failure that is addressable by the user.
const noSuchProjectErr = cmdline.ErrExitCode(23)
const rebaseFailedErr = cmdline.ErrExitCode(24)

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
branch is "change/<changeset>/<patchset>", but this can be overridden using
the -branch flag. The command will fail if the branch already exists. The
-delete flag will delete the branch if already exists. Use the -force flag to
force deleting the branch even if it contains unmerged changes).

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
	if !detachedHeadFlag {
		if branch == "" {
			cl, ps, err := gerrit.ParseRefString(ref)
			if err != nil {
				return false, err
			}
			branch = fmt.Sprintf("change/%v/%v", cl, ps)
		}
		jirix.Logger.Infof("Patching project %s(%s) on branch %q to ref %q\n", local.Name, local.Path, branch, ref)
		branchExists, err := scm.BranchExists(branch)
		if err != nil {
			return false, err
		}
		if branchExists {
			if patchDeleteFlag {
				_, currentBranch, err := scm.GetBranches()
				if err != nil {
					return false, err
				}
				if currentBranch == branch {
					if err := scm.CheckoutBranch("remotes/origin/"+remote, local.GitSubmodules, gitutil.DetachOpt(true)); err != nil {
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
		jirix.Logger.Infof("Patching project %s(%s) to ref %q\n", local.Name, local.Path, ref)
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
		if err := scm.CreateBranchFromRef(branch, branchBase); err != nil {
			return false, err
		}
		if err := scm.SetUpstream(branch, "origin/"+remote); err != nil {
			return false, fmt.Errorf("setting upstream to 'origin/%s': %s", remote, err)
		}
		if err := scm.CheckoutBranch(branch, local.GitSubmodules); err != nil {
			return false, err
		}
	} else if err := scm.CheckoutBranch(branchBase, local.GitSubmodules); err != nil {
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
			if err := scm.CheckoutBranch(lastRef, local.GitSubmodules); err != nil {
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
func rebaseProject(jirix *jiri.X, project project.Project, remoteBranch string) error {
	jirix.Logger.Infof("Rebasing project %s(%s)\n", project.Name, project.Path)
	scm := gitutil.New(jirix, gitutil.RootDirOpt(project.Path))
	name, email, err := scm.UserInfoForCommit("HEAD")
	if err != nil {
		return fmt.Errorf("Rebase: cannot get user info for HEAD: %s", err)
	}
	// TODO: provide a way to set username and email
	scm = gitutil.New(jirix, gitutil.UserNameOpt(name), gitutil.UserEmailOpt(email), gitutil.RootDirOpt(project.Path))
	if err := scm.FetchRefspec("origin", remoteBranch); err != nil {
		jirix.Logger.Errorf("Not able to fetch branch %q: %s", remoteBranch, err)
		jirix.IncrementFailures()
		return nil
	}
	if err := scm.Rebase("remotes/origin/"+remoteBranch, gitutil.RebaseMerges(true)); err != nil {
		if err2 := scm.RebaseAbort(); err2 != nil {
			return err2
		}
		jirix.Logger.Errorf("Cannot rebase the change: %s", err)
		jirix.IncrementFailures()
		atomic.AddUint32(&rebaseFailures, 1)
		return nil
	}
	jirix.Logger.Infof("Project rebased\n")
	return nil
}

// rebaseProjectWRevision rebases the current branch on top of a given revision.
func rebaseProjectWRevision(jirix *jiri.X, project project.Project, revision string) error {
	jirix.Logger.Infof("Rebasing project %s(%s)\n", project.Name, project.Path)
	scm := gitutil.New(jirix, gitutil.RootDirOpt(project.Path))
	name, email, err := scm.UserInfoForCommit("HEAD")
	if err != nil {
		return fmt.Errorf("Rebase: cannot get user info for HEAD: %s", err)
	}
	scm = gitutil.New(jirix, gitutil.UserNameOpt(name), gitutil.UserEmailOpt(email), gitutil.RootDirOpt(project.Path))
	if err := scm.Fetch("origin", gitutil.PruneOpt(true)); err != nil {
		jirix.Logger.Errorf("Not able to fetch origin: %v", err)
		jirix.IncrementFailures()
		return nil
	}
	if err := scm.FetchRefspec("origin", revision); err != nil {
		jirix.Logger.Errorf("Not able to fetch revision %q: %s", revision, err)
		jirix.IncrementFailures()
		return nil
	}
	if err := scm.Rebase(revision, gitutil.RebaseMerges(true)); err != nil {
		if err2 := scm.RebaseAbort(); err2 != nil {
			return err2
		}
		jirix.Logger.Errorf("Cannot rebase the change: %s", err)
		jirix.IncrementFailures()
		atomic.AddUint32(&rebaseFailures, 1)
		return nil
	}
	jirix.Logger.Infof("Project rebased\n")
	return nil
}

func findProject(jirix *jiri.X, projectName string, projects project.Projects, host string, hostUrl *url.URL, ref string) *project.Project {
	var projectToPatch *project.Project
	var projectToPatchNoGerritHost *project.Project
	for _, p := range projects {
		if p.Name == projectName {
			if host != "" && p.GerritHost != host {
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
						jirix.Logger.Debugf("skipping project %s(%s) for CL %s\n\n", p.Name, p.Path, ref)
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
	return projectToPatch
}

func runPatch(jirix *jiri.X, args []string) error {
	if expected, got := 1, len(args); expected != got {
		return jirix.UsageErrorf("unexpected number of arguments: expected %v, got %v", expected, got)
	}
	arg := args[0]

	if patchProjectFlag != "" && patchTopicFlag {
		return jirix.UsageErrorf("-topic and -project flags cannot be used together")
	}

	if patchRebaseRevision != "" && (!patchRebaseFlag || patchProjectFlag == "") {
		return jirix.UsageErrorf("-rebase-revision should only be used with -rebase and -project flag")
	}

	var cl int
	var ps int
	var err error
	changeRef := ""
	remoteBranch := ""
	if !patchTopicFlag {
		cl, ps, err = gerrit.ParseRefString(arg)
		if err != nil {
			if patchProjectFlag != "" {
				return fmt.Errorf("Please pass change ref with -project flag (refs/changes/<ps>/<cl>/<patch-set>)")
			}
			cl, err = strconv.Atoi(arg)
			if err != nil {
				return fmt.Errorf("invalid argument: %v", arg)
			}
		} else {
			changeRef = arg
		}
	}

	var p *project.Project
	host := patchHostFlag
	if patchProjectFlag != "" {
		projects, err := project.LocalProjects(jirix, project.FastScan)
		if err != nil {
			return err
		}
		var hostUrl *url.URL
		if host != "" {
			hostUrl, err = url.Parse(host)
			if err != nil {
				return fmt.Errorf("invalid Gerrit host %q: %s", host, err)
			}
		}
		p = findProject(jirix, patchProjectFlag, projects, host, hostUrl, changeRef)
		if p == nil {
			jirix.Logger.Errorf("Cannot find project for %q", patchProjectFlag)
			return noSuchProjectErr
		}
		// TODO: TO-592 - remove this hardcode
		if patchRebaseBranch == "" && p.RemoteBranch != "" {
			remoteBranch = p.RemoteBranch
		} else if patchRebaseBranch != "" {
			remoteBranch = patchRebaseBranch
		} else {
			remoteBranch = "master"
		}
	} else if project, perr := currentProject(jirix); perr == nil {
		p = &project
		if host == "" {
			if p.GerritHost == "" {
				return fmt.Errorf("no Gerrit host; use the '--host' flag, or add a 'gerrithost' attribute for project %q", p.Name)
			}
			host = p.GerritHost
		}
	}
	if !patchTopicFlag && p != nil {
		if remoteBranch == "" || changeRef == "" {
			hostUrl, err := url.Parse(host)
			if err != nil {
				return fmt.Errorf("invalid Gerrit host %q: %s", host, err)
			}
			g := gerrit.New(jirix, hostUrl)

			change, err := g.GetChange(cl)
			if err != nil {
				return err
			}
			remoteBranch = change.Branch
			changeRef = change.Reference()
		}
		branch := patchBranchFlag
		ok := false
		if ps != -1 {
			if ok, err = patchProject(jirix, *p, arg, branch, remoteBranch); err != nil {
				return err
			}
		} else {
			if ok, err = patchProject(jirix, *p, changeRef, branch, remoteBranch); err != nil {
				return err
			}
		}
		if ok && patchRebaseFlag {
			if patchRebaseRevision != "" {
				if err := rebaseProjectWRevision(jirix, *p, patchRebaseRevision); err != nil {
					return err
				}
			} else {
				if err := rebaseProject(jirix, *p, remoteBranch); err != nil {
					return err
				}
			}
		}
	} else {
		if host == "" {
			return fmt.Errorf("no Gerrit host; use the '--host' flag or run this from inside a project")
		}
		hostUrl, err := url.Parse(host)
		if err != nil {
			return fmt.Errorf("invalid Gerrit host %q: %v", host, err)
		}
		g := gerrit.New(jirix, hostUrl)

		var changes gerrit.CLList
		branch := patchBranchFlag
		if patchTopicFlag {
			temp, err := g.ListOpenChangesByTopic(arg)
			if err != nil {
				return err
			}
			if len(temp) == 0 {
				return fmt.Errorf("No changes found with topic %q", arg)
			}

			projectMap := make(map[string]map[string]gerrit.Change)
			//Handle stacked changes
			for _, change := range temp {
				v, ok := projectMap[change.Project]
				if !ok {
					v = make(map[string]gerrit.Change)
					projectMap[change.Project] = v
				}
				v[change.Change_id] = change
			}

			for p, topicChanges := range projectMap {
				// only CL in the project
				if len(topicChanges) == 1 {
					for _, change := range topicChanges {
						changes = append(changes, change)
						break
					}
					continue
				}

				// stacked CLs, get the top one
				if cherryPickFlag {
					return fmt.Errorf("Multiple CLs for projects %q. We do not support this with cherry-pick flag", p)
				}
				var relatedChanges *gerrit.RelatedChanges
				relatedChangesMap := make(map[string]struct{})

				// get related changes and build map.
				// loop will only run once as we just need one change to build the map.
				for _, change := range topicChanges {
					relatedChanges, err = g.GetRelatedChanges(change.Number, change.Current_revision)
					if err != nil {
						return err
					}
					changeAdded := false
					// get the top one and also build a map
					for _, relatedChange := range relatedChanges.Changes {
						if !changeAdded {
							if c, ok := topicChanges[relatedChange.Change_id]; ok {
								changes = append(changes, c)
								changeAdded = true
							}
						}
						relatedChangesMap[relatedChange.Change_id] = struct{}{}
					}
					break
				}
				// check if all the CLs contained in topic are in related CL list
				for changeId, change := range topicChanges {
					if _, ok := relatedChangesMap[changeId]; !ok {
						var cn []string
						for _, c := range topicChanges {
							cn = append(cn, strconv.Itoa(c.Number))
						}
						return fmt.Errorf("Not all of the changes (%s) for project %q and topic %q are related to each other", strings.Join(cn, ","), change.Project, arg)
					}
				}
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
			if projectToPatch := findProject(jirix, change.Project, projects, host, hostUrl, g.GetChangeURL(change.Number)); projectToPatch != nil {
				if ok, err := patchProject(jirix, *projectToPatch, ref, branch, change.Branch); err != nil {
					return err
				} else if ok {
					if patchRebaseFlag {
						if err := rebaseProject(jirix, *projectToPatch, change.Branch); err != nil {
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
	// In the case where jiri is called programatically by a recipe,
	// we want to make it clear to the recipe if all failures were rebase errors.
	if rebaseFailures != 0 && rebaseFailures == jirix.Failures() {
		return rebaseFailedErr
	} else if jirix.Failures() != 0 {
		return fmt.Errorf("Patch failed")
	}
	return nil
}
