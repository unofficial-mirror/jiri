// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package jiri provides utilities used by the jiri tool and related tools.
package jiri

// TODO(toddw): Rename this package to fuchsia.googlesource.com/jiri, and rename the tool itself to
// fuchsia.googlesource.com/jiri/cmd/jiri

import (
	"encoding/xml"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync/atomic"

	"fuchsia.googlesource.com/jiri/analytics_util"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/color"
	"fuchsia.googlesource.com/jiri/envvar"
	"fuchsia.googlesource.com/jiri/log"
	"fuchsia.googlesource.com/jiri/timing"
	"fuchsia.googlesource.com/jiri/tool"
)

const (
	RootMetaDir        = ".jiri_root"
	ProjectMetaDir     = ".git/jiri"
	OldProjectMetaDir  = ".jiri"
	ConfigFile         = "config"
	DefaultCacheSubdir = "cache"
	ProjectMetaFile    = "metadata.v2"
	ProjectConfigFile  = "config"
	JiriManifestFile   = ".jiri_manifest"

	// PreservePathEnv is the name of the environment variable that, when set to a
	// non-empty value, causes jiri tools to use the existing PATH variable,
	// rather than mutating it.
	PreservePathEnv = "JIRI_PRESERVE_PATH"
)

// Config represents jiri global config
type Config struct {
	CachePath       string `xml:"cache>path,omitempty"`
	Shared          bool   `xml:"cache>shared,omitempty"`
	AnalyticsOptIn  string `xml:"analytics>optin,omitempty"`
	AnalyticsUserId string `xml:"analytics>userId,omitempty"`
	// version user has opted-in to
	AnalyticsVersion string `xml:"analytics>version,omitempty"`

	XMLName struct{} `xml:"config"`
}

func (c *Config) Write(filename string) error {
	if c.CachePath != "" {
		var err error
		c.CachePath, err = cleanPath(c.CachePath)
		if err != nil {
			return err
		}
	}
	data, err := xml.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filename, data, 0644)
}

func ConfigFromFile(filename string) (*Config, error) {
	bytes, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	c := new(Config)
	if err := xml.Unmarshal(bytes, c); err != nil {
		return nil, err
	}
	return c, nil
}

// X holds the execution environment for the jiri tool and related tools.  This
// includes the jiri filesystem root directory.
//
// TODO(toddw): Other jiri state should be transitioned to this struct,
// including the manifest and related operations.
type X struct {
	*tool.Context
	Root     string
	Usage    func(format string, args ...interface{}) error
	config   *Config
	Cache    string
	Shared   bool
	Jobs     uint
	Color    color.Color
	Logger   *log.Logger
	failures uint32
}

func (jirix *X) IncrementFailures() {
	atomic.AddUint32(&jirix.failures, 1)
}

func (jirix *X) Failures() uint32 {
	return atomic.LoadUint32(&jirix.failures)
}

var (
	rootFlag              string
	jobsFlag              uint
	colorFlag             bool
	quietVerboseFlag      bool
	debugVerboseFlag      bool
	traceVerboseFlag      bool
	showProgressFlag      bool
	progessWindowSizeFlag uint
)

// showRootFlag implements a flag that dumps the root dir and exits the
// program when it is set.
type showRootFlag struct{}

func (showRootFlag) IsBoolFlag() bool { return true }
func (showRootFlag) String() string   { return "<just specify -show-root to activate>" }
func (showRootFlag) Set(string) error {
	if root, err := findJiriRoot(nil); err != nil {
		fmt.Printf("Error: %s\n", err)
		os.Exit(1)
	} else {
		fmt.Println(root)
		os.Exit(0)
	}
	return nil
}

func init() {
	flag.StringVar(&rootFlag, "root", "", "Jiri root directory")
	flag.UintVar(&jobsFlag, "j", DefaultJobs, "Number of jobs (commands) to run simultaneously")
	flag.BoolVar(&colorFlag, "color", true, "Use color to format output.")
	flag.BoolVar(&showProgressFlag, "show-progress", true, "Show progress.")
	flag.Var(showRootFlag{}, "show-root", "Displays jiri root and exits.")
	flag.UintVar(&progessWindowSizeFlag, "progress-window", 5, "Number of progress messages to show simultaneously. Should be between 1 and 10")
	flag.BoolVar(&quietVerboseFlag, "quiet", false, "Only print user actionable messages.")
	flag.BoolVar(&quietVerboseFlag, "q", false, "Same as -quiet")
	flag.BoolVar(&debugVerboseFlag, "v", false, "Print debug level output.")
	flag.BoolVar(&traceVerboseFlag, "vv", false, "Print trace level output.")
}

