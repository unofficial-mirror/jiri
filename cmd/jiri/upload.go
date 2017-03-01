// Copyright 2016 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"net/url"
	"strings"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/gerrit"
	"fuchsia.googlesource.com/jiri/git"
	"fuchsia.googlesource.com/jiri/gitutil"
)

var (
	uploadCcsFlag       string
	uploadHostFlag      string
	uploadPresubmitFlag string
	uploadReviewersFlag string
	uploadTopicFlag     string
	uploadVerifyFlag    bool
	uploadRebaseFlag    bool
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
	cmdUpload.Flags.StringVar(&uploadTopicFlag, "topic", "", `CL topic.`)
	cmdUpload.Flags.BoolVar(&uploadVerifyFlag, "verify", true, `Run pre-push git hooks.`)
	cmdUpload.Flags.BoolVar(&uploadRebaseFlag, "rebase", false, `Run rebase before pushing.`)
}

// runUpload is a wrapper that pushes the changes to gerrit for review.
func runUpload(jirix *jiri.X, _ []string) error {
	p, err := currentProject(jirix)
	if err != nil {
		return err
	}
	scm := gitutil.New(jirix.NewSeq(), gitutil.RootDirOpt(p.Path))
	if !scm.IsOnBranch() {
		return fmt.Errorf("The project is not on any branch.")
	}
	remoteBranch, err := scm.RemoteBranchName()
	if err != nil {
		return err
	}
	if remoteBranch == "" {
		return fmt.Errorf("Current branch is un-tracked or tracks a local un-tracked branch.")
	}
	if uploadRebaseFlag {
		if changes, err := scm.HasUncommittedChanges(); err != nil {
			return err
		} else if changes {
			return fmt.Errorf("project has uncommited changes, please commit them or stash them. Cannot rebase before pushing.")
		}
	}

	host := uploadHostFlag
	if host == "" {
		if p.GerritHost == "" {
			return fmt.Errorf("No gerrit host found.  Please use the '--host' flag, or add a 'gerrithost' attribute for project %q.", p.Name)
		}
		host = p.GerritHost
	}
	hostUrl, err := url.Parse(host)
	if err != nil {
		return fmt.Errorf("invalid Gerrit host %q: %v", host, err)
	}
	projectRemoteUrl, err := url.Parse(p.Remote)
	if err != nil {
		return fmt.Errorf("invalid project remote: %v", p.Remote, err)
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
		Topic:        uploadTopicFlag,
	}
	branch, err := gitutil.New(jirix.NewSeq()).CurrentBranchName()
	if err != nil {
		return err
	}
	opts.Branch = branch

	if opts.Presubmit == gerrit.PresubmitTestType("") {
		opts.Presubmit = gerrit.PresubmitTestTypeAll
	}

	if uploadRebaseFlag {
		g := git.NewGit(p.Path)
		if err := g.Fetch("origin", git.PruneOpt(true)); err != nil {
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
			return fmt.Errorf("Not able to rebase the branch to %v, please rebase manually: %v", trackingBranch, err)
		}
	}

	if err := gerrit.Push(jirix.NewSeq(), opts); err != nil {
		return gerritError(err.Error())
	}
	return nil
}
