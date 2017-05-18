// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"strconv"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/project"
)

var cmdProjectConfig = &cmdline.Command{
	Runner: jiri.RunnerFunc(runProjectConfig),
	Name:   "project-config",
	Short:  "Prints/sets project's local config",
	Long: `
Prints/Manages local project config. This command should be run from inside a
project. It will print config if no flags are provided otherwise set it.`,
}

var (
	configIgnoreFlag   string
	configNoUpdateFlag string
	configNoRebaseFlag string
)

func init() {
	cmdProjectConfig.Flags.StringVar(&configIgnoreFlag, "ignore", "", `This can be true or false. If set to true project would be completely ignored while updating`)
	cmdProjectConfig.Flags.StringVar(&configNoUpdateFlag, "no-update", "", `This can be true or false. If set to true project won't be updated`)
	cmdProjectConfig.Flags.StringVar(&configNoRebaseFlag, "no-rebase", "", `This can be true or false. If set to true local branch won't be rebased or merged.`)
}

func runProjectConfig(jirix *jiri.X, args []string) error {
	p, err := currentProject(jirix)
	if err != nil {
		return err
	}
	if configIgnoreFlag == "" && configNoUpdateFlag == "" && configNoRebaseFlag == "" {
		displayConfig(p.LocalConfig)
		return nil
	}
	lc := p.LocalConfig
	if err := setBoolVar(configIgnoreFlag, &lc.Ignore, "ignore"); err != nil {
		return err
	}
	if err := setBoolVar(configNoUpdateFlag, &lc.NoUpdate, "no-update"); err != nil {
		return err
	}
	if err := setBoolVar(configNoRebaseFlag, &lc.NoRebase, "no-rebase"); err != nil {
		return err
	}
	return project.WriteLocalConfig(jirix, p, lc)
}

func setBoolVar(value string, b *bool, flagName string) error {
	if value == "" {
		return nil
	}
	if val, err := strconv.ParseBool(value); err != nil {
		return fmt.Errorf("%s flag should be true or false", flagName)
	} else {
		*b = val
	}
	return nil
}

func displayConfig(lc project.LocalConfig) {
	fmt.Printf("Config:\n")
	fmt.Printf("ignore: %t\n", lc.Ignore)
	fmt.Printf("no-update: %t\n", lc.NoUpdate)
	fmt.Printf("no-rebase: %t\n", lc.NoRebase)
}