// NewX returns a new execution environment, given a cmdline env.
// It also prepends .jiri_root/bin to the PATH.
func NewX(env *cmdline.Env) (*X, error) {
	color := color.NewColor(colorFlag)

	loggerLevel := log.InfoLevel
	if quietVerboseFlag {
		loggerLevel = log.WarningLevel
	} else if traceVerboseFlag {
		loggerLevel = log.TraceLevel
	} else if debugVerboseFlag {
		loggerLevel = log.DebugLevel
	}
	if progessWindowSizeFlag < 1 {
		progessWindowSizeFlag = 1
	} else if progessWindowSizeFlag > 10 {
		progessWindowSizeFlag = 10
	}
	logger := log.NewLogger(loggerLevel, color, showProgressFlag, progessWindowSizeFlag, nil, nil)

	ctx := tool.NewContextFromEnv(env)
	root, err := findJiriRoot(ctx.Timer())
	if err != nil {
		return nil, err
	}

	if jobsFlag == 0 {
		return nil, fmt.Errorf("No of concurrent jobs should be more than zero")
	}

	x := &X{
		Context: ctx,
		Root:    root,
		Usage:   env.UsageErrorf,
		Jobs:    jobsFlag,
		Color:   color,
		Logger:  logger,
	}
	configPath := filepath.Join(x.RootMetaDir(), ConfigFile)
	if _, err := os.Stat(configPath); err == nil {
		x.config, err = ConfigFromFile(configPath)
		if err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	x.Cache, err = findCache(root, x.config)
	if x.config != nil {
		x.Shared = x.config.Shared
	}

	if err != nil {
		return nil, err
	}
	if ctx.Env()[PreservePathEnv] == "" {
		// Prepend .jiri_root/bin to the PATH, so execing a binary will
		// invoke the one in that directory, if it exists.  This is crucial for jiri
		// subcommands, where we want to invoke the binary that jiri installed, not
		// whatever is in the user's PATH.
		//
		// Note that we must modify the actual os env variable with os.SetEnv and
		// also the ctx.env, so that execing a binary through the os/exec package
		// and with ctx.Run both have the correct behavior.
		newPath := envvar.PrependUniqueToken(ctx.Env()["PATH"], string(os.PathListSeparator), x.BinDir())
		ctx.Env()["PATH"] = newPath
		if err := os.Setenv("PATH", newPath); err != nil {
			return nil, err
		}
	}
	return x, nil
}

const DefaultJobs = 25

func cleanPath(path string) (string, error) {
	result, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("EvalSymlinks(%v) failed: %v", path, err)
	}
	if !filepath.IsAbs(result) {
		return "", fmt.Errorf("%v isn't an absolute path", result)
	}
	return filepath.Clean(result), nil
}

func findCache(root string, config *Config) (string, error) {
	// Use flag variable if set.
	if config != nil && config.CachePath != "" {
		return cleanPath(config.CachePath)
	}

	// Check default location under .jiri_root.
	defaultCache := filepath.Join(root, DefaultCacheSubdir)
	fi, err := os.Stat(defaultCache)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	// .jiri_root/cache exists and is a directory (success).
	if fi.IsDir() {
		return defaultCache, nil
	}

	// defaultCache exists but is not a directory.  Assume the user is
	// up to something and there's no real cache directory.
	return "", nil
}

func findJiriRoot(timer *timing.Timer) (string, error) {
	if timer != nil {
		timer.Push("find .jiri_root")
		defer timer.Pop()
	}

	if rootFlag != "" {
		return cleanPath(rootFlag)
	}

	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	path, err := filepath.Abs(wd)
	if err != nil {
		return "", err
	}

	paths := []string{path}
	for i := len(path) - 1; i >= 0; i-- {
		if os.IsPathSeparator(path[i]) {
			path = path[:i]
			if path == "" {
				path = "/"
			}
			paths = append(paths, path)
		}
	}

	for _, path := range paths {
		fi, err := os.Stat(filepath.Join(path, RootMetaDir))
		if err == nil && fi.IsDir() {
			return path, nil
		}
	}

	return "", fmt.Errorf("cannot find %v", RootMetaDir)
}

