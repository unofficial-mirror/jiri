// Copyright 2016 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/project"
)

var cmdGrep = &cmdline.Command{
	Runner: jiri.RunnerFunc(runGrep),
	Name:   "grep",
	Short:  "Search across projects.",
	Long: `
Run git grep across all projects.
`,
	ArgsName: "<query> [--] [<pathspec>...]",
}

var grepFlags struct {
	n bool
	h bool
	i bool
	e string
	l bool
	L bool
	w bool
}

func init() {
	flags := &cmdGrep.Flags
	flags.BoolVar(&grepFlags.n, "n", false, "Prefix the line number to matching lines")
	flags.StringVar(&grepFlags.e, "e", "", "The next parameter is the pattern. This option has to be used for patterns starting with -")
	flags.BoolVar(&grepFlags.h, "H", true, "Does nothing. Just makes this git grep compatible")
	flags.BoolVar(&grepFlags.i, "i", false, "Ignore case differences between the patterns and the files")
	flags.BoolVar(&grepFlags.l, "l", false, "Instead of showing every matched line, show only the names of files that contain matches")
	flags.BoolVar(&grepFlags.w, "w", false, "Match the pattern only at word boundary")
	flags.BoolVar(&grepFlags.l, "name-only", false, "same as -l")
	flags.BoolVar(&grepFlags.l, "files-with-matches", false, "same as -l")
	flags.BoolVar(&grepFlags.L, "L", false, "Instead of showing every matched line, show only the names of files that do not contain matches")
	flags.BoolVar(&grepFlags.L, "files-without-match", false, "same as -L")
}

func buildFlags() []string {
	var args []string
	if grepFlags.n {
		args = append(args, "-n")
	}
	if grepFlags.e != "" {
		args = append(args, "-e", grepFlags.e)
	}
	if grepFlags.i {
		args = append(args, "-i")
	}
	if grepFlags.l {
		args = append(args, "-l")
	}
	if grepFlags.L {
		args = append(args, "-L")
	}
	if grepFlags.w {
		args = append(args, "-w")
	}
	return args
}

func doGrep(jirix *jiri.X, args []string) ([]string, error) {
	var pathSpecs []string
	lenArgs := len(args)
	if lenArgs > 0 {
		for i, a := range os.Args {
			if a == "--" {
				pathSpecs = os.Args[i+1:]
				break
			}
		}
		// we will not find -- if user uses something like jiri grep -- a b,
		// as flag.Parse() removes '--' in that case, so set args length
		lenArgs = len(args) - len(pathSpecs)
		for i, a := range args {

			if a == "--" {
				args = args[0:i]
				// reset length
				lenArgs = len(args)
				break
			}
		}
	}

	if grepFlags.e != "" && lenArgs > 0 {
		return nil, jirix.UsageErrorf("No additional argument allowed with flag -e")
	} else if grepFlags.e == "" && lenArgs != 1 {
		return nil, jirix.UsageErrorf("grep requires one argument")
	}

	projects, err := project.LocalProjects(jirix, project.FastScan)
	if err != nil {
		return nil, err
	}

	// TODO(ianloic): run in parallel rather than serially.
	// TODO(ianloic): only run grep on projects under the cwd.
	var results []string
	flags := buildFlags()
	if jirix.Color.Enabled() {
		flags = append(flags, "--color=always")
	}
	query := ""
	if lenArgs == 1 {
		query = args[0]
	}
	for _, project := range projects {
		relpath, err := filepath.Rel(jirix.Root, project.Path)
		if err != nil {
			return nil, err
		}
		git := gitutil.New(jirix, gitutil.RootDirOpt(project.Path))
		lines, err := git.Grep(query, pathSpecs, flags...)
		if err != nil {
			continue
		}
		for _, line := range lines {
			// TODO(ianloic): higlight the project path part like `repo grep`.
			results = append(results, relpath+"/"+line)
		}
	}

	// TODO(ianloic): fail if all of the sub-greps fail
	return results, nil
}

func runGrep(jirix *jiri.X, args []string) error {
	lines, err := doGrep(jirix, args)
	if err != nil {
		return err
	}

	for _, line := range lines {
		fmt.Println(line)
	}
	return nil
}
