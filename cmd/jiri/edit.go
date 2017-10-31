// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
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

type arrayFlag []string

func (i *arrayFlag) String() string {
	return strings.Join(*i, ", ")
}

func (i *arrayFlag) Set(value string) error {
	*i = append(*i, value)
	return nil
}

var editFlags struct {
	projects   arrayFlag
	imports    arrayFlag
	jsonOutput string
}

type projectChanges struct {
	Name   string `json:"name"`
	Remote string `json:"remote"`
	Path   string `json:"path"`
	OldRev string `json:"old_revision"`
	NewRev string `json:"new_revision"`
}

type importChanges struct {
	Name   string `json:"name"`
	Remote string `json:"remote"`
	OldRev string `json:"old_revision"`
	NewRev string `json:"new_revision"`
}

type editChanges struct {
	Projects []projectChanges `json:"projects"`
	Imports  []importChanges  `json:"imports"`
}

func (ec *editChanges) toFile(filename string) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(ec, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize JSON output: %s\n", err)
	}

	err = ioutil.WriteFile(filename, out, 0600)
	if err != nil {
		return fmt.Errorf("failed write JSON output to %s: %s\n", filename, err)
	}

	return nil
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
	flags.Var(&editFlags.projects, "project", "List of projects to update. It is of form <project-name>=<newRef> where newRef is optional. It can be specified multiple times.")
	flags.Var(&editFlags.imports, "import", "List of imports to update. It is of form <import-name>=<newRef> where newRef is optional. It can be specified multiple times.")
	flags.StringVar(&editFlags.jsonOutput, "json-output", "", "File to print changes to, in json format.")
}

func runEdit(jirix *jiri.X, args []string) error {
	if len(args) != 1 {
		return jirix.UsageErrorf("Wrong number of args")
	}
	manifestPath, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	if len(editFlags.projects) == 0 && len(editFlags.imports) == 0 {
		return jirix.UsageErrorf("Please provide -project or/and -import flag")
	}
	projects := make(map[string]string)
	imports := make(map[string]string)
	for _, p := range editFlags.projects {
		s := strings.SplitN(p, "=", 2)
		if len(s) == 1 {
			projects[s[0]] = ""
		} else {
			projects[s[0]] = s[1]
		}
	}
	for _, i := range editFlags.imports {
		s := strings.SplitN(i, "=", 2)
		if len(s) == 1 {
			imports[s[0]] = ""
		} else {
			imports[s[0]] = s[1]
		}
	}

	return updateManifest(jirix, manifestPath, projects, imports)
}

func updateRevision(manifestContent, tag, currentRevision, newRevision, name string) (string, error) {
	if currentRevision != "" && currentRevision != "HEAD" {
		return strings.Replace(manifestContent, currentRevision, newRevision, 1), nil
	}
	r, err := regexp.Compile(fmt.Sprintf("( *?)<%s [^<]*?name=%q(.|\\n)*?\\/>", tag, name))
	if err != nil {
		return "", err
	}
	t := r.FindStringSubmatch(manifestContent)
	if t == nil {
		return "", fmt.Errorf("Not able to match %s %q", tag, name)
	}
	s := t[0]
	spaces := t[1]
	for i := 0; i < len(tag); i++ {
		spaces = spaces + " "
	}
	us := strings.Replace(s, "/>", fmt.Sprintf("\n%s  revision=%q/>", spaces, newRevision), 1)
	return strings.Replace(manifestContent, s, us, 1), nil
}

func updateManifest(jirix *jiri.X, manifestPath string, projects, imports map[string]string) error {
	ec := &editChanges{
		Projects: []projectChanges{},
		Imports:  []importChanges{},
	}

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
		newRevision := ""
		if rev, ok := projects[p.Name]; !ok {
			continue
		} else {
			newRevision = rev
		}
		if newRevision == "" {
			branch := "master"
			if p.RemoteBranch != "" {
				branch = p.RemoteBranch
			}
			out, err := scm.LsRemote(p.Remote, fmt.Sprintf("refs/heads/%s", branch))
			if err != nil {
				return err
			}
			newRevision = strings.Fields(string(out))[0]
		}
		if p.Revision == newRevision {
			continue
		}
		manifestContent, err = updateRevision(manifestContent, "project", p.Revision, newRevision, p.Name)
		if err != nil {
			return err
		}
		ec.Projects = append(ec.Projects, projectChanges{
			Name:   p.Name,
			Remote: p.Remote,
			Path:   p.Path,
			OldRev: p.Revision,
			NewRev: newRevision,
		})
	}

	for _, i := range m.Imports {
		newRevision := ""
		if rev, ok := imports[i.Name]; !ok {
			continue
		} else {
			newRevision = rev
		}
		if newRevision == "" {
			branch := "master"
			if i.RemoteBranch != "" {
				branch = i.RemoteBranch
			}
			out, err := scm.LsRemote(i.Remote, fmt.Sprintf("refs/heads/%s", branch))
			if err != nil {
				return err
			}
			newRevision = strings.Fields(string(out))[0]
		}
		if i.Revision == newRevision {
			continue
		}
		manifestContent, err = updateRevision(manifestContent, "import", i.Revision, newRevision, i.Name)
		if err != nil {
			return err
		}
		ec.Imports = append(ec.Imports, importChanges{
			Name:   i.Name,
			Remote: i.Remote,
			OldRev: i.Revision,
			NewRev: newRevision,
		})
	}
	if editFlags.jsonOutput != "" {
		if err := ec.toFile(editFlags.jsonOutput); err != nil {
			return err
		}
	}

	return ioutil.WriteFile(manifestPath, []byte(manifestContent), os.ModePerm)
}
