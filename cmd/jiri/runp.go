// Copyright 2015 The Vanadium Authors, All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"sync"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cmdline"
	"fuchsia.googlesource.com/jiri/envvar"
	"fuchsia.googlesource.com/jiri/project"
	"fuchsia.googlesource.com/jiri/simplemr"
	"fuchsia.googlesource.com/jiri/tool"
)

var runpFlags struct {
	projectKeys    string
	verbose        bool
	interactive    bool
	uncommitted    bool
	noUncommitted  bool
	untracked      bool
	noUntracked    bool
	showNamePrefix bool
	showKeyPrefix  bool
	exitOnError    bool
	collateOutput  bool
	branch         string
}

var cmdRunP = &cmdline.Command{
	Runner: jiri.RunnerFunc(runRunp),
	Name:   "runp",
	Short:  "Run a command in parallel across jiri projects",
	Long: `Run a command in parallel across one or more jiri projects. Commands are run
using the shell specified by the users $SHELL environment variable, or "sh"
if that's not set. Thus commands are run as $SHELL -c "args..."
 `,
	ArgsName: "<command line>",
	ArgsLong: `A command line to be run in each project specified by the supplied command
line flags. Any environment variables intended to be evaluated when the
command line is run must be quoted to avoid expansion before being passed to
runp by the shell.
`,
}

func init() {
	cmdRunP.Flags.BoolVar(&runpFlags.verbose, "v", false, "Print verbose logging information")
	cmdRunP.Flags.StringVar(&runpFlags.projectKeys, "projects", "", "A Regular expression specifying project keys to run commands in. By default, runp will use projects that have the same branch checked as the current project unless it is run from outside of a project in which case it will default to using all projects.")
	cmdRunP.Flags.BoolVar(&runpFlags.uncommitted, "uncommitted", false, "Match projects that have uncommitted changes")
	cmdRunP.Flags.BoolVar(&runpFlags.noUncommitted, "no-uncommitted", false, "Match projects that have no uncommitted changes")
	cmdRunP.Flags.BoolVar(&runpFlags.untracked, "untracked", false, "Match projects that have untracked files")
	cmdRunP.Flags.BoolVar(&runpFlags.noUntracked, "no-untracked", false, "Match projects that have no untracked files")
	cmdRunP.Flags.BoolVar(&runpFlags.interactive, "interactive", false, "If set, the command to be run is interactive and should not have its stdout/stderr manipulated. This flag cannot be used with -show-name-prefix, -show-key-prefix or -collate-stdout.")
	cmdRunP.Flags.BoolVar(&runpFlags.showNamePrefix, "show-name-prefix", false, "If set, each line of output from each project will begin with the name of the project followed by a colon. This is intended for use with long running commands where the output needs to be streamed. Stdout and stderr are spliced apart. This flag cannot be used with -interactive, -show-key-prefix or -collate-stdout.")
	cmdRunP.Flags.BoolVar(&runpFlags.showKeyPrefix, "show-key-prefix", false, "If set, each line of output from each project will begin with the key of the project followed by a colon. This is intended for use with long running commands where the output needs to be streamed. Stdout and stderr are spliced apart. This flag cannot be used with -interactive, -show-name-prefix or -collate-stdout")
	cmdRunP.Flags.BoolVar(&runpFlags.collateOutput, "collate-stdout", true, "Collate all stdout output from each parallel invocation and display it as if had been generated sequentially. This flag cannot be used with -show-name-prefix, -show-key-prefix or -interactive.")
	cmdRunP.Flags.BoolVar(&runpFlags.exitOnError, "exit-on-error", false, "If set, all commands will killed as soon as one reports an error, otherwise, each will run to completion.")
	cmdRunP.Flags.StringVar(&runpFlags.branch, "branch", "", "A regular expression specifying branch names to use in matching projects. A project will match if the specified branch exists, even if it is not checked out.")
}

type mapInput struct {
	project.Project
	key          project.ProjectKey
	jirix        *jiri.X
	index, total int
	result       error
}

func newmapInput(jirix *jiri.X, project project.Project, key project.ProjectKey, index, total int) *mapInput {
	return &mapInput{
		Project: project,
		key:     key,
		jirix:   jirix.Clone(tool.ContextOpts{}),
		index:   index,
		total:   total,
	}
}

func projectNames(mapInputs map[project.ProjectKey]*mapInput) []string {
	n := []string{}
	for _, mi := range mapInputs {
		n = append(n, mi.Project.Name)
	}
	sort.Strings(n)
	return n
}

