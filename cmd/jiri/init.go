// Copyright 2016 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/analytics_util"
	"fuchsia.googlesource.com/jiri/cmdline"
)

var cmdInit = &cmdline.Command{
	Runner: cmdline.RunnerFunc(runInit),
	Name:   "init",
	Short:  "Create a new jiri root",
	Long: `
The "init" command creates new jiri "root" - basically a [root]/.jiri_root
directory and template files.

Running "init" in existing jiri [root] is safe. It will search all the parents
and update root config file if it already exists.
`,
	ArgsName: "[directory]",
	ArgsLong: `
If you provide a directory, the command is run inside it. If this directory
does not exists, it will be created. Jiri will not create a root if parent of
passed directory is already a jiri root.
`,
}

var (
	cacheFlag             string
	sharedFlag            bool
	showAnalyticsDataFlag bool
	analyticsOptFlag      string
)

func init() {
	cmdInit.Flags.StringVar(&cacheFlag, "cache", "", "Jiri cache directory.")
	cmdInit.Flags.BoolVar(&sharedFlag, "shared", false, "Use shared cache, which doesn't commit or push.")
	cmdInit.Flags.BoolVar(&showAnalyticsDataFlag, "show-analytics-data", false, "Show analytics data that jiri collect when you opt-in and exits.")
	cmdInit.Flags.StringVar(&analyticsOptFlag, "analytics-opt", "", "Opt in/out of analytics collection. Takes true/false")
}

// Searches for jiri root starting in parent directories
// path is absolute cleaned path
func findJiriRoot(path string) (string, error) {
	paths := []string{path}
	for path != "/" {
		path = filepath.Dir(path)
		paths = append(paths, path)
	}

	for i := len(paths) - 1; i >= 0; i-- {
		fi, err := os.Stat(filepath.Join(paths[i], jiri.RootMetaDir))
		if err == nil && fi.IsDir() {
			return paths[i], nil
		}
	}

	return "", nil
}

func runInit(env *cmdline.Env, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("wrong number of arguments")
	}

	if showAnalyticsDataFlag {
		fmt.Printf("%s\n", analytics_util.CollectedData)
		return nil
	}

	var dir string
	var err error
	passedDir := ""
	if len(args) == 1 {
		if dir, err = filepath.Abs(args[0]); err != nil {
			return err
		}
		dir = filepath.Clean(dir)
		passedDir = dir

		if _, err := os.Stat(dir); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			if err := os.Mkdir(dir, 0755); err != nil {
				return err
			}
		}
	} else {
		dir, err = os.Getwd()
		if err != nil {
			return err
		}
	}

	root, err := findJiriRoot(dir)
	if err != nil {
		return err
	}
	if root != "" && passedDir != "" && passedDir != root {
		return fmt.Errorf("Cannot create root at %q as it's parent %q is already a jiri root", args[0], root)
	}

	d := filepath.Join(dir, jiri.RootMetaDir)
	if root != "" {
		d = filepath.Join(root, jiri.RootMetaDir)
	} else if err := os.Mkdir(d, 0755); err != nil {
		return err
	}

	if cacheFlag != "" {
		cache, err := filepath.Abs(cacheFlag)
		if err != nil {
			return err
		}
		if _, err := os.Stat(cache); os.IsNotExist(err) {
			if err := os.MkdirAll(cache, 0755); err != nil {
				return err
			}
		}
	}

	config := &jiri.Config{}
	configPath := filepath.Join(d, jiri.ConfigFile)
	if _, err := os.Stat(configPath); err == nil {
		config, err = jiri.ConfigFromFile(configPath)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	if cacheFlag != "" {
		config.CachePath = cacheFlag
		config.Shared = sharedFlag
	}

	if analyticsOptFlag != "" {
		if val, err := strconv.ParseBool(analyticsOptFlag); err != nil {
			return fmt.Errorf("'analytics-opt' flag should be true or false")
		} else {
			if val {
				config.AnalyticsOptIn = "yes"
				config.AnalyticsVersion = analytics_util.Version

				bytes := make([]byte, 16)
				io.ReadFull(rand.Reader, bytes)
				if err != nil {
					return err
				}
				bytes[6] = (bytes[6] & 0x0f) | 0x40
				bytes[8] = (bytes[8] & 0x3f) | 0x80

				config.AnalyticsUserId = fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:])
			} else {
				config.AnalyticsOptIn = "no"
				config.AnalyticsVersion = ""
				config.AnalyticsUserId = ""
			}
		}
	}

	if err := config.Write(configPath); err != nil {
		return err
	}

	// TODO(phosek): also create an empty manifest

	return nil
}
