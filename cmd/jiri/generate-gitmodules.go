// Copyright 2019 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"sort"
	"strings"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/project"
)

var cmdGenGitModule = &cmdline.Command{
	Runner: jiri.RunnerFunc(runGenGitModule),
	Name:   "generate-gitmodules",
	Short:  "Create a .gitmodule file for git submodule repository",
	Long: `
The "jiri generate-gitmodules <.gitmodule path>" command captures the current project state
and create a .gitmodules file.
`,
	ArgsName: "<.gitmodule path>",
	ArgsLong: "<.gitmodule path> is the path to the output .gitmodule file.",
}

var genGitModuleFlags struct {
	genScript    string
	redirectRoot bool
}

func init() {
	flags := &cmdGenGitModule.Flags
	flags.StringVar(&genGitModuleFlags.genScript, "generate-script", "", "File to save generated git commands for seting up a superproject.")
	flags.BoolVar(&genGitModuleFlags.redirectRoot, "redir-root", false, "When set to true, jiri will add the root repository as a submodule into {name}-mirror directory and create necessary setup commands in generated script.")
}

type projectTree struct {
	project  *project.Project
	children map[string]*projectTree
}

type projectTreeRoot struct {
	root    *projectTree
	dropped project.Projects
}

func runGenGitModule(jirix *jiri.X, args []string) error {
	var gitmodulesPath = ".gitmodules"
	if len(args) == 1 {
		gitmodulesPath = args[0]
	}
	if len(args) > 1 {
		return jirix.UsageErrorf("unexpected number of arguments")
	}

	localProjects, err := project.LocalProjects(jirix, project.FullScan)
	if err != nil {
		return err
	}
	return writeGitModules(jirix, localProjects, gitmodulesPath)
}

func (p *projectTreeRoot) add(jirix *jiri.X, proj project.Project) error {
	if p == nil || p.root == nil {
		return errors.New("add called with nil root pointer")
	}

	if proj.Path == "." || proj.Path == "" || proj.Path == string(filepath.Separator) {
		// Skip fuchsia.git project
		p.dropped[proj.Key()] = proj
		return nil
	}

	// git submodule does not support one submodule to be placed under the path
	// of another submodule, therefore, it is necessary to detect nested
	// projects in jiri manifests and drop them from gitmodules file.
	//
	// The nested project detection is based on only 1 rule:
	// If the path of project A (pathA) is the parent directory of project B,
	// project B will be considered as nested under project A. It will be recorded
	// in "dropped" map.
	//
	// Due to the introduction of fuchsia.git, based on the rule above, all
	// other projects will be considered as nested project under fuchsia.git,
	// therefore, fuchsia.git is excluded in this detection process.
	//
	// The detection algorithm works in following ways:
	//
	// Assuming we have two project: "projA" and "projB", "projA" is located at
	// "$JIRI_ROOT/a" and projB is located as "$JIRI_ROOT/b/c".
	// The projectTree will look like the following chart:
	//
	//                   a    +-------+
	//               +--------+ projA |
	//               |        +-------+
	// +---------+   |
	// |nil(root)+---+
	// +---------+   |
	//               |   b    +-------+   c   +-------+
	//               +--------+  nil  +-------+ projB |
	//                        +-------+       +-------+
	//
	// The text inside each block represents the projectTree.project field,
	// each edge represents a key of projectTree.children field.
	//
	// Assuming we adds project "projC" whose path is "$JIRI_ROOT/a/d", it will
	// be dropped as the children of root already have key "a" and
	// children["a"].project is not pointed to nil, which means "projC" is
	// nested under "projA".
	//
	// Assuming we adds project "projD" whose path is "$JIRI_ROOT/d", it will
	// be added successfully since root.children does not have key "d" yet,
	// which means "projD" is not nested under any known project and no project
	// is currently nested under "projD" yet.
	//
	// Assuming we adds project "projE" whose path is "$JIRI_ROOT/b", it will
	// be added successfully and "projB" will be dropped. The reason is that
	// root.children["b"].project is nil but root.children["b"].children is not
	// empty, so any projects that can be reached from root.children["b"]
	// should be dropped as they are nested under "projE".
	elmts := strings.Split(proj.Path, string(filepath.Separator))
	pin := p.root
	for i := 0; i < len(elmts); i++ {
		if child, ok := pin.children[elmts[i]]; ok {
			if child.project != nil {
				// proj is nested under next.project, drop proj
				jirix.Logger.Debugf("project %q:%q nested under project %q:%q", proj.Path, proj.Remote, proj.Path, child.project.Remote)
				p.dropped[proj.Key()] = proj
				return nil
			}
			pin = child
		} else {
			child = &projectTree{nil, make(map[string]*projectTree)}
			pin.children[elmts[i]] = child
			pin = child
		}
	}
	if len(pin.children) != 0 {
		// There is one or more project nested under proj.
		jirix.Logger.Debugf("following project nested under project %q:%q", proj.Path, proj.Remote)
		if err := p.prune(jirix, pin); err != nil {
			return err
		}
		jirix.Logger.Debugf("\n")
	}
	pin.project = &proj
	return nil
}

