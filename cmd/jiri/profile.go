// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/profiles/profilescmdline"
)

var cmdProfile = &cmdline.Command{
	Name:  "profile",
	Short: "Display information about installed profiles",
	Long:  "Display information about installed profiles and their configuration.",
}

func init() {
	profilescmdline.RegisterReaderCommands(cmdProfile, "", jiri.ProfilesDBDir)
	profilescmdline.RegisterManagementCommands(cmdProfile, true, "", jiri.ProfilesDBDir, jiri.ProfilesRootDir)
}
