// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package retry provides a facility for retrying function
// invocations.
package retry

import (
	"fmt"
	"time"

	"fuchsia.googlesource.com/jiri"
)

type RetryOpt interface {
	retryOpt()
}

type AttemptsOpt int

func (a AttemptsOpt) retryOpt() {}

type IntervalOpt time.Duration

func (i IntervalOpt) retryOpt() {}

const (
	defaultAttempts = 3
	defaultInterval = 5 * time.Second
)

// Function retries the given function for the given number of
// attempts at the given interval.
func Function(jirix *jiri.X, fn func() error, task string, opts ...RetryOpt) error {
	attempts, interval := defaultAttempts, defaultInterval
	for _, opt := range opts {
		switch typedOpt := opt.(type) {
		case AttemptsOpt:
			attempts = int(typedOpt)
		case IntervalOpt:
			interval = time.Duration(typedOpt)
		}
	}

	var err error
	for i := 1; i <= attempts; i++ {
		if i > 1 {
			jirix.Logger.Infof("Attempt %d/%d: %s\n\n", i, attempts, task)
		}
		if err = fn(); err == nil {
			return nil
		}
		if i < attempts {
			jirix.Logger.Errorf("%s\n\n", err)
			jirix.Logger.Infof("Wait for %s before next attempt...: %s\n\n", interval, task)
			time.Sleep(interval)
		}
	}
	if attempts > 1 {
		return fmt.Errorf("%q failed %d times in a row, Last error: %s", task, attempts, err)
	}
	return err
}
