// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/xml"
	"fmt"
	"os"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/project"
)

var (
	// Flags for configuring project attributes for remote imports.
	flagImportName, flagImportRemoteBranch, flagImportRoot string
	// Flags for controlling the behavior of the command.
	flagImportOverwrite bool
	flagImportOut       string
	flagImportDelete    bool
)

func init() {
	cmdImport.Flags.StringVar(&flagImportName, "name", "manifest", `The name of the remote manifest project.`)
	cmdImport.Flags.StringVar(&flagImportRemoteBranch, "remote-branch", "master", `The branch of the remote manifest project to track, without the leading "origin/".`)
	cmdImport.Flags.StringVar(&flagImportRoot, "root", "", `Root to store the manifest project locally.`)

	cmdImport.Flags.BoolVar(&flagImportOverwrite, "overwrite", false, `Write a new .jiri_manifest file with the given specification.  If it already exists, the existing content will be ignored and the file will be overwritten.`)
	cmdImport.Flags.StringVar(&flagImportOut, "out", "", `The output file.  Uses <root>/.jiri_manifest if unspecified.  Uses stdout if set to "-".`)
	cmdImport.Flags.BoolVar(&flagImportDelete, "delete", false, `Delete existing import. Import is matched using <manifest>, <remote> and name. <remote> is optional.`)
}

var cmdImport = &cmdline.Command{
	Runner: jiri.RunnerFunc(runImport),
	Name:   "import",
	Short:  "Adds imports to .jiri_manifest file",
	Long: `
Command "import" adds imports to the [root]/.jiri_manifest file, which specifies
manifest information for the jiri tool.  The file is created if it doesn't
already exist, otherwise additional imports are added to the existing file.

An <import> element is added to the manifest representing a remote manifest
import.  The manifest file path is relative to the root directory of the remote
import repository.

Example:
  $ jiri import myfile https://foo.com/bar.git

Run "jiri help manifest" for details on manifests.
`,
	ArgsName: "<manifest> <remote>",
	ArgsLong: `
<manifest> specifies the manifest file to use.

<remote> specifies the remote manifest repository.
`,
}

func isFile(file string) (bool, error) {
	fileInfo, err := os.Stat(file)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return !fileInfo.IsDir(), nil
}

func runImport(jirix *jiri.X, args []string) error {
	if flagImportDelete && len(args) != 1 && len(args) != 2 {
		return jirix.UsageErrorf("wrong number of arguments with delete flag")
	} else if !flagImportDelete && len(args) != 2 {
		return jirix.UsageErrorf("wrong number of arguments")
	}
	if flagImportDelete && flagImportOverwrite {
		return jirix.UsageErrorf("cannot use -delete and -overwrite together")
	}

	// Initialize manifest.
	var manifest *project.Manifest
	manifestExists, err := isFile(jirix.JiriManifestFile())
	if err != nil {
		return err
	}
	if !flagImportOverwrite && manifestExists {
		m, err := project.ManifestFromFile(jirix, jirix.JiriManifestFile())
		if err != nil {
			return err
		}
		manifest = m
	}
	if manifest == nil {
		manifest = &project.Manifest{}
	}

	if flagImportDelete {
		var tempImports []project.Import
		deletedImports := make(map[string]project.Import)
		for _, imp := range manifest.Imports {
			if imp.Manifest == args[0] && imp.Name == flagImportName {
				match := true
				if len(args) == 2 {
					match = false
					if imp.Remote == args[1] {
						match = true
					}
				}
				if match {
					deletedImports[imp.Name+"~"+imp.Manifest+"~"+imp.Remote] = imp
					continue
				}
			}
			tempImports = append(tempImports, imp)
		}
		if len(deletedImports) > 1 {
			return fmt.Errorf("More than 1 import meets your criteria. Please provide remote.")
		} else if len(deletedImports) == 1 {
			var data []byte
			for _, i := range deletedImports {
				data, err = xml.Marshal(i)
				if err != nil {
					return err
				}
				break
			}
			jirix.Logger.Infof("Deleted one import:\n%s", string(data))
		}
		manifest.Imports = tempImports
	} else {
		for _, imp := range manifest.Imports {
			if imp.Manifest == args[0] && imp.Remote == args[1] && imp.Name == flagImportName {
				//Already exists, skip
				jirix.Logger.Debugf("Skip import. Duplicate entry")
				return nil
			}
		}
		// There's not much error checking when writing the .jiri_manifest file;
		// errors will be reported when "jiri update" is run.
		manifest.Imports = append(manifest.Imports, project.Import{
			Manifest:     args[0],
			Name:         flagImportName,
			Remote:       args[1],
			RemoteBranch: flagImportRemoteBranch,
			Root:         flagImportRoot,
		})
	}

	// Write output to stdout or file.
	outFile := flagImportOut
	if outFile == "" {
		outFile = jirix.JiriManifestFile()
	}
	if outFile == "-" {
		bytes, err := manifest.ToBytes()
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(bytes)
		return err
	}
	return manifest.ToFile(jirix, outFile)
}