func projectKeys(mapInputs map[project.ProjectKey]*mapInput) []string {
	n := []string{}
	for key := range mapInputs {
		n = append(n, string(key))
	}
	sort.Strings(n)
	return n
}

type runner struct {
	args                 []string
	serializedWriterLock sync.Mutex
	collatedOutputLock   sync.Mutex
}

func (r *runner) serializedWriter(w io.Writer) io.Writer {
	return &sharedLockWriter{&r.serializedWriterLock, w}
}

type sharedLockWriter struct {
	mu *sync.Mutex
	f  io.Writer
}

func (lw *sharedLockWriter) Write(d []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.f.Write(d)
}

func copyWithPrefix(prefix string, w io.Writer, r io.Reader) {
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if line != "" {
				fmt.Fprintf(w, "%v: %v\n", prefix, line)
			}
			break
		}
		fmt.Fprintf(w, "%v: %v", prefix, line)
	}
}

type mapOutput struct {
	mi             *mapInput
	outputFilename string
	key            string
	err            error
}

func (r *runner) Map(mr *simplemr.MR, key string, val interface{}) error {
	mi := val.(*mapInput)
	output := &mapOutput{
		key: key,
		mi:  mi}
	jirix := mi.jirix
	path := os.Getenv("SHELL")
	if path == "" {
		path = "sh"
	}
	var wg sync.WaitGroup
	cmd := exec.Command(path, "-c", strings.Join(r.args, " "))
	cmd.Env = envvar.MapToSlice(jirix.Env())
	cmd.Dir = mi.Project.Path
	cmd.Stdin = mi.jirix.Stdin()
	var stdoutCloser, stderrCloser io.Closer
	if runpFlags.interactive {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	} else {
		var stdout io.Writer
		stderr := r.serializedWriter(jirix.Stderr())
		var cleanup func()
		if runpFlags.collateOutput {
			// Write standard output to a file, stderr
			// is not collated.
			f, err := ioutil.TempFile("", "jiri-runp-")
			if err != nil {
				return err
			}
			stdout = f
			output.outputFilename = f.Name()
			cleanup = func() {
				os.Remove(output.outputFilename)
			}
			// The child process will have exited by the
			// time this method returns so it's safe to close the file
			// here.
			defer f.Close()
		} else {
			stdout = r.serializedWriter(os.Stdout)
			cleanup = func() {}
		}
		if !runpFlags.showNamePrefix && !runpFlags.showKeyPrefix {
			// write directly to stdout, stderr if there's no prefix
			cmd.Stdout = stdout
			cmd.Stderr = stderr
		} else {
			stdoutReader, stdoutWriter, err := os.Pipe()
			if err != nil {
				cleanup()
				return err
			}
			stderrReader, stderrWriter, err := os.Pipe()
			if err != nil {
				cleanup()
				stdoutReader.Close()
				stdoutWriter.Close()
				return err
			}
			cmd.Stdout = stdoutWriter
			cmd.Stderr = stderrWriter
			// Record the write end of the pipe so that it can be closed
			// after the child has exited, this ensures that all goroutines
			// will finish.
			stdoutCloser = stdoutWriter
			stderrCloser = stderrWriter
			prefix := key
			if runpFlags.showNamePrefix {
				prefix = mi.Project.Name
			}
			wg.Add(2)
			go func() { copyWithPrefix(prefix, stdout, stdoutReader); wg.Done() }()
			go func() { copyWithPrefix(prefix, stderr, stderrReader); wg.Done() }()

		}
	}
	if err := cmd.Start(); err != nil {
		mi.result = err
	}
	done := make(chan error)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case output.err = <-done:
		if output.err != nil && runpFlags.exitOnError {
			mr.Cancel()
		}
	case <-mr.CancelCh():
		output.err = cmd.Process.Kill()
	}
	for _, closer := range []io.Closer{stdoutCloser, stderrCloser} {
		if closer != nil {
			closer.Close()
		}
	}
	wg.Wait()
	mr.MapOut(key, output)
	return nil
}

func (r *runner) Reduce(mr *simplemr.MR, key string, values []interface{}) error {
	for _, v := range values {
		mo := v.(*mapOutput)
		if mo.err != nil {
			fmt.Fprintf(os.Stdout, "FAILED: %v: %s %v\n", mo.key, strings.Join(r.args, " "), mo.err)
			return nil
		} else {
			if runpFlags.collateOutput {
				r.collatedOutputLock.Lock()
				defer r.collatedOutputLock.Unlock()
				defer os.Remove(mo.outputFilename)
				if fi, err := os.Open(mo.outputFilename); err == nil {
					io.Copy(os.Stdout, fi)
					fi.Close()
				} else {
					return err
				}
			}
		}
	}
	return nil
}

