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
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/project"
)

var (
	branchesFlag        bool
	cleanupBranchesFlag bool
	noPristineFlag      bool
	checkDirtyFlag      bool
	showNameFlag        bool
	jsonOutputFlag      string
	regexpFlag          bool
	templateFlag        string
)

func init() {
	cmdProjectClean.Flags.BoolVar(&cleanupBranchesFlag, "branches", false, "Delete all non-master branches.")
	cmdProjectList.Flags.BoolVar(&branchesFlag, "branches", false, "Show project branches.")
	cmdProjectList.Flags.BoolVar(&noPristineFlag, "nopristine", false, "If true, omit pristine projects, i.e. projects with a clean master branch and no other branches.")
	cmdProjectShellPrompt.Flags.BoolVar(&checkDirtyFlag, "check-dirty", true, "If false, don't check for uncommitted changes or untracked files. Setting this option to false is dangerous: dirty master branches will not appear in the output.")
	cmdProjectShellPrompt.Flags.BoolVar(&showNameFlag, "show-name", false, "Show the name of the current repo.")
	cmdProjectInfo.Flags.StringVar(&jsonOutputFlag, "json-output", "", "Path to write operation results to.")
	cmdProjectInfo.Flags.BoolVar(&regexpFlag, "regexp", false, "Use argument as regular expression.")
	cmdProjectInfo.Flags.StringVar(&templateFlag, "template", "", "The template for the fields to display.")
}

// cmdProject represents the "jiri project" command.
var cmdProject = &cmdline.Command{
	Name:     "project",
	Short:    "Manage the jiri projects",
	Long:     "Manage the jiri projects.",
	Children: []*cmdline.Command{cmdProjectClean, cmdProjectInfo, cmdProjectList, cmdProjectShellPrompt},
}

// cmdProjectClean represents the "jiri project clean" command.
var cmdProjectClean = &cmdline.Command{
	Runner:   jiri.RunnerFunc(runProjectClean),
	Name:     "clean",
	Short:    "Restore jiri projects to their pristine state",
	Long:     "Restore jiri projects back to their master branches and get rid of all the local branches and changes.",
	ArgsName: "<project ...>",
	ArgsLong: "<project ...> is a list of projects to clean up.",
}

func runProjectClean(jirix *jiri.X, args []string) (e error) {
	localProjects, err := project.LocalProjects(jirix, project.FullScan)
	if err != nil {
		return err
	}
	var projects project.Projects
	if len(args) > 0 {
		for _, arg := range args {
			p, err := localProjects.FindUnique(arg)
			if err != nil {
				fmt.Fprintf(jirix.Stderr(), "Error finding local project %q: %v.\n", p.Name, err)
			} else {
				projects[p.Key()] = p
			}
		}
	} else {
		projects = localProjects
	}
	if err := project.CleanupProjects(jirix, projects, cleanupBranchesFlag); err != nil {
		return err
	}
	return nil
}

// cmdProjectList represents the "jiri project list" command.
var cmdProjectList = &cmdline.Command{
	Runner: jiri.RunnerFunc(runProjectList),
	Name:   "list",
	Short:  "List existing jiri projects and branches",
	Long:   "Inspect the local filesystem and list the existing projects and branches.",
}

// runProjectList generates a listing of local projects.
func runProjectList(jirix *jiri.X, _ []string) error {
	projects, err := project.LocalProjects(jirix, project.FastScan)
	if err != nil {
		return err
	}
	states, err := project.GetProjectStates(jirix, projects, noPristineFlag)
	if err != nil {
		return err
	}
	var keys project.ProjectKeys
	for key := range states {
		keys = append(keys, key)
	}
	sort.Sort(keys)

	for _, key := range keys {
		state := states[key]
		if noPristineFlag {
			pristine := len(state.Branches) == 1 && state.CurrentBranch.Name == "master" && !state.HasUncommitted && !state.HasUntracked
			if pristine {
				continue
			}
		}
		fmt.Fprintf(jirix.Stdout(), "name=%q remote=%q path=%q\n", state.Project.Name, state.Project.Remote, state.Project.Path)
		if branchesFlag {
			for _, branch := range state.Branches {
				s := "  "
				if branch.Name == state.CurrentBranch.Name {
					s += "* "
				}
				s += branch.Name
				fmt.Fprintf(jirix.Stdout(), "%v\n", s)
			}
		}
	}
	return nil
}

// cmdProjectInfo represents the "jiri project info" command.
var cmdProjectInfo = &cmdline.Command{
	Runner: jiri.RunnerFunc(runProjectInfo),
	Name:   "info",
	Short:  "Provided structured input for existing jiri projects and branches",
	Long: `
Inspect the local filesystem and provide structured info on the existing
projects and branches. Projects are specified using either names or regular
expressions that are matched against project names. If no command line
arguments are provided the project that the contains the current directory is
used, or if run from outside of a given project, all projects will be used. The
information to be displayed can be specified using a Go template, supplied via
the -template flag.`,
	ArgsName: "<project-names>...",
	ArgsLong: "<project-namess>... a list of project names",
}

// infoOutput defines JSON format for 'project info' output.
type infoOutput struct {
	Name          string   `json:"name"`
	Path          string   `json:"path"`
	Remote        string   `json:"remote"`
	Revision      string   `json:"revision"`
	CurrentBranch string   `json:"current_branch"`
	Branches      []string `json:"branches"`
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
		currentProjectKey, err := project.CurrentProjectKey(jirix)
		if err != nil {
			return err
		}
		state, err := project.GetProjectState(jirix, currentProjectKey, true)
		if err != nil {
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
			states = map[project.ProjectKey]*project.ProjectState{
				currentProjectKey: state,
			}
			keys = append(keys, currentProjectKey)
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

// cmdProjectShellPrompt represents the "jiri project shell-prompt" command.
var cmdProjectShellPrompt = &cmdline.Command{
	Runner: jiri.RunnerFunc(runProjectShellPrompt),
	Name:   "shell-prompt",
	Short:  "Print a succinct status of projects suitable for shell prompts",
	Long: `
Reports current branches of jiri projects (repositories) as well as an
indication of each project's status:
  *  indicates that a repository contains uncommitted changes
  %  indicates that a repository contains untracked files
`,
}

func runProjectShellPrompt(jirix *jiri.X, args []string) error {
	projects, err := project.LocalProjects(jirix, project.FastScan)
	if err != nil {
		return err
	}
	states, err := project.GetProjectStates(jirix, projects, checkDirtyFlag)
	if err != nil {
		return err
	}
	var keys project.ProjectKeys
	for key := range states {
		keys = append(keys, key)
	}
	sort.Sort(keys)

	// Get the key of the current project.
	currentProjectKey, err := project.CurrentProjectKey(jirix)
	if err != nil {
		return err
	}
	var statuses []string
	for _, key := range keys {
		state := states[key]
		status := ""
		if checkDirtyFlag {
			if state.HasUncommitted {
				status += "*"
			}
			if state.HasUntracked {
				status += "%"
			}
		}
		short := state.CurrentBranch.Name + status
		long := filepath.Base(states[key].Project.Name) + ":" + short
		if key == currentProjectKey {
			if showNameFlag {
				statuses = append([]string{long}, statuses...)
			} else {
				statuses = append([]string{short}, statuses...)
			}
		} else {
			pristine := state.CurrentBranch.Name == "master"
			if checkDirtyFlag {
				pristine = pristine && !state.HasUncommitted && !state.HasUntracked
			}
			if !pristine {
				statuses = append(statuses, long)
			}
		}
	}
	fmt.Println(strings.Join(statuses, ","))
	return nil
}
