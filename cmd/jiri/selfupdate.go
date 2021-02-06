// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"

	"go.fuchsia.dev/jiri"
	"go.fuchsia.dev/jiri/cmdline"
)

// cmdSelfUpdate represents the "jiri update" command.
var cmdSelfUpdate = &cmdline.Command{
	Runner: cmdline.RunnerFunc(runSelfUpdate),
	Name:   "selfupdate",
	Short:  "Update jiri tool",
	Long: `
Updates jiri tool and replaces current one with the latest`,
}

func runSelfUpdate(env *cmdline.Env, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unexpected number of arguments")
	}

	if err := jiri.Update(true); err != nil {
		return fmt.Errorf("Update failed: %s", err)
	}
	fmt.Println("Tool updated.")
	return nil
}