func runRunp(jirix *jiri.X, args []string) error {
	if runpFlags.interactive {
		runpFlags.collateOutput = false
	}

	var keysRE, branchRE *regexp.Regexp
	var err error

	if runpFlags.projectKeys != "" {
		re := ""
		for _, pre := range strings.Split(runpFlags.projectKeys, ",") {
			re += pre + "|"
		}
		re = strings.TrimRight(re, "|")
		keysRE, err = regexp.Compile(re)
		if err != nil {
			return fmt.Errorf("failed to compile projects regexp: %q: %v", runpFlags.projectKeys, err)
		}
	}

	if runpFlags.branch != "" {
		branchRE, err = regexp.Compile(runpFlags.branch)
		if err != nil {
			return fmt.Errorf("failed to compile has-branch regexp: %q: %v", runpFlags.branch, err)
		}
	}

	if (runpFlags.showKeyPrefix || runpFlags.showNamePrefix) && runpFlags.interactive {
		fmt.Fprintf(jirix.Stderr(), "WARNING: interactive mode being disabled because show-key-prefix or show-name-prefix was set\n")
		runpFlags.interactive = false
		runpFlags.collateOutput = true
	}

	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("os.Getwd() failed: %v", err)
	}
	if dir == jirix.Root || err != nil {
		// jiri was run from outside of a project. Let's assume we'll
		// use all projects if none have been specified via the projects flag.
		if keysRE == nil {
			keysRE = regexp.MustCompile(".*")
		}
	}
	projects, err := project.LocalProjects(jirix, project.FastScan)
	if err != nil {
		return err
	}

	projectStateRequired := branchRE != nil || runpFlags.untracked || runpFlags.noUntracked || runpFlags.uncommitted || runpFlags.noUncommitted
	var states map[project.ProjectKey]*project.ProjectState
	if projectStateRequired {
		var err error
		states, err = project.GetProjectStates(jirix, projects, runpFlags.untracked || runpFlags.noUntracked || runpFlags.uncommitted || runpFlags.noUncommitted)
		if err != nil {
			return err
		}
	}
	mapInputs := map[project.ProjectKey]*mapInput{}
	var keys project.ProjectKeys
	for _, localProject := range projects {
		key := localProject.Key()
		if keysRE != nil {
			if !keysRE.MatchString(string(key)) {
				continue
			}
		}
		state := states[key]
		if branchRE != nil {
			found := false
			for _, br := range state.Branches {
				if branchRE.MatchString(br.Name) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if (runpFlags.untracked && !state.HasUntracked) || (runpFlags.noUntracked && state.HasUntracked) {
			continue
		}
		if (runpFlags.uncommitted && !state.HasUncommitted) || (runpFlags.noUncommitted && state.HasUncommitted) {
			continue
		}
		mapInputs[key] = &mapInput{
			Project: localProject,
			jirix:   jirix,
			key:     key,
		}
		keys = append(keys, key)
	}

	total := len(mapInputs)
	index := 1
	for _, mi := range mapInputs {
		mi.index = index
		mi.total = total
		index++
	}

	if runpFlags.verbose {
		fmt.Fprintf(os.Stdout, "Project Names: %s\n", strings.Join(projectNames(mapInputs), " "))
		fmt.Fprintf(os.Stdout, "Project Keys: %s\n", strings.Join(projectKeys(mapInputs), " "))
	}

	runner := &runner{
		args: args,
	}
	mr := simplemr.MR{}
	if runpFlags.interactive {
		// Run one mapper at a time.
		mr.NumMappers = 1
		sort.Sort(keys)
	} else {
		mr.NumMappers = int(jirix.Jobs)
	}
	in, out := make(chan *simplemr.Record, len(mapInputs)), make(chan *simplemr.Record, len(mapInputs))
	sigch := make(chan os.Signal)
	signal.Notify(sigch, os.Interrupt)
	jirix.TimerPush("Map and Reduce")
	go func() { <-sigch; mr.Cancel() }()
	go mr.Run(in, out, runner, runner)
	for _, key := range keys {
		in <- &simplemr.Record{string(key), []interface{}{mapInputs[key]}}
	}
	close(in)
	<-out
	jirix.TimerPop()
	return mr.Error()
}