func (p *projectTreeRoot) prune(jirix *jiri.X, node *projectTree) error {
	// Looking for projects nested under node using BFS
	workList := make([]*projectTree, 0)
	workList = append(workList, node)

	for len(workList) > 0 {
		item := workList[0]
		if item == nil {
			return errors.New("purgeLeaves encountered a nil node")
		}
		workList = workList[1:]
		if item.project != nil {
			p.dropped[item.project.Key()] = *item.project
			jirix.Logger.Debugf("\tnested project %q:%q", item.project.Path, item.project.Remote)
		}
		for _, v := range item.children {
			workList = append(workList, v)
		}
	}

	// Purge leaves under node
	node.children = make(map[string]*projectTree)
	return nil
}

func writeGitModules(jirix *jiri.X, projects project.Projects, outputPath string) error {
	projEntries := make([]project.Project, len(projects))

	// relativaize the paths and copy projects from map to slice for sorting.
	i := 0
	for _, v := range projects {
		relPath, err := makePathRel(jirix.Root, v.Path)
		if err != nil {
			return err
		}
		v.Path = relPath
		projEntries[i] = v
		i++
	}
	sort.Slice(projEntries, func(i, j int) bool {
		return string(projEntries[i].Key()) < string(projEntries[j].Key())
	})

	// Create path prefix tree to collect all nested projects
	root := projectTree{nil, make(map[string]*projectTree)}
	treeRoot := projectTreeRoot{&root, make(project.Projects)}
	for _, v := range projEntries {
		if err := treeRoot.add(jirix, v); err != nil {
			return err
		}
	}

	// Start creating .gitmodule and set up script.
	var gitmoduleBuf bytes.Buffer
	var commandBuf bytes.Buffer
	commandBuf.WriteString("#/!bin/sh\n")

	// Special hack for fuchsia.git
	// When -redir-root is set to true, fuchsia.git will be added as submodule
	// to fuchsia-mirror directory
	reRootRepoName := ""
	if genGitModuleFlags.redirectRoot {
		// looking for root repository, there should be no more than 1
		rIndex := -1
		for i, v := range projEntries {
			if v.Path == "." || v.Path == "" || v.Path == string(filepath.Separator) {
				if rIndex == -1 {
					rIndex = i
				} else {
					return fmt.Errorf("more than 1 project defined at path \".\", projects %+v:%+v", projEntries[rIndex], projEntries[i])
				}
			}
		}
		if rIndex != -1 {
			v := projEntries[rIndex]
			v.Name = v.Name + "-mirror"
			v.Path = v.Name
			reRootRepoName = v.Path
			gitmoduleBuf.WriteString(moduleDecl(v))
			gitmoduleBuf.WriteString("\n")
			commandBuf.WriteString(commandDecl(v))
			commandBuf.WriteString("\n")
		}
	}

	for _, v := range projEntries {
		if reRootRepoName != "" && reRootRepoName == v.Path {
			return fmt.Errorf("path collision for root repo and project %+v", v)
		}
		if _, ok := treeRoot.dropped[v.Key()]; ok {
			jirix.Logger.Debugf("dropped project %+v", v)
			continue
		}
		gitmoduleBuf.WriteString(moduleDecl(v))
		gitmoduleBuf.WriteString("\n")
		commandBuf.WriteString(commandDecl(v))
		commandBuf.WriteString("\n")
	}
	jirix.Logger.Debugf("generated gitmodule content \n%v\n", gitmoduleBuf.String())
	if err := ioutil.WriteFile(outputPath, gitmoduleBuf.Bytes(), 0644); err != nil {
		return err
	}

	if genGitModuleFlags.genScript != "" {
		jirix.Logger.Debugf("generated set up script for gitmodule content \n%v\n", commandBuf.String())
		if err := ioutil.WriteFile(genGitModuleFlags.genScript, commandBuf.Bytes(), 0755); err != nil {
			return err
		}
	}
	return nil
}

func makePathRel(basepath, targpath string) (string, error) {
	if filepath.IsAbs(targpath) {
		relPath, err := filepath.Rel(basepath, targpath)
		if err != nil {
			return "", err
		}
		return relPath, nil
	}
	return targpath, nil
}

func moduleDecl(p project.Project) string {
	tmpl := "[submodule \"%s\"]\n\tbranch = %s\n\tpath = %s\n\turl = %s"
	return fmt.Sprintf(tmpl, p.Name, p.Revision, p.Path, p.Remote)
}

func commandDecl(p project.Project) string {
	tmpl := "git update-index --add --cacheinfo 160000 %s \"%s\""
	return fmt.Sprintf(tmpl, p.Revision, p.Path)
}
