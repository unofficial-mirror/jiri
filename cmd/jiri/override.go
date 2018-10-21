// Copyright 2018 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/project"
)

var (
	// Flags configuring project attributes for overrides.
	flagOverridePath       string
	flagOverrideRevision   string
	flagOverrideGerritHost string
	// Flags controlling the behavior of the command.
	flagOverrideDelete     bool
	flagOverrideList       bool
	flagOverrideJSONOutput string
)

func init() {
	cmdOverride.Flags.StringVar(&flagOverridePath, "path", "", `Path used to store the project locally.`)
	cmdOverride.Flags.StringVar(&flagOverrideRevision, "revision", "", `Revision to check out for the remote (defaults to HEAD).`)
	cmdOverride.Flags.StringVar(&flagOverrideGerritHost, "gerrithost", "", `The project Gerrit host.`)

	cmdOverride.Flags.BoolVar(&flagOverrideDelete, "delete", false, `Delete existing override. Override is matched using <name> and <remote>, <remote> is optional.`)
	cmdOverride.Flags.BoolVar(&flagOverrideList, "list", false, `List all the overrides from .jiri_manifest. This flag doesn't accept any arguments. -json-out flag can be used to specify json output file.`)
	cmdOverride.Flags.StringVar(&flagOverrideJSONOutput, "json-output", "", `JSON output file from -list flag.`)
}

var cmdOverride = &cmdline.Command{
	Runner: jiri.RunnerFunc(runOverride),
	Name:   "override",
	Short:  "Add overrides to .jiri_manifest file",
	Long: `Add overrides to the .jiri_manifest file. This allows overriding project
definitions, including from transitively imported manifests.

Example:
  $ jiri override project https://foo.com/bar.git

Run "jiri help manifest" for details on manifests.
`,
	ArgsName: "<name> <remote>",
	ArgsLong: `
<name> is the project name.

<remote> is the project remote.
`,
}

type projectInfo struct {
	Name       string `json:"name"`
	Path       string `json:"path,omitempty"`
	Remote     string `json:"remote"`
	Revision   string `json:"revision,omitempty"`
	GerritHost string `json:"gerrithost,omitempty"`
}

func runOverride(jirix *jiri.X, args []string) error {
	if flagOverrideDelete && flagOverrideList {
		return jirix.UsageErrorf("cannot use -delete and -list together")
	}

	if flagOverrideList && len(args) != 0 {
		return jirix.UsageErrorf("wrong number of arguments for the list flag")
	} else if flagOverrideDelete && len(args) != 1 && len(args) != 2 {
		return jirix.UsageErrorf("wrong number of arguments for the delete flag")
	} else if !flagOverrideDelete && !flagOverrideList && len(args) != 2 {
		return jirix.UsageErrorf("wrong number of arguments")
	}

	// Initialize manifest.
	manifestExists, err := isFile(jirix.JiriManifestFile())
	if err != nil {
		return err
	}
	if !manifestExists {
		return fmt.Errorf("'%s' does not exist", jirix.JiriManifestFile())
	}
	manifest, err := project.ManifestFromFile(jirix, jirix.JiriManifestFile())
	if err != nil {
		return err
	}

	if flagOverrideList {
		overrides := make([]projectInfo, len(manifest.Overrides))
		for i, p := range manifest.Overrides {
			overrides[i] = projectInfo{
				Name:       p.Name,
				Path:       p.Path,
				Remote:     p.Remote,
				Revision:   p.Revision,
				GerritHost: p.GerritHost,
			}
		}

		if flagOverrideJSONOutput == "" {
			for _, o := range overrides {
				fmt.Printf("* override %s\n", o.Name)
				fmt.Printf("  Name:        %s\n", o.Name)
				fmt.Printf("  Remote:      %s\n", o.Remote)
				if o.Path != "" {
					fmt.Printf("  Path:        %s\n", o.Path)
				}
				if o.Remote != "" {
					fmt.Printf("  Revision:    %s\n", o.Revision)
				}
				if o.GerritHost != "" {
					fmt.Printf("  Gerrit Host: %s\n", o.GerritHost)
				}
			}
		} else {
			file, err := os.Create(flagOverrideJSONOutput)
			if err != nil {
				return fmt.Errorf("failed to create output JSON file: %v\n", err)
			}
			defer file.Close()
			encoder := json.NewEncoder(file)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(overrides); err != nil {
				return fmt.Errorf("failed to serialize JSON output: %v\n", err)
			}
		}
		return nil
	}

	name := args[0]
	if flagOverrideDelete {
		var overrides []project.Project
		var deleted []project.Project
		for _, p := range manifest.Overrides {
			if len(args) == 2 && p.Remote != args[1] {
				overrides = append(overrides, p)
				continue
			}
			if p.Name != name {
				overrides = append(overrides, p)
				continue
			}
			deleted = append(deleted, p)
		}
		if len(deleted) > 1 {
			return fmt.Errorf("more than one override matches")
		} else if len(deleted) == 1 {
			var names []string
			for _, p := range deleted {
				names = append(names, p.Name)
			}
			jirix.Logger.Infof("Deleted overrides: %s\n", strings.Join(names, " "))
		}
		manifest.Overrides = overrides
	} else {
		remote := args[1]
		project := project.Project{
			Name:         name,
			Remote:       remote,
			Path:         flagOverridePath,
			Revision:     flagOverrideRevision,
			GerritHost:   flagOverrideGerritHost,
			// We deliberately omit RemoteBranch, HistoryDepth and
			// GitHooks. Those fields are effectively deprecated and
			// will likely be removed in the future.
		}
		match := false
		for i, p := range manifest.Overrides {
			if p.Name == name && p.Remote == remote {
				manifest.Overrides[i] = project
				match = true
				break
			}
		}
		if !match {
			manifest.Overrides = append(manifest.Overrides, project)
		}
	}

	// There's no error checking when writing the .jiri_manifest file;
	// errors will be reported when "jiri update" is run.
	return manifest.ToFile(jirix, jirix.JiriManifestFile())
}
