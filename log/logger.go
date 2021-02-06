// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package log

import (
	"bytes"
	"container/list"
	"fmt"
	"io"
	"io/ioutil"
	glog "log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"go.fuchsia.dev/jiri/color"
	"go.fuchsia.dev/jiri/isatty"
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
	goBufferLogger       *glog.Logger
	color                color.Color
	progressLines        int
	progressWindowSize   uint
	enableProgress       uint32
	progressUpdateNeeded bool
	timeLogThreshold     time.Duration
	tasks                *list.List
	logBuffer            *bytes.Buffer
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

func NewLogger(loggerLevel LogLevel, color color.Color, enableProgress bool, progressWindowSize uint, timeLogThreshold time.Duration, outWriter, errWriter io.Writer) *Logger {
	var logBuffer bytes.Buffer
	if outWriter == nil {
		outWriter = os.Stdout
	}
	if errWriter == nil {
		errWriter = os.Stderr
	}
	outWriter = io.MultiWriter(outWriter, &logBuffer)
	errWriter = io.MultiWriter(errWriter, &logBuffer)
	term := os.Getenv("TERM")
	switch term {
	case "dumb", "":
		enableProgress = false
	}
	if enableProgress {
		enableProgress = isatty.IsTerminal()
	}
	l := &Logger{
		LoggerLevel:          loggerLevel,
		lock:                 &sync.Mutex{},
		goLogger:             glog.New(outWriter, "", 0),
		goErrorLogger:        glog.New(errWriter, "", 0),
		goBufferLogger:       glog.New(&logBuffer, "", 0),
		color:                color,
		progressLines:        0,
		enableProgress:       0,
		progressWindowSize:   progressWindowSize,
		progressUpdateNeeded: false,
		timeLogThreshold:     timeLogThreshold,
		tasks:                list.New(),
		logBuffer:            &logBuffer,
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

func (l *Logger) TimeLogThreshold() time.Duration {
	return l.timeLogThreshold
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
	stamp := time.Now().Format("15:04:05.000")
	defer l.lock.Unlock()
	l.clearProgress()
	l.goLogger.Printf("[%s] %s%s", stamp, prefix, fmt.Sprintf(format, a...))
}

func (l *Logger) logToBufferOnly(prefix, format string, a ...interface{}) {
	l.lock.Lock()
	stamp := time.Now().Format("15:04:05.000")
	defer l.lock.Unlock()
	l.goBufferLogger.Printf("[%s] %s%s", stamp, prefix, fmt.Sprintf(format, a...))
}

func (l *Logger) Logf(loglevel LogLevel, format string, a ...interface{}) {
	switch loglevel {
	case InfoLevel:
		l.Infof(format, a...)
	case DebugLevel:
		l.Debugf(format, a...)
	case TraceLevel:
		l.Tracef(format, a...)
	case WarningLevel:
		l.Warningf(format, a...)
	case ErrorLevel:
		l.Errorf(format, a...)
	default:
		panic(fmt.Sprintf("Undefined loglevel: %v, log message: %s", loglevel, fmt.Sprintf(format, a...)))
	}
}

func (l *Logger) Infof(format string, a ...interface{}) {
	if l.LoggerLevel >= InfoLevel {
		l.log("", format, a...)
	} else {
		l.logToBufferOnly("", format, a...)
	}
}

func (l *Logger) Debugf(format string, a ...interface{}) {
	if l.LoggerLevel >= DebugLevel {
		l.log(l.color.Cyan("DEBUG: "), format, a...)
	} else {
		l.logToBufferOnly(l.color.Cyan("DEBUG: "), format, a...)
	}
}

func (l *Logger) Tracef(format string, a ...interface{}) {
	if l.LoggerLevel >= TraceLevel {
		l.log(l.color.Blue("TRACE: "), format, a...)
	} else {
		l.logToBufferOnly(l.color.Blue("TRACE: "), format, a...)
	}
}

func (l *Logger) Warningf(format string, a ...interface{}) {
	if l.LoggerLevel >= WarningLevel {
		l.log(l.color.Yellow("WARN: "), format, a...)
	} else {
		l.logToBufferOnly(l.color.Yellow("WARN: "), format, a...)
	}
}

func (l *Logger) Errorf(format string, a ...interface{}) {
	if l.LoggerLevel >= ErrorLevel {
		l.lock.Lock()
		defer l.lock.Unlock()
		l.clearProgress()
		l.goErrorLogger.Printf("%s%s", l.color.Red("ERROR: "), fmt.Sprintf(format, a...))
	} else {
		l.logToBufferOnly(l.color.Red("ERROR: "), format, a...)
	}
}

// WriteLogToFile writes current logs into file.
func (l *Logger) WriteLogToFile(filename string) error {
	return ioutil.WriteFile(filename, l.logBuffer.Bytes(), 0644)
}

// GetLogBuffer returns current buffer for the logger.
func (l *Logger) GetLogBuffer() *bytes.Buffer {
	return l.logBuffer
}
