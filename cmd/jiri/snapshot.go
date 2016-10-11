// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/project"
	"fuchsia.googlesource.com/jiri/runutil"
)

const (
	defaultSnapshotDir = ".snapshot"
)

var (
	pushRemoteFlag  bool
	snapshotDirFlag string
	snapshotGcFlag  bool
	timeFormatFlag  string
)

func init() {
	cmdSnapshot.Flags.StringVar(&snapshotDirFlag, "dir", "", "Directory where snapshot are stored.  Defaults to $JIRI_ROOT/.snapshot.")
	cmdSnapshotCheckout.Flags.BoolVar(&snapshotGcFlag, "gc", false, "Garbage collect obsolete repositories.")
	cmdSnapshotCreate.Flags.StringVar(&timeFormatFlag, "time-format", time.RFC3339, "Time format for snapshot file name.")
}

var cmdSnapshot = &cmdline.Command{
	Name:  "snapshot",
	Short: "Manage project snapshots",
	Long: `
The "jiri snapshot" command can be used to manage project snapshots.
In particular, it can be used to create new snapshots and to list
existing snapshots.
`,
	Children: []*cmdline.Command{cmdSnapshotCheckout, cmdSnapshotCreate, cmdSnapshotList},
}

// cmdSnapshotCreate represents the "jiri snapshot create" command.
var cmdSnapshotCreate = &cmdline.Command{
	Runner: jiri.RunnerFunc(runSnapshotCreate),
	Name:   "create",
	Short:  "Create a new project snapshot",
	Long: `
The "jiri snapshot create <label>" command captures the current project state
in a manifest.  If the -push-remote flag is provided, the snapshot is committed
and pushed upstream.

Internally, snapshots are organized as follows:

 <snapshot-dir>/
   labels/
     <label1>/
       <label1-snapshot1>
       <label1-snapshot2>
       ...
     <label2>/
       <label2-snapshot1>
       <label2-snapshot2>
       ...
     <label3>/
     ...
   <label1> # a symlink to the latest <label1-snapshot*>
   <label2> # a symlink to the latest <label2-snapshot*>
   ...

NOTE: Unlike the jiri tool commands, the above internal organization
is not an API. It is an implementation and can change without notice.
`,
	ArgsName: "<label>",
	ArgsLong: "<label> is the snapshot label.",
}

func runSnapshotCreate(jirix *jiri.X, args []string) error {
	if len(args) != 1 {
		return jirix.UsageErrorf("unexpected number of arguments")
	}
	label := args[0]
	snapshotDir, err := getSnapshotDir(jirix)
	if err != nil {
		return err
	}
	snapshotFile := filepath.Join(snapshotDir, "labels", label, time.Now().Format(timeFormatFlag))

	return createSnapshot(jirix, snapshotDir, snapshotFile, label)
}

// getSnapshotDir returns the path to the snapshot directory, creating it if
// necessary.
func getSnapshotDir(jirix *jiri.X) (string, error) {
	dir := snapshotDirFlag
	if dir == "" {
		dir = filepath.Join(jirix.Root, defaultSnapshotDir)
	}

	if !filepath.IsAbs(dir) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(cwd, dir)
	}

	// Make sure directory exists.
	if err := jirix.NewSeq().MkdirAll(dir, 0755).Done(); err != nil {
		return "", err
	}
	return dir, nil
}

func createSnapshot(jirix *jiri.X, snapshotDir, snapshotFile, label string) error {
	// Create a snapshot that encodes the current state of master
	// branches for all local projects.
	if err := project.CreateSnapshot(jirix, snapshotFile, ""); err != nil {
		return err
	}

	s := jirix.NewSeq()
	// Update the symlink for this snapshot label to point to the
	// latest snapshot.
	symlink := filepath.Join(snapshotDir, label)
	newSymlink := symlink + ".new"
	relativeSnapshotPath := strings.TrimPrefix(snapshotFile, snapshotDir+string(os.PathSeparator))
	return s.RemoveAll(newSymlink).
		Symlink(relativeSnapshotPath, newSymlink).
		Rename(newSymlink, symlink).Done()
}

// cmdSnapshotCheckout represents the "jiri snapshot checkout" command.
var cmdSnapshotCheckout = &cmdline.Command{
	Runner: jiri.RunnerFunc(runSnapshotCheckout),
	Name:   "checkout",
	Short:  "Checkout a project snapshot",
	Long: `
The "jiri snapshot checkout <snapshot>" command restores local project state to
the state in the given snapshot manifest.
`,
	ArgsName: "<snapshot>",
	ArgsLong: "<snapshot> is the snapshot manifest file.",
}

func runSnapshotCheckout(jirix *jiri.X, args []string) error {
	if len(args) != 1 {
		return jirix.UsageErrorf("unexpected number of arguments")
	}
	return project.CheckoutSnapshot(jirix, args[0], snapshotGcFlag)
}

// cmdSnapshotList represents the "jiri snapshot list" command.
var cmdSnapshotList = &cmdline.Command{
	Runner: jiri.RunnerFunc(runSnapshotList),
	Name:   "list",
	Short:  "List existing project snapshots",
	Long: `
The "snapshot list" command lists existing snapshots of the labels
specified as command-line arguments. If no arguments are provided, the
command lists snapshots for all known labels.
`,
	ArgsName: "<label ...>",
	ArgsLong: "<label ...> is a list of snapshot labels.",
}

func runSnapshotList(jirix *jiri.X, args []string) error {
	snapshotDir, err := getSnapshotDir(jirix)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		// Identify all known snapshot labels, using a
		// heuristic that looks for all symbolic links <foo>
		// in the snapshot directory that point to a file in
		// the "labels/<foo>" subdirectory of the snapshot
		// directory.
		fileInfoList, err := ioutil.ReadDir(snapshotDir)
		if err != nil {
			return fmt.Errorf("ReadDir(%v) failed: %v", snapshotDir, err)
		}
		for _, fileInfo := range fileInfoList {
			if fileInfo.Mode()&os.ModeSymlink != 0 {
				path := filepath.Join(snapshotDir, fileInfo.Name())
				dst, err := filepath.EvalSymlinks(path)
				if err != nil {
					return fmt.Errorf("EvalSymlinks(%v) failed: %v", path, err)
				}
				if strings.HasSuffix(filepath.Dir(dst), filepath.Join("labels", fileInfo.Name())) {
					args = append(args, fileInfo.Name())
				}
			}
		}
	}

	// Check that all labels exist.
	var notexist []string
	for _, label := range args {
		labelDir := filepath.Join(snapshotDir, "labels", label)
		switch _, err := jirix.NewSeq().Stat(labelDir); {
		case runutil.IsNotExist(err):
			notexist = append(notexist, label)
		case err != nil:
			return err
		}
	}
	if len(notexist) > 0 {
		return fmt.Errorf("snapshot labels %v not found", notexist)
	}

	// Print snapshots for all labels.
	sort.Strings(args)
	for _, label := range args {
		// Scan the snapshot directory "labels/<label>" printing
		// all snapshots.
		labelDir := filepath.Join(snapshotDir, "labels", label)
		fileInfoList, err := ioutil.ReadDir(labelDir)
		if err != nil {
			return fmt.Errorf("ReadDir(%v) failed: %v", labelDir, err)
		}
		fmt.Fprintf(jirix.Stdout(), "snapshots of label %q:\n", label)
		for _, fileInfo := range fileInfoList {
			fmt.Fprintf(jirix.Stdout(), "  %v\n", fileInfo.Name())
		}
	}
	return nil
}
