// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runutil

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"v.io/x/lib/cmdline"
)

// Sequence provides for convenient chaining of multiple calls to its
// methods to avoid repeated tests for error returns. The usage is:
//
// err := s.Run("echo", "a").Run("echo", "b").Done()
//
// The first method to encounter an error short circuits any following
// methods and the result of that first error is returned by the
// Done method or any of the other 'terminating methods' (see below).
// Sequence is not thread safe. It also good practice to use a new
// instance of a Sequence in defer's.
//
// Unless directed to specific stdout and stderr io.Writers using Capture(),
// the stdout and stderr output from the command is discarded, unless an error
// is encountered, in which case the output from the command that failed (both
// stdout and stderr) is written to the stderr io.Writer specified via
// NewSequence. In addition, in verbose mode, command execution logging
// is written to the stdout an stderr io.Writers configured via NewSequence.
//
// Modifier methods are provided that influence the behaviour of the
// next invocation of the Run method to set timeouts (Timed), to
// capture output (Capture), input (Read) and to set the environment (Env).
// For example, the following will result in a timeout error.
//
// err := s.Timeout(time.Second).Run("sleep","10").Done()
// err := s.Timeout(time.Second).Last("sleep","10")
//
// A sequence of commands must be terminated with a call to a 'terminating'
// method. The simplest are the Done or Last methods used in the examples above,
// but there are other methods which typically return results in addition to
// error, such as ReadFile(filename string) ([]byte, error). Here the usage
// would be:
//
// o.Stdout, _ = os.Create("foo")
// data, err := s.Capture(o, nil).Run("echo","b").ReadFile("foo")
// // data == "b"
//
// Note that terminating functions, even those that take an action, may
// return an error generated by a previous method.
//
// In addition to Run which will always run a command as a subprocess,
// the Call method will invoke a function. Note that Capture and Timeout
// do not affect such calls.
//
// Errors returned by Sequence augment those returned by the underlying
// packages with details of the exact call that generated those errors.
// This means that it is not possible to test directly for errors from
// those packages. The GetOriginalError function can be used to obtain
// the error from the underlying package, or the IsTimeout, IsNotExists etc
// functions can be used on the wrapped error. The ExitCode method
// is also provided to convert to the exit codes expected by the
// v.io/x/lib/cmdline package which is often used in conjunction with
// Sequence.
type Sequence struct {
	// NOTE: we use a struct as the return value of all
	// Sequence methods to ensure that code of the form:
	// if err := s.WriteFile(); err != nil {...}
	// does not compile.
	*sequence
}

type sequence struct {
	r                            *executor
	err                          error
	caller                       string
	stdout, stderr               io.Writer
	stdin                        io.Reader
	reading                      bool
	env                          map[string]string
	opts                         *opts
	defaultStdin                 io.Reader
	defaultStdout, defaultStderr io.Writer
	dirs                         []string
	verbosity                    *bool
	dryRun                       *bool
	cmdDir                       string
	timeout                      time.Duration
	serializedWriterLock         sync.Mutex
}

// NewSequence creates an instance of Sequence with default values for its
// environment, stdin, stderr, stdout and other supported options.
func NewSequence(env map[string]string, stdin io.Reader, stdout, stderr io.Writer, color, dryRun, verbose bool) Sequence {
	s := Sequence{
		&sequence{
			r:            newExecutor(env, stdin, stdout, stderr, color, dryRun, verbose),
			defaultStdin: stdin,
		},
	}
	s.defaultStdout, s.defaultStderr = s.serializeWriter(stdout), s.serializeWriter(stderr)
	return s
}

// RunOpts returns the values of dryRun and verbose that were used to
// create this sequence.
func (s Sequence) RunOpts() (dryRun bool, verbose bool) {
	opts := s.getOpts()
	return opts.dryRun, opts.verbose
}

