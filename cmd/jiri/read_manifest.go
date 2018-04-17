// Copyright 2018 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
)

var readManifestFlags struct {
	ElementName string
	AttributeName string
)

func init() {
	cmdReadManifest.Flags.StringVar(&readManifestFlags.ElementName, 
		"element", "", "The name= of the <project> or <import>")
	cmdReadManifest.Flags.StringVar(&readManifestFlags.AttributeName, 
		"attribute", "", "The name of the element attribute")
}

var cmdReadManifest = &cmdline.Command{
	Runner: jiri.RunnerFunc(nil), // TODO
	Name:   "read-manifest",
	Short:  "Read <import> or <project> information from a manifest",
	Long: `
Command "read-manifest" reads information about a <project> or <import>
from a manifest file.

TODO
`,
	ArgsName: "<manifest>",
	ArgsLong: "<manifest> is the manifest file.",
}

func runReadManifest(jirix *jiri.X, args []string) error{
	if len(args) != 1 {
		return jirix.UsageErrorf("Wrong number of args")
	}
	manifestPath, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	if readManifestFlags.Element == "" {
		return errors.New("-element is required")
	}
	if readManifestFlags.Attribute == "" {
		return errors.New("-attribute is required")
	}
	
	return readManifest(jirix, manifestPath)
}

func readManifest(jirix *jiri.X, manifestPath string) error {
	manifest, err := project.ManifestFromFile(jirix, manifest)
	if err != nil {
		return err
	}
	content, err := ioutil.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	manifestContent := string(content)
	scm := gitutil.New(jirix, gitutil.RootDirOpt(filepath.Dir(manifestPath)))
	for _, p := range manifest.Projects {
		if p.Name == readManifestFlags.ElementName {
			
		}

		if p.Revision != "" {
			branch := "master"
			if p.RemoteBranch != "" {
				branch = p.RemoteBranch
			}
			out, err := scm.LsRemote(p.Remote, fmt.Sprintf("refs/heads/%s", branch))
			if err != nil {
				return err
			}
			latestRevision := strings.Fields(string(out))[0]
			manifestContent = strings.Replace(manifestContent, p.Revision, latestRevision, 1)
		}
	}
}

