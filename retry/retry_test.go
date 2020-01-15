// Copyright 2020 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package retry

import (
	"testing"
	"time"
)

func TestExponentialBackOff(t *testing.T) {
	backoff := newExponentialBackoff(5, 64, 2)
	val := backoff.nextBackoff()
	if val < 5*time.Second || val > 15*time.Second {
		t.Errorf("expecting backoff between 5 to 15 secs, got %v", val)
	}
	val = backoff.nextBackoff()
	if val < 10*time.Second || val > 25*time.Second {
		t.Errorf("expecting backoff between 10 to 25 secs, got %v", val)
	}
	val = backoff.nextBackoff()
	if val < 20*time.Second || val > 35*time.Second {
		t.Errorf("expecting backoff between 20 to 35 secs, got %v", val)
	}
	val = backoff.nextBackoff()
	if val < 40*time.Second || val > 55*time.Second {
		t.Errorf("expecting backoff between 40 to 55 secs, got %v", val)
	}
	val = backoff.nextBackoff()
	if val != 64*time.Second {
		t.Errorf("expecting backoff of 64 secs, got %v", val)
	}
}
