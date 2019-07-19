// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package analytics_util

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

type Command struct {
	name      string
	flags     map[string]string
	startTime time.Time
	endTime   time.Time
}

// string values allowed to be tracked
var allowedStrings = map[string]struct{}{
	"always": struct{}{},
	"auto":   struct{}{},
	"never":  struct{}{},
}

func newCommand(name string, flags map[string]string) *Command {
	for k, v := range flags {
		allowed := false
		if _, ok := allowedStrings[v]; ok {
			allowed = true
		} else if _, err := strconv.ParseBool(v); err == nil {
			allowed = true
		} else if _, err := strconv.ParseFloat(v, 10); err == nil {
			allowed = true
		} else if _, err := strconv.ParseInt(v, 10, 64); err == nil {
			allowed = true
		}
		if !allowed {
			flags[k] = ""
		}
	}
	c := &Command{name: name, flags: flags}
	c.startTime = time.Now()
	return c
}

func (c *Command) AnalyticsObject() AnayticsObject {
	e := Event{}
	e.Category = "Command"
	e.Action = c.name
	if !c.endTime.IsZero() {
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
	} else {
		// track no flag
		value = "()"
	}
	e.CustomDimension = make(map[string]string)
	e.CustomDimension["flags"] = value
	return e

}

func (c *Command) Done() {
	c.endTime = time.Now()
}
