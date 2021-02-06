// Copyright 2018 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"go.fuchsia.dev/jiri"
	"go.fuchsia.dev/jiri/cmdline"
	"go.fuchsia.dev/jiri/project"
)

var overrideFlags struct {
	// Flags configuring project attributes for overrides.
	importManifest string
	gerritHost     string
	path           string
	revision       string
	// Flags controlling the behavior of the command.
	delete     bool
	list       bool
	JSONOutput string
}

func init() {
	cmdOverride.Flags.StringVar(&overrideFlags.importManifest, "import-manifest", "", "The manifest of the import override.")

	cmdOverride.Flags.StringVar(&overrideFlags.path, "path", "", `Path used to store the project locally.`)
	cmdOverride.Flags.StringVar(&overrideFlags.revision, "revision", "", `Revision to check out for the remote (defaults to HEAD).`)
	cmdOverride.Flags.StringVar(&overrideFlags.gerritHost, "gerrithost", "", `The project Gerrit host.`)

	cmdOverride.Flags.BoolVar(&overrideFlags.delete, "delete", false, `Delete existing override. Override is matched using <name> and <remote>, <remote> is optional.`)
	cmdOverride.Flags.BoolVar(&overrideFlags.list, "list", false, `List all the overrides from .jiri_manifest. This flag doesn't accept any arguments. -json-out flag can be used to specify json output file.`)
	cmdOverride.Flags.StringVar(&overrideFlags.JSONOutput, "json-output", "", `JSON output file from -list flag.`)
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

type overrideInfo struct {
	Import         bool   `json:"import,omitempty"`
	ImportManifest string `json:"import-manifest,omitempty"`
	Name           string `json:"name"`
	Path           string `json:"path,omitempty"`
	Remote         string `json:"remote"`
	Revision       string `json:"revision,omitempty"`
	GerritHost     string `json:"gerrithost,omitempty"`
}

func runOverride(jirix *jiri.X, args []string) error {
	if overrideFlags.delete && overrideFlags.list {
		return jirix.UsageErrorf("cannot use -delete and -list together")
	}

	if overrideFlags.list && len(args) != 0 {
		return jirix.UsageErrorf("wrong number of arguments for the list flag")
	} else if overrideFlags.delete && len(args) != 1 && len(args) != 2 {
		return jirix.UsageErrorf("wrong number of arguments for the delete flag")
	} else if !overrideFlags.delete && !overrideFlags.list && len(args) != 2 {
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

	if overrideFlags.list {
		overrides := make([]overrideInfo, 0)
		for _, p := range manifest.ProjectOverrides {
			overrides = append(overrides, overrideInfo{
				Name:       p.Name,
				Path:       p.Path,
				Remote:     p.Remote,
				Revision:   p.Revision,
				GerritHost: p.GerritHost,
			})
		}

		for _, p := range manifest.ImportOverrides {
			overrides = append(overrides, overrideInfo{
				Import:         true,
				ImportManifest: p.Manifest,
				Name:           p.Name,
				Remote:         p.Remote,
				Revision:       p.Revision,
			})
		}

		if overrideFlags.JSONOutput == "" {
			for _, o := range overrides {
				fmt.Printf("* override %s\n", o.Name)
				if o.Import {
					fmt.Printf("  IsImport: %v\n", o.Import)
					fmt.Printf("  ImportManifest: %s\n", o.ImportManifest)
				}
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
			file, err := os.Create(overrideFlags.JSONOutput)
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
	if overrideFlags.delete {
		var projectOverrides []project.Project
		var importOverrides []project.Import
		var deletedProjectOverrides []project.Project
		var deletedImportOverrides []project.Import
		for _, p := range manifest.ImportOverrides {
			if overrideFlags.importManifest == "" || (len(args) == 2 && p.Remote != args[1]) || p.Name != name {
				importOverrides = append(importOverrides, p)
				continue
			}
			deletedImportOverrides = append(deletedImportOverrides, p)
		}

		for _, p := range manifest.ProjectOverrides {
			if overrideFlags.importManifest != "" || (len(args) == 2 && p.Remote != args[1]) || p.Name != name {
				projectOverrides = append(projectOverrides, p)
				continue
			}
			deletedProjectOverrides = append(deletedProjectOverrides, p)
		}

		if len(deletedProjectOverrides)+len(deletedImportOverrides) > 1 {
			return fmt.Errorf("more than one override matches")
		}
		var names []string
		for _, p := range deletedProjectOverrides {
			names = append(names, p.Name)
		}
		for _, p := range deletedImportOverrides {
			names = append(names, p.Name)
		}
		jirix.Logger.Infof("Deleted overrides: %s\n", strings.Join(names, " "))

		manifest.ProjectOverrides = projectOverrides
		manifest.ImportOverrides = importOverrides
	} else {
		remote := args[1]
		overrideKeys := make(map[string]bool)
		for _, p := range manifest.ProjectOverrides {
			overrideKeys[string(p.Key())] = true
		}
		for _, p := range manifest.ImportOverrides {
			overrideKeys[string(p.ProjectKey())] = true
		}
		if _, ok := overrideKeys[string(project.MakeProjectKey(name, remote))]; !ok {
			if overrideFlags.importManifest != "" {
				importOverride := project.Import{
					Name:     name,
					Remote:   remote,
					Manifest: overrideFlags.importManifest,
					Revision: overrideFlags.revision,
				}
				manifest.ImportOverrides = append(manifest.ImportOverrides, importOverride)
			} else {
				projectOverride := project.Project{
					Name:       name,
					Remote:     remote,
					Path:       overrideFlags.path,
					Revision:   overrideFlags.revision,
					GerritHost: overrideFlags.gerritHost,
					// We deliberately omit RemoteBranch, HistoryDepth and
					// GitHooks. Those fields are effectively deprecated and
					// will likely be removed in the future.
				}
				manifest.ProjectOverrides = append(manifest.ProjectOverrides, projectOverride)
			}
		} else {
			jirix.Logger.Infof("Override \"%s:%s\" is already exist, no modification will be made.", name, remote)
		}
	}

	// There's no error checking when writing the .jiri_manifest file;
	// errors will be reported when "jiri update" is run.
	return manifest.ToFile(jirix, jirix.JiriManifestFile())
}
