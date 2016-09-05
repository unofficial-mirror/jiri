// Copyright 2016 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package version

import (
	"bytes"
	"fmt"
)

var (
	GitCommit string
	BuildTime string
)

func FormattedVersion() string {
	var versionString bytes.Buffer
	if GitCommit != "" {
		fmt.Fprintf(&versionString, "%s", GitCommit)
	}
	if BuildTime != "" {
		fmt.Fprintf(&versionString, " %s", BuildTime)
	}
	return versionString.String()
}
