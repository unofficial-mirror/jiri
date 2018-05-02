// Copyright 2018 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/template"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/project"
)

// Flags for cmdManifest.
var manifestFlags struct {
	// ElementName is a flag specifying the name= of the <import> or <project>
	// to search for in the manifest file.
	ElementName string

	// Template is a string template from pkg/text/template specifying which
	// fields to display.  The invoker of Jiri is expected to form this template
	// themselves.
	Template string
}

var cmdManifest = &cmdline.Command{
	Runner: jiri.RunnerFunc(runManifest),
	Name:   "manifest",
	Short:  "Reads <import> or <project> information from a manifest file",
	Long: `Reads <import> or <project> information from a manifest file.
	A template matching the schema defined in pkg/text/template is used to fill
	in the requested information.  Some examples:

	    Read project's 'remote' attribute:
	        manifest -element=$PROJECT_NAME -template="{{.Remote}}"

	    Read import's 'path' attribute:
	        manifest -element=$IMPORT_NAME -template="{{.Path}}"
	`,
	ArgsName: "<manifest>",
	ArgsLong: "<manifest> is the manifest file.",
}

func init() {
	setManifestFlags(&cmdManifest.Flags)
}

// setManifestFlags sets command-line flags for the manifest command.
func setManifestFlags(f *flag.FlagSet) {
	f.StringVar(&manifestFlags.ElementName, "element", "", "Name of the <project> or <import>.")
	f.StringVar(&manifestFlags.Template, "template", "", "The template for the fields to display.")
}

// Run executes the ManifestCommand.
func runManifest(jirix *jiri.X, args []string) error {
	if len(args) != 1 {
		return jirix.UsageErrorf("Wrong number of args")
	}
	manifestPath := args[0]

	if manifestFlags.ElementName == "" {
		return errors.New("-element is required")
	}
	if manifestFlags.Template == "" {
		return errors.New("-template is required")
	}

	// Create the template to fill in.
	tmpl, err := template.New("").Parse(manifestFlags.Template)
	if err != nil {
		return fmt.Errorf("failed to parse -template: %s", err)
	}

	return readManifest(jirix, manifestPath, tmpl)
}

func readManifest(jirix *jiri.X, manifestPath string, tmpl *template.Template) error {
	manifest, err := project.ManifestFromFile(jirix, manifestPath)
	if err != nil {
		return err
	}

	elementName := strings.ToLower(manifestFlags.ElementName)

	// Check if any <project> elements match the given element name.
	for _, project := range manifest.Projects {
		if strings.ToLower(project.Name) == elementName {
			return tmpl.Execute(os.Stdout, &project)
		}
	}

	// Check if any <import> elements match the given element name.
	for _, imprt := range manifest.Imports {
		if strings.ToLower(imprt.Name) == elementName {
			return tmpl.Execute(os.Stdout, &imprt)
		}
	}

	// Found nothing.
	return fmt.Errorf("found no project/import named %s", manifestFlags.ElementName)
}
