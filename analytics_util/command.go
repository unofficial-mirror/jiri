// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package analytics_util

import (
	"sort"
	"strings"
	"time"
)

type Command struct {
	name      string
	flags     map[string]string
	startTime time.Time
	endTime   time.Time
}

func newCommand(name string, flags map[string]string) *Command {
	c := &Command{name: name, flags: flags}
	c.startTime = time.Now()
	return c
}

func (c *Command) AnalyticsObject() AnayticsObject {
	e := Event{}
	e.Category = "Command"
	e.Action = c.name
	if c.endTime.Second() != 0 {
		e.Label = "Complete"
		e.Value = c.endTime.Sub(c.startTime).Nanoseconds() / 1000000
	}
	value := ""
	if len(c.flags) > 0 {
		keys := []string{}
		for k, _ := range c.flags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := c.flags[k]
			value = value + k + ":" + v + ","
		}
		value = strings.TrimRight(value, ",")
	}
	e.CustomDimension = make(map[string]string)
	e.CustomDimension["flags"] = value
	return e

}

func (c *Command) Done() {
	c.endTime = time.Now()
}
