// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/project"
)

type stringsValue []string

func (i *stringsValue) String() string {
	return strings.Join(*i, ",")
}

func (i *stringsValue) Set(value string) error {
	*i = strings.Split(value, ",")
	return nil
}

var editFlags struct {
	projects stringsValue
}

var cmdEdit = &cmdline.Command{
	Runner:   jiri.RunnerFunc(runEdit),
	Name:     "edit",
	Short:    "Edit manifest file",
	Long:     `Edit manifest file by rolling the revision of provided projects`,
	ArgsName: "<manifest>",
	ArgsLong: "<manifest> is path of the manifest",
}

func init() {
	flags := &cmdEdit.Flags
	flags.Var(&editFlags.projects, "projects", "List of projects to update")
}

func runEdit(jirix *jiri.X, args []string) error {
	if len(args) != 1 {
		return jirix.UsageErrorf("Wrong number of args")
	}
	manifestPath, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	projects := make(map[string]struct{})
	for _, p := range editFlags.projects {
		projects[p] = struct{}{}
	}

	return updateManifest(jirix, manifestPath, projects)
}

func updateManifest(jirix *jiri.X, manifestPath string, projects map[string]struct{}) error {
	m, err := project.ManifestFromFile(jirix, manifestPath)
	if err != nil {
		return err
	}
	content, err := ioutil.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	manifestContent := string(content)
	scm := gitutil.New(jirix, gitutil.RootDirOpt(filepath.Dir(manifestPath)))
	for _, p := range m.Projects {
		if _, ok := projects[p.Name]; !ok {
			continue
		}
		branch := "master"
		if p.RemoteBranch != "" {
			branch = p.RemoteBranch
		}
		out, err := scm.LsRemote(p.Remote, fmt.Sprintf("refs/heads/%s", branch))
		if err != nil {
			return err
		}
		latestRevision := strings.Fields(string(out))[0]
		if p.Revision != "" && p.Revision != "HEAD" {
			manifestContent = strings.Replace(manifestContent, p.Revision, latestRevision, 1)
		} else {
			r, err := regexp.Compile(fmt.Sprintf("( *?)<project (.|\\n)*?name=%q(.|\\n)*?\\/>", p.Name))
			if err != nil {
				return err
			}
			t := r.FindStringSubmatch(manifestContent)
			if t == nil {
				return fmt.Errorf("Not able to match project %q", p.Name)
			}
			s := t[0]
			spaces := t[1]
			us := strings.Replace(s, "/>", fmt.Sprintf("\n%s         revision=%q/>", spaces, latestRevision), 1)
			manifestContent = strings.Replace(manifestContent, s, us, 1)
		}
	}

	return ioutil.WriteFile(manifestPath, []byte(manifestContent), os.ModePerm)
}
