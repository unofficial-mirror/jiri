// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tool

import (
	"io"
	"os"

	"go.fuchsia.dev/jiri/cmdline"
	"go.fuchsia.dev/jiri/envvar"
	"go.fuchsia.dev/jiri/timing"
)

// Context represents an execution context of a tool command
// invocation. Its purpose is to enable sharing of state throughout
// the lifetime of a command invocation.
type Context struct {
	opts ContextOpts
}

// ContextOpts records the context options.
type ContextOpts struct {
	Env    map[string]string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Timer  *timing.Timer
}

// newContextOpts is the ContextOpts factory.
func newContextOpts() *ContextOpts {
	return &ContextOpts{
		Env:    map[string]string{},
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Timer:  nil,
	}
}

// initOpts initializes all unset options to the given defaults.
func initOpts(defaultOpts, opts *ContextOpts) {
	if opts.Env == nil {
		opts.Env = defaultOpts.Env
	}
	if opts.Stdin == nil {
		opts.Stdin = defaultOpts.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = defaultOpts.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = defaultOpts.Stderr
	}
	if opts.Timer == nil {
		opts.Timer = defaultOpts.Timer
	}
}

// NewContext is the Context factory.
func NewContext(opts ContextOpts) *Context {
	initOpts(newContextOpts(), &opts)
	return &Context{opts: opts}
}

// NewContextFromEnv returns a new context instance based on the given
// cmdline environment.
func NewContextFromEnv(env *cmdline.Env) *Context {
	opts := ContextOpts{}
	initOpts(newContextOpts(), &opts)
	opts.Env = envvar.CopyMap(env.Vars)
	opts.Stdin = env.Stdin
	opts.Stdout = env.Stdout
	opts.Stderr = env.Stderr
	opts.Timer = env.Timer
	return NewContext(opts)
}

// NewDefaultContext returns a new default context.
func NewDefaultContext() *Context {
	return NewContext(ContextOpts{})
}

// Clone creates a clone of the given context, overriding select
// settings using the given options.
func (ctx Context) Clone(opts ContextOpts) *Context {
	initOpts(&ctx.opts, &opts)
	return NewContext(opts)
}

// Env returns the environment of the context.
func (ctx Context) Env() map[string]string {
	return ctx.opts.Env
}

// Stdin returns the standard input of the context.
func (ctx Context) Stdin() io.Reader {
	return ctx.opts.Stdin
}

// Stdout returns the standard output of the context.
func (ctx Context) Stdout() io.Writer {
	return ctx.opts.Stdout
}

// Stderr returns the standard error output of the context.
func (ctx Context) Stderr() io.Writer {
	return ctx.opts.Stderr
}

// Timer returns the timer associated with the context, which may be nil.
func (ctx Context) Timer() *timing.Timer {
	return ctx.opts.Timer
}

// TimerPush calls ctx.Timer().Push(name), only if the Timer is non-nil.
func (ctx Context) TimerPush(name string) {
	if ctx.opts.Timer != nil {
		ctx.opts.Timer.Push(name)
	}
}

// TimerPop calls ctx.Timer().Pop(), only if the Timer is non-nil.
func (ctx Context) TimerPop() {
	if ctx.opts.Timer != nil {
		ctx.opts.Timer.Pop()
	}
}
