// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"sort"
	"text/template"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/project"
)

var (
	cleanAllFlag   bool
	cleanupFlag    bool
	jsonOutputFlag string
	regexpFlag     bool
	templateFlag   string
)

func init() {
	cmdProject.Flags.BoolVar(&cleanAllFlag, "clean-all", false, "Restore jiri projects to their pristine state and delete all branches.")
	cmdProject.Flags.BoolVar(&cleanupFlag, "clean", false, "Restore jiri projects to their pristine state.")
	cmdProject.Flags.StringVar(&jsonOutputFlag, "json-output", "", "Path to write operation results to.")
	cmdProject.Flags.BoolVar(&regexpFlag, "regexp", false, "Use argument as regular expression.")
	cmdProject.Flags.StringVar(&templateFlag, "template", "", "The template for the fields to display.")
}

// cmdProject represents the "jiri project" command.
var cmdProject = &cmdline.Command{
	Runner: jiri.RunnerFunc(runProject),
	Name:   "project",
	Short:  "Manage the jiri projects",
	Long: `Cleans all projects if -clean flag is provided else inspect
	the local filesystem and provide structured info on the existing
	projects and branches. Projects are specified using either names or
	regular expressions that are matched against project names. If no
	command line arguments are provided the project that the contains the
	current directory is used, or if run from outside of a given project,
	all projects will be used. The information to be displayed can be
	specified using a Go template, supplied via
the -template flag.`,
	ArgsName: "<project ...>",
	ArgsLong: "<project ...> is a list of projects to clean up or give info about.",
}

func runProject(jirix *jiri.X, args []string) (e error) {
	if cleanupFlag || cleanAllFlag {
		return runProjectClean(jirix, args)
	} else {
		return runProjectInfo(jirix, args)
	}
}
func runProjectClean(jirix *jiri.X, args []string) (e error) {
	localProjects, err := project.LocalProjects(jirix, project.FullScan)
	if err != nil {
		return err
	}
	projects := make(project.Projects)
	if len(args) > 0 {
		if regexpFlag {
			for _, a := range args {
				re, err := regexp.Compile(a)
				if err != nil {
					return fmt.Errorf("failed to compile regexp %v: %v", a, err)
				}
				for _, p := range localProjects {
					if re.MatchString(p.Name) {
						projects[p.Key()] = p
					}
				}
			}
		} else {
			for _, arg := range args {
				p, err := localProjects.FindUnique(arg)
				if err != nil {
					fmt.Fprintf(jirix.Stderr(), "Error finding local project %q: %v.\n", p.Name, err)
				} else {
					projects[p.Key()] = p
				}
			}
		}
	} else {
		projects = localProjects
	}
	if err := project.CleanupProjects(jirix, projects, cleanAllFlag); err != nil {
		return err
	}
	return nil
}

// infoOutput defines JSON format for 'project info' output.
type infoOutput struct {
	Name          string   `json:"name"`
	Path          string   `json:"path"`
	Remote        string   `json:"remote"`
	Revision      string   `json:"revision"`
	CurrentBranch string   `json:"current_branch,omitempty"`
	Branches      []string `json:"branches,omitempty"`
}

// runProjectInfo provides structured info on local projects.
func runProjectInfo(jirix *jiri.X, args []string) error {
	var tmpl *template.Template
	var err error
	if templateFlag != "" {
		tmpl, err = template.New("info").Parse(templateFlag)
		if err != nil {
			return fmt.Errorf("failed to parse template %q: %v", templateFlag, err)
		}
	}

	regexps := []*regexp.Regexp{}
	if len(args) > 0 && regexpFlag {
		regexps = make([]*regexp.Regexp, len(args), len(args))
		for i, a := range args {
			re, err := regexp.Compile(a)
			if err != nil {
				return fmt.Errorf("failed to compile regexp %v: %v", a, err)
			}
			regexps[i] = re
		}
	}

	var states map[project.ProjectKey]*project.ProjectState
	var keys project.ProjectKeys
	projects, err := project.LocalProjects(jirix, project.FastScan)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		currentProject, err := project.CurrentProject(jirix)
		if err != nil {
			return err
		}
		if currentProject == nil {
			// jiri was run from outside of a project so let's
			// use all available projects.
			states, err = project.GetProjectStates(jirix, projects, false)
			if err != nil {
				return err
			}
			for key := range states {
				keys = append(keys, key)
			}
		} else {
			state, err := project.GetProjectState(jirix, *currentProject, true)
			if err != nil {
				return err
			}
			states = map[project.ProjectKey]*project.ProjectState{
				currentProject.Key(): state,
			}
			keys = append(keys, currentProject.Key())
		}
	} else {
		var err error
		states, err = project.GetProjectStates(jirix, projects, false)
		if err != nil {
			return err
		}
		for key, state := range states {
			if regexpFlag {
				for _, re := range regexps {
					if re.MatchString(state.Project.Name) {
						keys = append(keys, key)
						break
					}
				}
			} else {
				for _, arg := range args {
					if arg == state.Project.Name {
						keys = append(keys, key)
						break
					}
				}
			}
		}
	}
	sort.Sort(keys)

	info := make([]infoOutput, len(keys))
	for i, key := range keys {
		state := states[key]
		info[i] = infoOutput{
			Name:          state.Project.Name,
			Path:          state.Project.Path,
			Remote:        state.Project.Remote,
			Revision:      state.Project.Revision,
			CurrentBranch: state.CurrentBranch.Name,
		}
		for _, b := range state.Branches {
			info[i].Branches = append(info[i].Branches, b.Name)
		}
	}

	for _, i := range info {
		if templateFlag != "" {
			out := &bytes.Buffer{}
			if err := tmpl.Execute(out, i); err != nil {
				return jirix.UsageErrorf("invalid format")
			}
			fmt.Fprintln(os.Stdout, out.String())
		} else {
			fmt.Printf("* project %s\n", i.Name)
			fmt.Printf("  Path:     %s\n", i.Path)
			fmt.Printf("  Remote:   %s\n", i.Remote)
			fmt.Printf("  Revision: %s\n", i.Revision)
			if len(i.Branches) != 0 {
				fmt.Printf("  Branches:\n")
				width := 0
				for _, b := range i.Branches {
					if len(b) > width {
						width = len(b)
					}
				}
				for _, b := range i.Branches {
					fmt.Printf("    %-*s", width, b)
					if i.CurrentBranch == b {
						fmt.Printf(" current")
					}
					fmt.Println()
				}
			} else {
				fmt.Printf("  Branches: none\n")
			}
		}
	}

	if jsonOutputFlag != "" {
		if err := writeJSONOutput(info); err != nil {
			return err
		}
	}

	return nil
}

func writeJSONOutput(result interface{}) error {
	out, err := json.MarshalIndent(&result, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize JSON output: %s\n", err)
	}

	err = ioutil.WriteFile(jsonOutputFlag, out, 0600)
	if err != nil {
		return fmt.Errorf("failed write JSON output to %s: %s\n", jsonOutputFlag, err)
	}

	return nil
}
