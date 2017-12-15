// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package log

import (
	"fmt"
	"time"
)

type TimeTracker struct {
	msg       string
	startTime time.Time
	logger    *Logger
	done      bool
}

func (t *TimeTracker) Done() {
	if t.done {
		return
	}
	duration := time.Since(t.startTime)
	t.done = true
	t.logger.LogTime(t.msg, duration)
}

func (l *Logger) TrackTime(format string, a ...interface{}) *TimeTracker {
	return &TimeTracker{
		logger:    l,
		startTime: time.Now(),
		msg:       fmt.Sprintf(format, a...),
		done:      false,
	}
}

func (l *Logger) LogTime(msg string, duration time.Duration) {
	if duration.Nanoseconds() >= l.timeLogThreshold.Nanoseconds() {
		l.Debugf("%.2f seconds taken for operation: %s", duration.Seconds(), msg)
	}
}