// Capture arranges for the next call to Run or Last to write its stdout and
// stderr output to the supplied io.Writers. This will be cleared and not used
// for any calls to Run or Last beyond the next one. Specifying nil for
// a writer will result in using the the corresponding io.Writer supplied
// to NewSequence. ioutil.Discard should be used to discard output.
func (s Sequence) Capture(stdout, stderr io.Writer) Sequence {
	if s.err != nil {
		return s
	}
	s.stdout, s.stderr = stdout, stderr
	return s
}

// Read arranges for the next call to Run or Last to read from the supplied
// io.Reader. This will be cleared and not used for any calls to Run or Last
// beyond the next one. Specifying nil will result in reading from os.DevNull.
func (s Sequence) Read(stdin io.Reader) Sequence {
	if s.err != nil {
		return s
	}
	s.reading = true
	s.stdin = stdin
	return s
}

// Env arranges for the next call to Run, Call, Start or Last to use the supplied
// environment variables. This will be cleared and not used for any calls
// to Run, Call or Last beyond the next one.
func (s Sequence) Env(env map[string]string) Sequence {
	if s.err != nil {
		return s
	}
	s.env = env
	return s
}

// Verbosity arranges for the next call to Run, Call, Start or Last to use the
// specified verbosity. This will be cleared and not used for any calls
// to Run, Call or Last beyond the next one.
func (s Sequence) Verbose(verbosity bool) Sequence {
	if s.err != nil {
		return s
	}
	s.verbosity = &verbosity
	return s
}

// Dir sets the working directory for the next subprocess that is created
// via Run, Call, Start or Last to the supplied parameter. This is the only
// way to safely set the working directory of a command when multiple threads
// are used.
func (s Sequence) Dir(dir string) Sequence {
	if s.err != nil {
		return s
	}
	s.cmdDir = dir
	return s
}

// DryRun arranges for the next call to Run, Call, Start or Last to use the
// specified dry run value. This will be cleared and not used for any calls
// to Run, Call or Last beyond the next one.
func (s Sequence) DryRun(dryRun bool) Sequence {
	if s.err != nil {
		return s
	}
	s.dryRun = &dryRun
	return s
}

// internal getOpts that doesn't override stdin, stdout, stderr
func (s Sequence) getOpts() opts {
	var opts opts
	if s.opts != nil {
		opts = *s.opts
	} else {
		opts = s.r.opts
	}
	return opts
}

// Timeout arranges for the next call to Run, Start or Last to be subject to the
// specified timeout. The timeout will be cleared and not used any calls to Run
// or Last beyond the next one. It has no effect for calls to Call.
func (s Sequence) Timeout(timeout time.Duration) Sequence {
	if s.err != nil {
		return s
	}
	s.timeout = timeout
	return s
}

func (s Sequence) setOpts(opts opts) {
	s.opts = &opts
}

type wrappedError struct {
	oe, we error
}

func (ie *wrappedError) Error() string {
	return ie.we.Error()
}

// Error returns the error, if any, stored in the Sequence.
func (s Sequence) Error() error {
	if s.err != nil && len(s.caller) > 0 {
		return &wrappedError{oe: s.err, we: fmt.Errorf("%s: %v", s.caller, s.err)}
	}
	return s.err
}

// TranslateExitCode translates errors from the "os/exec" package that
// contain exit codes into cmdline.ErrExitCode errors.
func TranslateExitCode(err error) error {
	return translateExitCode(GetOriginalError(err))
}

func translateExitCode(err error) error {
	if exit, ok := err.(*exec.ExitError); ok {
		if wait, ok := exit.Sys().(syscall.WaitStatus); ok {
			if status := wait.ExitStatus(); wait.Exited() && status != 0 {
				return cmdline.ErrExitCode(status)
			}
		}
	}
	return err
}

// GetOriginalError gets the original error wrapped in the supplied err.
// If the given err has not been wrapped by Sequence, then the supplied error
// is returned.
func GetOriginalError(err error) error {
	if we, ok := err.(*wrappedError); ok {
		return we.oe
	}
	return err
}

