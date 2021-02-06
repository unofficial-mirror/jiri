// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package log

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.fuchsia.dev/jiri/color"
)

const commonPrefix = "seconds taken for operation:"
const timeThreshold = 100 * time.Millisecond

// Creates time trackers for passed operations, sleeps for |sleeptime|
// duration and returns the logger output.
// It creates logger using |loglevel| and |threshold| params.
func runTimeTracker(loglevel LogLevel, threshold, sleeptime time.Duration, operations []string) *bytes.Buffer {
	buf := bytes.NewBufferString("")
	logger := NewLogger(loglevel, color.NewColor(color.ColorNever), false, 0, threshold, buf, nil)
	var tts []*TimeTracker
	for _, op := range operations {
		tts = append(tts, logger.TrackTime(op))
	}
	time.Sleep(sleeptime)
	for _, tt := range tts {
		tt.Done()
	}
	return buf
}

// Tests time tracker and checks if it is logging the debug message.
func TestTimeTrackerBasic(t *testing.T) {
	t.Parallel()
	buf := runTimeTracker(DebugLevel, timeThreshold, timeThreshold, []string{"new operation"})
	if !strings.Contains(buf.String(), fmt.Sprintf("%s new operation", commonPrefix)) {
		t.Fatalf("logger should have logged timing for this operation")
	}
}

// Tests that logger does not log time if the loglevel is more than DebugLevel.
func TestTimeTrackerLogLevel(t *testing.T) {
	t.Parallel()
	buf := runTimeTracker(InfoLevel, timeThreshold, timeThreshold, []string{"new operation"})
	if len(buf.String()) != 0 {
		t.Fatalf("Did not expect logging, got: %s", buf.String())
	}
}

// Tests that code works fine with time trackers on more than one operation.
func TestMultiTimeTracker(t *testing.T) {
	t.Parallel()
	buf := runTimeTracker(DebugLevel, timeThreshold, timeThreshold, []string{"operation 1", "operation 2"})
	if !strings.Contains(buf.String(), fmt.Sprintf("%s operation 1", commonPrefix)) {
		t.Fatalf("logger should have logged timing for operation 1")
	}
	if !strings.Contains(buf.String(), fmt.Sprintf("%s operation 2", commonPrefix)) {
		t.Fatalf("logger should have logged timing for operation 2")
	}
}

// Tests that logger only logs time when more than threshold.
func TestTimeTrackerThreshold(t *testing.T) {
	t.Parallel()
	buf := runTimeTracker(DebugLevel, timeThreshold, timeThreshold/2, []string{"operation 1"})
	if len(buf.String()) != 0 {
		t.Fatalf("Did not expect logging, got: %s", buf.String())
	}
}
