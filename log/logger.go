// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package log

import (
	"container/list"
	"fmt"
	"io"
	glog "log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"fuchsia.googlesource.com/jiri/color"
)

// Logger provides for convenient logging in jiri. It supports logger
// level using global flags. To use it "InitializeGlobalLogger" needs to
// be called once, then GetLogger function can be used to get the logger or
// log functions can be called directly
//
// The default logging level is Info. It uses golang logger to log messages internally.
// As an example to use debug logger one needs to run
// log.GetLogger().Debugf(....)
// or
// log.Debugf(....)
// By default Error logger prints to os.Stderr and others print to os.Stdout.
// Capture function can be used to temporarily capture the logs.

type TaskData struct {
	msg      string
	progress int
}

type Task struct {
	taskData *TaskData
	e        *list.Element
	l        *Logger
}

type Logger struct {
	lock                 *sync.Mutex
	LoggerLevel          LogLevel
	goLogger             *glog.Logger
	goErrorLogger        *glog.Logger
	color                color.Color
	progressLines        int
	progressWindowSize   uint
	enableProgress       uint32
	progressUpdateNeeded bool
	tasks                *list.List
}

type LogLevel int

const (
	NoLogLevel LogLevel = iota
	ErrorLevel
	WarningLevel
	InfoLevel
	DebugLevel
	TraceLevel
)

func NewLogger(loggerLevel LogLevel, color color.Color, enableProgress bool, progressWindowSize uint, outWriter, errWriter io.Writer) *Logger {
	if outWriter == nil {
		outWriter = os.Stdout
	}
	if errWriter == nil {
		errWriter = os.Stderr
	}

	term := os.Getenv("TERM")
	switch term {
	case "dumb", "":
		enableProgress = false
	}

	l := &Logger{
		LoggerLevel:          loggerLevel,
		lock:                 &sync.Mutex{},
		goLogger:             glog.New(outWriter, "", 0),
		goErrorLogger:        glog.New(errWriter, "", 0),
		color:                color,
		progressLines:        0,
		enableProgress:       0,
		progressWindowSize:   progressWindowSize,
		progressUpdateNeeded: false,
		tasks:                list.New(),
	}
	if enableProgress {
		l.enableProgress = 1
	}
	go func() {
		for l.IsProgressEnabled() {
			l.repaintProgressMsgs()
			time.Sleep(time.Second / 30)
		}
	}()
	return l
}

func (l *Logger) IsProgressEnabled() bool {
	return atomic.LoadUint32(&l.enableProgress) == 1
}

func (l *Logger) DisableProgress() {
	l.lock.Lock()
	defer l.lock.Unlock()
	l.clearProgress()
	atomic.StoreUint32(&l.enableProgress, 0)
}

func (l *Logger) AddTaskMsg(format string, a ...interface{}) Task {
	if !l.IsProgressEnabled() {
		return Task{taskData: &TaskData{}, l: l}
	}
	t := &TaskData{
		msg:      fmt.Sprintf(format, a...),
		progress: 0,
	}
	l.lock.Lock()
	defer l.lock.Unlock()
	e := l.tasks.PushBack(t)
	l.progressUpdateNeeded = true
	return Task{
		taskData: t,
		e:        e,
		l:        l,
	}
}

func (t *Task) Done() {
	t.taskData.progress = 100
	if !t.l.IsProgressEnabled() {
		return
	}
	t.l.lock.Lock()
	defer t.l.lock.Unlock()
	t.l.progressUpdateNeeded = true
}

func (l *Logger) repaintProgressMsgs() {
	l.lock.Lock()
	defer l.lock.Unlock()
	if !l.IsProgressEnabled() || !l.progressUpdateNeeded {
		return
	}
	l.clearProgress()
	e := l.tasks.Front()
	for i := 0; i < int(l.progressWindowSize); i++ {
		if e == nil {
			break
		}
		t := e.Value.(*TaskData)
		if t.progress < 100 {
			l.printProgressMsg(t.msg)
			e = e.Next()
		} else {
			temp := e.Next()
			l.tasks.Remove(e)
			e = temp
			i--
		}
	}
	l.progressUpdateNeeded = false
}

// This is thread unsafe
func (l *Logger) printProgressMsg(msg string) {
	// Disable wrap and print progress
	str := fmt.Sprintf("\033[?7l%s: %s\033[?7h\n", l.color.Green("PROGRESS"), msg)
	fmt.Printf(str)
	l.progressLines++
}

// This is thread unsafe
func (l *Logger) clearProgress() {
	if !l.IsProgressEnabled() || l.progressLines == 0 {
		return
	}
	buf := ""
	for i := 0; i < l.progressLines; i++ {
		buf = buf + "\033[1A\033[2K\r"
	}
	fmt.Printf(buf)
	l.progressLines = 0
}

func (l *Logger) log(prefix, format string, a ...interface{}) {
	l.lock.Lock()
	defer l.lock.Unlock()
	l.clearProgress()
	l.goLogger.Printf("%s%s", prefix, fmt.Sprintf(format, a...))
}

func (l *Logger) Infof(format string, a ...interface{}) {
	if l.LoggerLevel >= InfoLevel {
		l.log("", format, a...)
	}
}

func (l *Logger) Debugf(format string, a ...interface{}) {
	if l.LoggerLevel >= DebugLevel {
		l.log(l.color.Cyan("DEBUG: "), format, a...)
	}
}

func (l *Logger) Tracef(format string, a ...interface{}) {
	if l.LoggerLevel >= TraceLevel {
		l.log(l.color.Blue("TRACE: "), format, a...)
	}
}

func (l *Logger) Warningf(format string, a ...interface{}) {
	if l.LoggerLevel >= WarningLevel {
		l.log(l.color.Yellow("WARN: "), format, a...)
	}
}

func (l *Logger) Errorf(format string, a ...interface{}) {
	if l.LoggerLevel >= ErrorLevel {
		l.lock.Lock()
		defer l.lock.Unlock()
		l.clearProgress()
		l.goErrorLogger.Printf("%s%s", l.color.Red("ERROR: "), fmt.Sprintf(format, a...))
	}
}