// IsExist returns a boolean indicating whether the error is known
// to report that a file or directory already exists.
func IsExist(err error) bool {
	if we, ok := err.(*wrappedError); ok {
		return os.IsExist(we.oe)
	}
	return os.IsExist(err)
}

// IsNotExist returns a boolean indicating whether the error is known
// to report that a file or directory does not exist.
func IsNotExist(err error) bool {
	if we, ok := err.(*wrappedError); ok {
		return os.IsNotExist(we.oe)
	}
	return os.IsNotExist(err)
}

// IsPermission returns a boolean indicating whether the error is known
// to report that permission is denied.
func IsPermission(err error) bool {
	if we, ok := err.(*wrappedError); ok {
		return os.IsPermission(we.oe)
	}
	return os.IsPermission(err)
}

// IsTimeout returns a boolean indicating whether the error is a result of
// a timeout.
func IsTimeout(err error) bool {
	if we, ok := err.(*wrappedError); ok {
		return we.oe == commandTimedOutErr
	}
	return err == commandTimedOutErr
}

func fmtError(depth int, err error, detail string) string {
	_, file, line, _ := runtime.Caller(depth + 1)
	return fmt.Sprintf("%s:%d: %s", filepath.Base(file), line, detail)
}

func (s Sequence) setError(err error, detail string) {
	if err == nil || s.err != nil {
		return
	}
	s.err = err
	s.caller = fmtError(2, err, detail)
}

// reset all state except s.err
func (s Sequence) reset() {
	s.stdin, s.stdout, s.stderr, s.env = nil, nil, nil, nil
	s.opts, s.verbosity, s.dryRun = nil, nil, nil
	s.cmdDir = ""
	s.reading = false
	s.timeout = 0
}

func cleanup(p1, p2 *io.PipeWriter, stdinCh, stderrCh chan error) error {
	p1.Close()
	p2.Close()
	if stdinCh != nil {
		if err := <-stdinCh; err != nil {
			return err
		}
	}
	if stderrCh != nil {
		if err := <-stderrCh; err != nil {
			return err
		}
	}
	return nil
}

func useIfNotNil(a, b io.Writer) io.Writer {
	if a != nil {
		return a
	}
	return b
}