// FindRoot returns the root directory of the jiri environment.  All state
// managed by jiri resides under this root.
//
// If the rootFlag variable is non-empty, we always attempt to use it.
// It must point to an absolute path, after symlinks are evaluated.
//
// Returns an empty string if the root directory cannot be determined, or if any
// errors are encountered.
//
// FindRoot should be rarely used; typically you should use NewX to create a new
// execution environment, and handle errors.  An example of a valid usage is to
// initialize default flag values in an init func before main.
func FindRoot() string {
	root, _ := findJiriRoot(nil)
	return root
}

// Clone returns a clone of the environment.
func (x *X) Clone(opts tool.ContextOpts) *X {
	return &X{
		Context:  x.Context.Clone(opts),
		Root:     x.Root,
		Usage:    x.Usage,
		Jobs:     x.Jobs,
		Cache:    x.Cache,
		Color:    x.Color,
		Logger:   x.Logger,
		failures: x.failures,
	}
}

// UsageErrorf prints the error message represented by the printf-style format
// and args, followed by the usage output.  The implementation typically calls
// cmdline.Env.UsageErrorf.
func (x *X) UsageErrorf(format string, args ...interface{}) error {
	if x.Usage != nil {
		return x.Usage(format, args...)
	}
	return fmt.Errorf(format, args...)
}

// RootMetaDir returns the path to the root metadata directory.
func (x *X) RootMetaDir() string {
	return filepath.Join(x.Root, RootMetaDir)
}

// JiriManifestFile returns the path to the .jiri_manifest file.
func (x *X) JiriManifestFile() string {
	return filepath.Join(x.Root, JiriManifestFile)
}

// BinDir returns the path to the bin directory.
func (x *X) BinDir() string {
	return filepath.Join(x.RootMetaDir(), "bin")
}

// ScriptsDir returns the path to the scripts directory.
func (x *X) ScriptsDir() string {
	return filepath.Join(x.RootMetaDir(), "scripts")
}

// UpdateHistoryDir returns the path to the update history directory.
func (x *X) UpdateHistoryDir() string {
	return filepath.Join(x.RootMetaDir(), "update_history")
}

// UpdateHistoryLatestLink returns the path to a symlink that points to the
// latest update in the update history directory.
func (x *X) UpdateHistoryLatestLink() string {
	return filepath.Join(x.UpdateHistoryDir(), "latest")
}

// UpdateHistorySecondLatestLink returns the path to a symlink that points to
// the second latest update in the update history directory.
func (x *X) UpdateHistorySecondLatestLink() string {
	return filepath.Join(x.UpdateHistoryDir(), "second-latest")
}

// RunnerFunc is an adapter that turns regular functions into cmdline.Runner.
// This is similar to cmdline.RunnerFunc, but the first function argument is
// jiri.X, rather than cmdline.Env.
func RunnerFunc(run func(*X, []string) error) cmdline.Runner {
	return runner(run)
}

type runner func(*X, []string) error

func (r runner) Run(env *cmdline.Env, args []string) error {
	x, err := NewX(env)
	if err != nil {
		return err
	}
	enablesdAnalytics := false
	userId := ""
	analyticsCommandMsg := fmt.Sprintf("To check what data we collect run '%s'\n"+
		"To opt-in run '%s'\n"+
		"To opt-out run '%s'",
		x.Color.Yellow("jiri init -show-analytics-data"),
		x.Color.Yellow("jiri init -analytics-opt=true %q", x.Root),
		x.Color.Yellow("jiri init -analytics-opt=false %q", x.Root))
	if x.config == nil || x.config.AnalyticsOptIn == "" {
		x.Logger.Warningf("Please opt in or out of analytics collection. You will receive this warning until an option is selected.\n%s\n\n", analyticsCommandMsg)
	} else if x.config.AnalyticsOptIn == "yes" {
		if x.config.AnalyticsUserId == "" || x.config.AnalyticsVersion == "" {
			x.Logger.Warningf("Please opt in or out of analytics collection. You will receive this warning until an option is selected.\n%s\n\n", analyticsCommandMsg)
		} else if x.config.AnalyticsVersion != analytics_util.Version {
			x.Logger.Warningf("You have opted in for old version of data collection. Please opt in/out again\n%s\n\n", analyticsCommandMsg)
		} else {
			userId = x.config.AnalyticsUserId
			enablesdAnalytics = true
		}
	}
	as := analytics_util.NewAnalyticsSession(enablesdAnalytics, "UA-101128147-1", userId)
	id := as.AddCommand(env.CommandName, env.CommandFlags)

	err = r(x, args)
	x.Logger.DisableProgress()

	as.Done(id)
	as.SendAllAndWaitToFinish()
	return err
}
