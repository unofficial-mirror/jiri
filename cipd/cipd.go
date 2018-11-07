// Copyright 2018 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cipd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"time"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/osutil"
)

func getCipdPath() (string, error) {
	jiriPath, err := osutil.Executable()
	if err != nil {
		return "", err
	}
	// Assume cipd binary is located in the same directory of jiri
	jiriBinaryRoot := path.Dir(jiriPath)
	cipdBinary := path.Join(jiriBinaryRoot, "cipd")
	fileInfo, err := os.Stat(cipdBinary)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("cipd binary was not found at %q", cipdBinary)
		}
		return "", err
	}
	// Check if cipd binary has execution permission
	if fileInfo.Mode()&0111 == 0 {
		return "", fmt.Errorf("cipd binary at %q is not executable", cipdBinary)
	}
	return cipdBinary, nil

}

// Ensure runs cipd binary's ensure funcationality over file. Fetched packages will be
// saved to projectRoot directory. Parameter timeout is in minitues
func Ensure(jirix *jiri.X, file, projectRoot string, timeout uint) error {
	cipdBinary, err := getCipdPath()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Minute)
	defer cancel()
	args := []string{"ensure", "-ensure-file", file, "-root", projectRoot, "-log-level", "warning"}
	jirix.Logger.Debugf("Invoke cipd with %v", args)
	command := exec.CommandContext(ctx, cipdBinary, args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr

	return command.Run()
}
