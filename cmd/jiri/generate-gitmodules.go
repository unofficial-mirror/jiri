// Copyright 2019 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"strings"

	"go.fuchsia.dev/jiri"
	"go.fuchsia.dev/jiri/cmdline"
	"go.fuchsia.dev/jiri/project"
)

var cmdGenGitModule = &cmdline.Command{
	Runner: jiri.RunnerFunc(runGenGitModule),
	Name:   "generate-gitmodules",
	Short:  "Create a .gitmodule and a .gitattributes files for git submodule repository",
	Long: `
The "jiri generate-gitmodules command captures the current project state and
create a .gitmodules file and an optional .gitattributes file for building
a git submodule based super repository.
`,
	ArgsName: "<.gitmodule path> [<.gitattributes path>]",
	ArgsLong: `
<.gitmodule path> is the path to the output .gitmodule file.
<.gitattributes path> is the path to the output .gitattribute file, which is optional.`,
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
	gitmodulesPath := ".gitmodules"
	gitattributesPath := ""
	if len(args) >= 1 {
		gitmodulesPath = args[0]
	}
	if len(args) == 2 {
		gitattributesPath = args[1]
	}

	if len(args) > 2 {
		return jirix.UsageErrorf("unexpected number of arguments")
	}

	localProjects, err := project.LocalProjects(jirix, project.FullScan)
	if err != nil {
		return err
	}
	return writeGitModules(jirix, localProjects, gitmodulesPath, gitattributesPath)
}

func writeGitModules(jirix *jiri.X, projects project.Projects, gitmodulesPath, gitattributesPath string) error {
	projEntries, treeRoot, err := project.GenerateSubmoduleTree(jirix, projects)
	if err != nil {
		return err
	}

	// Start creating .gitmodule and set up script.
	var gitmoduleBuf bytes.Buffer
	var commandBuf bytes.Buffer
	var gitattributeBuf bytes.Buffer
	commandBuf.WriteString("#!/bin/sh\n")

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
			if v.GitAttributes != "" {
				gitattributeBuf.WriteString(attributeDecl(v))
				gitattributeBuf.WriteString("\n")
			}
		}
	}

	for _, v := range projEntries {
		if reRootRepoName != "" && reRootRepoName == v.Path {
			return fmt.Errorf("path collision for root repo and project %+v", v)
		}
		if _, ok := treeRoot.Dropped[v.Key()]; ok {
			jirix.Logger.Debugf("dropped project %+v", v)
			continue
		}
		gitmoduleBuf.WriteString(moduleDecl(v))
		gitmoduleBuf.WriteString("\n")
		commandBuf.WriteString(commandDecl(v))
		commandBuf.WriteString("\n")
		if v.GitAttributes != "" {
			gitattributeBuf.WriteString(attributeDecl(v))
			gitattributeBuf.WriteString("\n")
		}
	}
	jirix.Logger.Debugf("generated gitmodule content \n%v\n", gitmoduleBuf.String())
	if err := ioutil.WriteFile(gitmodulesPath, gitmoduleBuf.Bytes(), 0644); err != nil {
		return err
	}

	if genGitModuleFlags.genScript != "" {
		jirix.Logger.Debugf("generated set up script for gitmodule content \n%v\n", commandBuf.String())
		if err := ioutil.WriteFile(genGitModuleFlags.genScript, commandBuf.Bytes(), 0755); err != nil {
			return err
		}
	}

	if gitattributesPath != "" {
		jirix.Logger.Debugf("generated gitattributes content \n%v\n", gitattributeBuf.String())
		if err := ioutil.WriteFile(gitattributesPath, gitattributeBuf.Bytes(), 0644); err != nil {
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
	tmpl := "[submodule \"%s\"]\n\tpath = %s\n\turl = %s"
	hashBytes := (sha256.Sum256([]byte(p.Key())))
	return fmt.Sprintf(tmpl, p.Name+"-"+hex.EncodeToString(hashBytes[:5]), p.Path, p.Remote)
}

func commandDecl(p project.Project) string {
	tmpl := "git update-index --add --cacheinfo 160000 %s \"%s\""
	return fmt.Sprintf(tmpl, p.Revision, p.Path)
}

func attributeDecl(p project.Project) string {
	tmpl := "%s %s"
	attrs := strings.ReplaceAll(p.GitAttributes, ",", " ")
	return fmt.Sprintf(tmpl, p.Path, strings.TrimSpace(attrs))
}
