// Copyright 2018 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package analytics_util

import (
	"time"
)

// Tracks the timing between command execution.
type CommandExecutionTiming struct {
	name   string
	timing time.Duration
}

func newCommandExecutionTiming(name string, timing time.Duration) *CommandExecutionTiming {
	return &CommandExecutionTiming{name: name, timing: timing}
}

func (c *CommandExecutionTiming) AnalyticsObject() AnayticsObject {
	ut := UserTiming{}
	ut.Category = "Execution"
	ut.Variable = "Command"
	ut.Label = c.name
	ut.Timing = int64(c.timing.Seconds() * 1000) // send ms
	return ut
}

func (c *CommandExecutionTiming) Done() {}
