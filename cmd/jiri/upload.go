// Copyright 2016 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/gerrit"
	"fuchsia.googlesource.com/jiri/git"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/project"
)

var (
	uploadCcsFlag       string
	uploadHostFlag      string
	uploadPresubmitFlag string
	uploadReviewersFlag string
	uploadTopicFlag     string
	uploadVerifyFlag    bool
	uploadRebaseFlag    bool
	uploadSetTopicFlag  bool
	uploadMultipartFlag bool
)

var cmdUpload = &cmdline.Command{
	Runner: jiri.RunnerFunc(runUpload),
	Name:   "upload",
	Short:  "Upload a changelist for review",
	Long:   `Command "upload" uploads all commits of a local branch to Gerrit.`,
}

func init() {
	cmdUpload.Flags.StringVar(&uploadCcsFlag, "cc", "", `Comma-separated list of emails or LDAPs to cc.`)
	cmdUpload.Flags.StringVar(&uploadHostFlag, "host", "", `Gerrit host to use.  Defaults to gerrit host specified in manifest.`)
	cmdUpload.Flags.StringVar(&uploadPresubmitFlag, "presubmit", string(gerrit.PresubmitTestTypeAll),
		fmt.Sprintf("The type of presubmit tests to run. Valid values: %s.", strings.Join(gerrit.PresubmitTestTypes(), ",")))
	cmdUpload.Flags.StringVar(&uploadReviewersFlag, "r", "", `Comma-separated list of emails or LDAPs to request review.`)
	cmdUpload.Flags.StringVar(&uploadTopicFlag, "topic", "", `CL topic. Default is <username>-<branchname>.`)
	cmdUpload.Flags.BoolVar(&uploadSetTopicFlag, "set-topic", true, `Set topic.`)
	cmdUpload.Flags.BoolVar(&uploadVerifyFlag, "verify", true, `Run pre-push git hooks.`)
	cmdUpload.Flags.BoolVar(&uploadRebaseFlag, "rebase", false, `Run rebase before pushing.`)
	cmdUpload.Flags.BoolVar(&uploadMultipartFlag, "multipart", false, `Send multipart CL.`)
}

// runUpload is a wrapper that pushes the changes to gerrit for review.
func runUpload(jirix *jiri.X, _ []string) error {
	p, err := currentProject(jirix)
	if err != nil {
		return err
	}
	scm := gitutil.New(jirix, gitutil.RootDirOpt(p.Path))
	if !scm.IsOnBranch() {
		return fmt.Errorf("The project is not on any branch.")
	}

	currentBranch, err := scm.CurrentBranchName()
	if err != nil {
		return err
	}
	var projectsToProcess []project.Project
	topic := ""
	if uploadSetTopicFlag {
		if topic = uploadTopicFlag; topic == "" {
			topic = fmt.Sprintf("%s-%s", os.Getenv("USER"), currentBranch) // use <username>-<branchname> as the default
		}
	}
	if uploadMultipartFlag {
		projects, err := project.LocalProjects(jirix, project.FastScan)
		if err != nil {
			return err
		}
		for _, project := range projects {
			scm := gitutil.New(jirix, gitutil.RootDirOpt(project.Path))
			if scm.IsOnBranch() {
				branch, err := scm.CurrentBranchName()
				if err != nil {
					return err
				}
				if currentBranch == branch {
					projectsToProcess = append(projectsToProcess, project)
				}
			}
		}

	} else {
		if project, err := currentProject(jirix); err != nil {
			return err
		} else {
			projectsToProcess = append(projectsToProcess, project)
		}
	}
	type GerritPushOption struct {
		Project      project.Project
		CLOpts       gerrit.CLOpts
		relativePath string
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	var gerritPushOptions []GerritPushOption
	for _, project := range projectsToProcess {
		scm := gitutil.New(jirix, gitutil.RootDirOpt(project.Path))
		relativePath, err := filepath.Rel(cwd, project.Path)
		if err != nil {
			// Just use the full path if an error occurred.
			relativePath = project.Path
		}
		if uploadRebaseFlag {
			if changes, err := git.NewGit(project.Path).HasUncommittedChanges(); err != nil {
				return err
			} else if changes {
				return fmt.Errorf("Project %s(%s) has uncommited changes, please commit them or stash them. Cannot rebase before pushing.", project.Name, relativePath)
			}
		}
		remoteBranch, err := scm.RemoteBranchName()
		if err != nil {
			return err
		}
		if remoteBranch == "" {
			return fmt.Errorf("For project %s(%s), current branch is un-tracked or tracks a local un-tracked branch.", project.Name, relativePath)
		}

		host := uploadHostFlag
		if host == "" {
			if project.GerritHost == "" {
				return fmt.Errorf("No gerrit host found.  Please use the '--host' flag, or add a 'gerrithost' attribute for project %s(%s).", project.Name, relativePath)
			}
			host = project.GerritHost
		}
		hostUrl, err := url.Parse(host)
		if err != nil {
			return fmt.Errorf("invalid Gerrit host for project %s(%s) %q: %s", project.Name, relativePath, host, err)
		}
		projectRemoteUrl, err := url.Parse(project.Remote)
		if err != nil {
			return fmt.Errorf("invalid project remote for project %s(%s): %s", project.Name, relativePath, project.Remote, err)
		}
		gerritRemote := *hostUrl
		gerritRemote.Path = projectRemoteUrl.Path
		opts := gerrit.CLOpts{
			Ccs:          parseEmails(uploadCcsFlag),
			Host:         hostUrl,
			Presubmit:    gerrit.PresubmitTestType(uploadPresubmitFlag),
			RemoteBranch: remoteBranch,
			Remote:       gerritRemote.String(),
			Reviewers:    parseEmails(uploadReviewersFlag),
			Verify:       uploadVerifyFlag,
			Topic:        topic,
			Branch:       currentBranch,
		}

		if opts.Presubmit == gerrit.PresubmitTestType("") {
			opts.Presubmit = gerrit.PresubmitTestTypeAll
		}
		gerritPushOptions = append(gerritPushOptions, GerritPushOption{project, opts, relativePath})
	}

	// Rebase all projects before pushing
	if uploadRebaseFlag {
		for _, gerritPushOption := range gerritPushOptions {
			scm := gitutil.New(jirix, gitutil.RootDirOpt(gerritPushOption.Project.Path))
			if err := scm.Fetch("origin"); err != nil {
				return err
			}
			trackingBranch, err := scm.TrackingBranchName()
			if err != nil {
				return err
			}
			if err = scm.Rebase(trackingBranch); err != nil {
				if err2 := scm.RebaseAbort(); err2 != nil {
					return err2
				}
				return fmt.Errorf("For project %s(%s), not able to rebase the branch to %s, please rebase manually: %s", gerritPushOption.Project.Name, gerritPushOption.relativePath, trackingBranch, err)
			}
		}
	}

	for _, gerritPushOption := range gerritPushOptions {
		fmt.Printf("Pushing project %s(%s)\n", gerritPushOption.Project.Name, gerritPushOption.relativePath)
		if err := gerrit.Push(jirix.NewSeq().Dir(gerritPushOption.Project.Path), gerritPushOption.CLOpts); err != nil {
			if strings.Contains(err.Error(), "(no new changes)") {
				if gitErr, ok := err.(gitutil.GitError); ok {
					fmt.Printf("%s", gitErr.Output)
					fmt.Printf("%s", gitErr.ErrorOutput)
				} else {
					return gerritError(err.Error())
				}
			} else {
				return gerritError(err.Error())
			}
		}
		fmt.Println()
	}
	return nil
}