func writeOutput(logdir bool, from string, to io.Writer) {
	if fi, err := os.Open(from); err == nil {
		io.Copy(to, fi)
		fi.Close()
	}
	if !logdir {
		return
	}
	if wd, err := os.Getwd(); err == nil {
		fmt.Fprintf(to, "Current Directory: %v\n", wd)
	}
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

func (s Sequence) serializeWriter(a io.Writer) io.Writer {
	if a != nil {
		return &sharedLockWriter{&s.serializedWriterLock, a}
	}
	return nil
}

func (s Sequence) initAndDefer(h *Handle) func() {
	if s.stdout == nil && s.stderr == nil {
		fout, err := ioutil.TempFile("", "seq")
		if err != nil {
			return func() {}
		}
		opts := s.getOpts()
		opts.stdout, opts.stderr = s.serializeWriter(fout), s.serializeWriter(fout)
		opts.env = s.env
		if s.reading {
			opts.stdin = s.stdin
		}
		if s.verbosity != nil {
			opts.verbose = *s.verbosity
		}
		if s.dryRun != nil {
			opts.dryRun = *s.dryRun
		}
		opts.dir = s.cmdDir
		s.setOpts(opts)
		if h != nil {
			return func() {
				h.stderr = useIfNotNil(s.defaultStderr, os.Stderr)
				h.filename = fout.Name()
				h.doneErr = nil
				fout.Close()
			}
		}
		return func() {
			filename := fout.Name()
			fout.Close()
			defer func() { os.Remove(filename); s.opts = nil }()
			if s.err != nil {
				writeOutput(true, filename, useIfNotNil(s.defaultStderr, os.Stderr))
			}
			if opts.verbose && s.defaultStderr != s.defaultStdout {
				writeOutput(false, filename, useIfNotNil(s.defaultStdout, os.Stdout))
			}
		}
	}
	opts := s.getOpts()
	rStdout, wStdout := io.Pipe()
	rStderr, wStderr := io.Pipe()
	opts.stdout = wStdout
	opts.stderr = wStderr
	opts.env = s.env
	if s.reading {
		opts.stdin = s.stdin
	}
	var stdinCh, stderrCh chan error
	stdout, stderr := s.serializeWriter(s.stdout), s.serializeWriter(s.stderr)
	if stdout != nil {
		stdinCh = make(chan error)
		go copy(stdout, rStdout, stdinCh)
	} else {
		opts.stdout = s.defaultStdout
	}
	if stderr != nil {
		stderrCh = make(chan error)
		go copy(stderr, rStderr, stderrCh)
	} else {
		opts.stderr = s.defaultStderr
	}
	if s.verbosity != nil {
		opts.verbose = *s.verbosity
	}
	if s.dryRun != nil {
		opts.dryRun = *s.dryRun
	}
	opts.dir = s.cmdDir
	s.setOpts(opts)
	if h != nil {
		return func() {
			h.filename = ""
			h.doneErr = cleanup(wStdout, wStderr, stdinCh, stderrCh)
		}
	}
	return func() {
		if err := cleanup(wStdout, wStderr, stdinCh, stderrCh); err != nil && s.err == nil {
			// If we haven't already encountered an error and we fail to
			// cleanup then record the error from the cleanup
			s.err = err
		}
		// reset does not affect s.err
		s.reset()
	}
}

func fmtStringArgs(args ...string) string {
	if len(args) == 0 {
		return ""
	}
	var out bytes.Buffer
	for _, a := range args {
		fmt.Fprintf(&out, ", %q", a)
	}
	return out.String()
}

// Run runs the given command as a subprocess.
func (s Sequence) Run(path string, args ...string) Sequence {
	if s.err != nil {
		return s
	}
	defer s.initAndDefer(nil)()
	s.setError(s.r.run(s.timeout, s.getOpts(), path, args...), fmt.Sprintf("Run(%q%s)", path, fmtStringArgs(args...)))
	return s
}

// Last runs the given command as a subprocess and returns an error
// immediately terminating the sequence, it is equivalent to
// calling s.Run(path, args...).Done().
func (s Sequence) Last(path string, args ...string) error {
	if s.err != nil {
		return s.Done()
	}
	defer s.Done()
	defer s.initAndDefer(nil)()
	s.setError(s.r.run(s.timeout, s.getOpts(), path, args...), fmt.Sprintf("Last(%q%s)", path, fmtStringArgs(args...)))
	return s.Error()
}

// Call runs the given function. Note that Capture and Timeout have no
// effect on invocations of Call, but Opts can control logging.
func (s Sequence) Call(fn func() error, format string, args ...interface{}) Sequence {
	if s.err != nil {
		return s
	}
	defer s.initAndDefer(nil)()
	s.setError(s.r.function(s.getOpts(), fn, format, args...), fmt.Sprintf(format, args))
	return s
}

// Handle represents a command running in the background.
type Handle struct {
	stdout, stderr io.Writer
	doneErr        error
	filename       string
	deferFn        func()
	cmd            *exec.Cmd
}

// Kill terminates the currently running background process.
func (h *Handle) Kill() error {
	return h.cmd.Process.Kill()
}

// Pid returns the pid of the running process.
func (h *Handle) Pid() int {
	return h.cmd.Process.Pid
}

func (h *Handle) Signal(sig os.Signal) error {
	return h.cmd.Process.Signal(sig)
}

// Wait waits for the currently running background process to terminate.
func (h *Handle) Wait() error {
	err := h.cmd.Wait()
	h.deferFn()
	if len(h.filename) > 0 {
		if err != nil {
			writeOutput(true, h.filename, h.stderr)
		}
		os.Remove(h.filename)
		return err
	}
	return h.doneErr
}

// Start runs the given command as a subprocess in background and returns
// a handle that can be used to kill and/or wait for that background process.
// Start is a terminating function.
func (s Sequence) Start(path string, args ...string) (*Handle, error) {
	if s.err != nil {
		return nil, s.Done()
	}
	h := &Handle{}
	h.deferFn = s.initAndDefer(h)
	cmd, err := s.r.start(s.timeout, s.getOpts(), path, args...)
	h.cmd = cmd
	s.setError(err, fmt.Sprintf("Start(%q%s)", path, fmtStringArgs(args...)))
	return h, s.Error()
}

// Output logs the given list of lines using the currently in effect verbosity
// as specified by Opts, or the default otherwise.
func (s Sequence) Output(output []string) Sequence {
	if s.err != nil {
		return s
	}
	opts := s.getOpts()
	if s.verbosity != nil {
		opts.verbose = *s.verbosity
	}
	s.r.output(opts, output)
	return s
}

// Fprintf calls fmt.Fprintf.
func (s Sequence) Fprintf(f io.Writer, format string, args ...interface{}) Sequence {
	if s.err != nil {
		return s
	}
	fmt.Fprintf(f, format, args...)
	return s
}

// Done returns the error stored in the Sequence and pops back to the first
// entry in the directory stack if Pushd has been called. Done is a terminating
// function. There is no need to ensure that Done is called before returning
// from a function that uses a sequence unless it is necessary to pop the
// stack.
func (s Sequence) Done() error {
	rerr := s.Error()
	s.err = nil
	s.caller = ""
	s.reset()
	if len(s.dirs) > 0 {
		cwd := s.dirs[0]
		s.dirs = nil
		err := s.r.alwaysRun(func() error {
			return os.Chdir(cwd)
		}, fmt.Sprintf("sequence done popd %q", cwd))
		if err != nil {
			detail := "Done: Chdir(" + cwd + ")"
			if rerr == nil {
				s.setError(err, detail)
			} else {
				// In the unlikely event that Chdir fails in addition to an
				// earlier error, we append an appropriate error message.
				s.err = fmt.Errorf("%v\n%v", rerr, fmtError(1, err, detail))
			}
			return s.Error()
		}
	}
	return rerr
}

// Pushd pushes the current directory onto a stack and changes directory
// to the specified one. Calling any terminating function will pop back
// to the first element in the stack on completion of that function.
func (s Sequence) Pushd(dir string) Sequence {
	cwd, err := os.Getwd()
	if err != nil {
		s.setError(err, "Pushd("+dir+"): os.Getwd")
		return s
	}
	s.dirs = append(s.dirs, cwd)
	err = s.r.alwaysRun(func() error {
		return os.Chdir(dir)
	}, fmt.Sprintf("pushd %q", dir))
	s.setError(err, "Pushd("+dir+")")
	return s
}

// Popd popds the last directory from the directory stack and chdir's to it.
// Calling any termination function will pop back to the first element in
// the stack on completion of that function.
func (s Sequence) Popd() Sequence {
	if s.err != nil {
		return s
	}
	if len(s.dirs) == 0 {
		s.setError(fmt.Errorf("directory stack is empty"), "Popd()")
		return s
	}
	last := s.dirs[len(s.dirs)-1]
	s.dirs = s.dirs[:len(s.dirs)-1]
	err := s.r.alwaysRun(func() error {
		return os.Chdir(last)
	}, fmt.Sprintf("popd %q", last))
	s.setError(err, "Popd() -> "+last)
	return s
}

// Chdir is a wrapper around os.Chdir that handles options such as
// "verbose" or "dry run".
func (s Sequence) Chdir(dir string) Sequence {
	if s.err != nil {
		return s
	}
	err := s.r.alwaysRun(func() error {
		return os.Chdir(dir)
	}, fmt.Sprintf("cd %q", dir))
	s.setError(err, "Chdir("+dir+")")
	return s

}

// Chmod is a wrapper around os.Chmod that handles options such as
// "verbose" or "dry run".
func (s Sequence) Chmod(dir string, mode os.FileMode) Sequence {
	if s.err != nil {
		return s
	}
	err := s.r.call(func() error { return os.Chmod(dir, mode) }, fmt.Sprintf("chmod %v %q", mode, dir))
	s.setError(err, fmt.Sprintf("Chmod(%s, %s)", dir, mode))
	return s

}

// MkdirAll is a wrapper around os.MkdirAll that handles options such
// as "verbose" or "dry run".
func (s Sequence) MkdirAll(dir string, mode os.FileMode) Sequence {
	if s.err != nil {
		return s
	}
	err := s.r.call(func() error { return os.MkdirAll(dir, mode) }, fmt.Sprintf("mkdir -p %q", dir))
	s.setError(err, fmt.Sprintf("MkdirAll(%s, %s)", dir, mode))
	return s
}

// RemoveAll is a wrapper around os.RemoveAll that handles options
// such as "verbose" or "dry run".
func (s Sequence) RemoveAll(dir string) Sequence {
	if s.err != nil {
		return s
	}
	err := s.r.call(func() error { return os.RemoveAll(dir) }, fmt.Sprintf("rm -rf %q", dir))
	s.setError(err, fmt.Sprintf("RemoveAll(%s)", dir))
	return s
}

// Remove is a wrapper around os.Remove that handles options
// such as "verbose" or "dry run".
func (s Sequence) Remove(file string) Sequence {
	if s.err != nil {
		return s
	}
	err := s.r.call(func() error { return os.Remove(file) }, fmt.Sprintf("rm %q", file))
	s.setError(err, fmt.Sprintf("Remove(%s)", file))
	return s
}

// Rename is a wrapper around os.Rename that handles options such as
// "verbose" or "dry run".
func (s Sequence) Rename(src, dst string) Sequence {
	if s.err != nil {
		return s
	}
	err := s.r.call(func() error {
		if err := os.Rename(src, dst); err != nil {
			// Check if the rename operation failed
			// because the source and destination are
			// located on different mount points.
			linkErr, ok := err.(*os.LinkError)
			if !ok {
				return err
			}
			errno, ok := linkErr.Err.(syscall.Errno)
			if !ok || errno != syscall.EXDEV {
				return err
			}
			// Fall back to a non-atomic rename.
			cmd := exec.Command("mv", src, dst)
			return cmd.Run()
		}
		return nil
	}, fmt.Sprintf("mv %q %q", src, dst))
	s.setError(err, fmt.Sprintf("Rename(%s, %s)", src, dst))
	return s

}

// Symlink is a wrapper around os.Symlink that handles options such as
// "verbose" or "dry run".
func (s Sequence) Symlink(src, dst string) Sequence {
	if s.err != nil {
		return s
	}
	err := s.r.call(func() error { return os.Symlink(src, dst) }, fmt.Sprintf("ln -s %q %q", src, dst))
	s.setError(err, fmt.Sprintf("Symlink(%s, %s)", src, dst))
	return s
}

// Open is a wrapper around os.Open that handles options such as
// "verbose" or "dry run". Open is a terminating function.
func (s Sequence) Open(name string) (f *os.File, err error) {
	if s.err != nil {
		return nil, s.Done()
	}
	s.r.call(func() error {
		f, err = os.Open(name)
		return err
	}, fmt.Sprintf("open %q", name))
	s.setError(err, fmt.Sprintf("Open(%s)", name))
	err = s.Done()
	return
}

// OpenFile is a wrapper around os.OpenFile that handles options such as
// "verbose" or "dry run". OpenFile is a terminating function.
func (s Sequence) OpenFile(name string, flag int, perm os.FileMode) (f *os.File, err error) {
	if s.err != nil {
		return nil, s.Done()
	}
	s.r.call(func() error {
		f, err = os.OpenFile(name, flag, perm)
		return err
	}, fmt.Sprintf("open file %q", name))
	s.setError(err, fmt.Sprintf("OpenFile(%s, %s, %s)", name, flag, perm))
	err = s.Done()
	return
}

// Create is a wrapper around os.Create that handles options such as "verbose"
// or "dry run". Create is a terminating function.
func (s Sequence) Create(name string) (f *os.File, err error) {

	if s.err != nil {
		return nil, s.Done()
	}
	s.r.call(func() error {
		var err error
		f, err = os.Create(name)
		return err
	}, fmt.Sprintf("create %q", name))
	s.setError(err, fmt.Sprintf("Create(%s)", name))
	err = s.Done()
	return
}

// ReadDir is a wrapper around ioutil.ReadDir that handles options
// such as "verbose" or "dry run". ReadDir is a terminating function.
func (s Sequence) ReadDir(dirname string) (fi []os.FileInfo, err error) {
	if s.err != nil {
		return nil, s.Done()
	}
	s.r.alwaysRun(func() error {
		fi, err = ioutil.ReadDir(dirname)
		return err
	}, fmt.Sprintf("ls %q", dirname))
	s.setError(err, fmt.Sprintf("ReadDir(%s)", dirname))
	err = s.Done()
	return
}

// ReadFile is a wrapper around ioutil.ReadFile that handles options
// such as "verbose" or "dry run". ReadFile is a terminating function.
func (s Sequence) ReadFile(filename string) (bytes []byte, err error) {

	if s.err != nil {
		return nil, s.Done()
	}
	s.r.alwaysRun(func() error {
		bytes, err = ioutil.ReadFile(filename)
		return err
	}, fmt.Sprintf("read %q", filename))
	s.setError(err, fmt.Sprintf("ReadFile(%s)", filename))
	err = s.Done()
	return
}

// WriteFile is a wrapper around ioutil.WriteFile that handles options
// such as "verbose" or "dry run".
func (s Sequence) WriteFile(filename string, data []byte, perm os.FileMode) Sequence {
	if s.err != nil {
		return s
	}
	err := s.r.call(func() error {
		return ioutil.WriteFile(filename, data, perm)
	}, fmt.Sprintf("write %q", filename))
	s.setError(err, fmt.Sprintf("WriteFile(%s, %10s,  %s)", filename, data, perm))
	return s
}

// Copy is a wrapper around io.Copy that handles options such as "verbose" or
// "dry run". Copy is a terminating function.
func (s Sequence) Copy(dst *os.File, src io.Reader) (n int64, err error) {
	if s.err != nil {
		return 0, s.Done()
	}
	s.r.call(func() error {
		n, err = io.Copy(dst, src)
		return err
	}, fmt.Sprintf("io.copy %q", dst.Name()))
	s.setError(err, fmt.Sprintf("Copy(%s, %s)", dst, src))
	err = s.Done()
	return
}

// Stat is a wrapper around os.Stat that handles options such as
// "verbose" or "dry run". Stat is a terminating function.
func (s Sequence) Stat(name string) (fi os.FileInfo, err error) {
	if s.err != nil {
		return nil, s.Done()
	}
	s.r.alwaysRun(func() error {
		fi, err = os.Stat(name)
		return err
	}, fmt.Sprintf("stat %q", name))
	s.setError(err, fmt.Sprintf("Stat(%s)", name))
	err = s.Done()
	return
}

// Lstat is a wrapper around os.Lstat that handles options such as
// "verbose" or "dry run". Lstat is a terminating function.
func (s Sequence) Lstat(name string) (fi os.FileInfo, err error) {
	if s.err != nil {
		return nil, s.Done()
	}
	s.r.alwaysRun(func() error {
		fi, err = os.Lstat(name)
		return err
	}, fmt.Sprintf("lstat %q", name))
	s.setError(err, fmt.Sprintf("Lstat(%s)", name))
	err = s.Done()
	return
}

// Readlink is a wrapper around os.Readlink that handles options such as
// "verbose" or "dry run". Lstat is a terminating function.
func (s Sequence) Readlink(name string) (link string, err error) {
	if s.err != nil {
		return "", s.Done()
	}
	s.r.alwaysRun(func() error {
		link, err = os.Readlink(name)
		return err
	}, fmt.Sprintf("readlink %q", name))
	s.setError(err, fmt.Sprintf("Readlink(%s)", name))
	err = s.Done()
	return
}

// TempDir is a wrapper around ioutil.TempDir that handles options
// such as "verbose" or "dry run". TempDir is a terminating function.
func (s Sequence) TempDir(dir, prefix string) (tmpDir string, err error) {
	if s.err != nil {
		return "", s.Done()
	}
	if dir == "" {
		dir = os.Getenv("TMPDIR")
	}
	tmpDir = filepath.Join(dir, prefix+"XXXXXX")
	s.r.call(func() error {
		tmpDir, err = ioutil.TempDir(dir, prefix)
		return err
	}, fmt.Sprintf("mkdir -p %q", tmpDir))
	s.setError(err, fmt.Sprintf("TempDir(%s,%s)", dir, prefix))
	err = s.Done()
	return
}

// TempFile is a wrapper around ioutil.TempFile that handles options
// such as "verbose" or "dry run".
func (s Sequence) TempFile(dir, prefix string) (f *os.File, err error) {
	if s.err != nil {
		return nil, s.Done()
	}
	if dir == "" {
		dir = os.Getenv("TMPDIR")
	}
	s.r.call(func() error {
		f, err = ioutil.TempFile(dir, prefix)
		return err
	}, fmt.Sprintf("tempFile %q %q", dir, prefix))
	s.setError(err, fmt.Sprintf("TempFile(%s,%s)", dir, prefix))
	err = s.Done()
	return
}

// IsDir is a wrapper around os.Stat with appropriate logging that
// returns true of dirname exists and is a directory.
// IsDir is a terminating function.
func (s Sequence) IsDir(dirname string) (bool, error) {
	if s.err != nil {
		return false, s.Done()
	}
	var fileInfo os.FileInfo
	var err error
	err = s.r.alwaysRun(func() error {
		fileInfo, err = os.Stat(dirname)
		return err
	}, fmt.Sprintf("isdir %q", dirname))
	if err != nil {
		return false, err
	}
	s.setError(err, fmt.Sprintf("IsDir(%s)", dirname))
	return fileInfo.IsDir(), s.Done()
}

// IsFile is a wrapper around os.Stat with appropriate logging that
// returns true if file exists and is not a directory.
// IsFile is a terminating function.
func (s Sequence) IsFile(file string) (bool, error) {
	if s.err != nil {
		return false, s.Done()
	}
	var fileInfo os.FileInfo
	var err error
	err = s.r.alwaysRun(func() error {
		fileInfo, err = os.Stat(file)
		return err
	}, fmt.Sprintf("isfile %q", file))
	if err != nil {
		return false, err
	}
	s.setError(err, fmt.Sprintf("IsFile(%s)", file))
	return !fileInfo.IsDir(), s.Done()
}

// AssertDirExists asserts that the specified directory exists with appropriate
// logging.
func (s Sequence) AssertDirExists(dirname string) Sequence {
	if s.err != nil {
		return s
	}
	isdir, err := s.IsDir(dirname)
	if !isdir && err == nil {
		err = os.ErrNotExist
	}
	s.setError(err, fmt.Sprintf("AssertDirExists(%s)", dirname))
	return s
}

// AssertFileExists asserts that the specified file exists with appropriate
// logging.
func (s Sequence) AssertFileExists(file string) Sequence {
	if s.err != nil {
		return s
	}
	isfile, err := s.IsFile(file)
	if !isfile && err == nil {
		err = os.ErrNotExist
	}
	s.setError(err, fmt.Sprintf("AssertFileExists(%s)", file))
	return s
}

func copy(to io.Writer, from io.Reader, ch chan error) {
	_, err := io.Copy(to, from)
	ch <- err
}
