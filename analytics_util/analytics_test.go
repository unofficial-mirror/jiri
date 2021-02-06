// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package analytics_util

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"go.fuchsia.dev/jiri/version"
)

func TestAnalyticsDisabled(t *testing.T) {
	serverMux := http.NewServeMux()
	serverMux.HandleFunc("/collect", func(rw http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not be called")
	})
	server := httptest.NewServer(serverMux)
	defer server.Close()
	analyticsUrl = server.URL + "/collect"
	as := NewAnalyticsSession(false, "UA-XXXXXX-1", "test-id")
	as.AddCommand("test", nil)
	as.SendAllAndWaitToFinish()
}

func TestSendExecutionCommandTiming(t *testing.T) {
	serverMux := http.NewServeMux()
	serverCalled := false
	expectedVals := make(map[string]string)

	serverMux.HandleFunc("/collect", func(rw http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		for k, v := range expectedVals {
			if got, ok := r.Form[k]; !ok && v != "" {
				t.Errorf("expected key %q", k)
			} else if ok {
				if len(got) != 1 {
					t.Errorf("Expected one value for key %q", k)
				}
				if got[0] != v {
					t.Errorf("%q should be %q, got %q", k, v, got[0])
				}
			}
		}
		serverCalled = true
	})
	server := httptest.NewServer(serverMux)
	defer server.Close()
	analyticsUrl = server.URL + "/collect"

	expectedVals["v"] = "1"
	expectedVals["cid"] = "test-id"
	expectedVals["tid"] = "UA-XXXXXX-1"
	expectedVals["t"] = "timing"
	expectedVals["utc"] = "Execution"
	expectedVals["utv"] = "Command"
	expectedVals["utl"] = "update"
	expectedVals["utt"] = "600000"
	as := NewAnalyticsSession(true, expectedVals["tid"], expectedVals["cid"])
	id := as.AddCommandExecutionTiming(expectedVals["utl"], time.Duration(10)*time.Minute)
	as.Done(id)
	as.SendAllAndWaitToFinish()
	if !serverCalled {
		t.Fatal("Analytics should have been sent")
	}
}

func TestSendCommand(t *testing.T) {
	serverMux := http.NewServeMux()
	serverCalled := false
	expectedVals := make(map[string]string)

	serverMux.HandleFunc("/collect", func(rw http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		for k, v := range expectedVals {
			if got, ok := r.Form[k]; !ok && v != "" {
				t.Errorf("expected key %q, but not found. Full request: %+v", k, r)
			} else if ok {
				if len(got) != 1 {
					t.Errorf("Expected one value for key %q", k)
				}
				if k == "ev" {
					want := strings.Split(v, ",")
					got = strings.Split(got[0], ", ")
					if len(got) != len(want) {
						t.Errorf("%q should be %q, got %q", k, want, got)
					}
					sort.Strings(want)
					sort.Strings(got)
					for i, w := range want {
						if got[i] != w {
							t.Errorf("%q should be %q, got %q", k, want, got)
							break
						}
					}
					return
				}
				if got[0] != v {
					t.Errorf("%q should be %q, got %q", k, v, got[0])
				}
			}
		}
		serverCalled = true
	})
	server := httptest.NewServer(serverMux)
	defer server.Close()
	analyticsUrl = server.URL + "/collect"

	expectedVals["v"] = "1"
	expectedVals["cid"] = "test-id"
	expectedVals["tid"] = "UA-XXXXXX-1"
	expectedVals["ec"] = "Command"
	expectedVals["ea"] = "action"
	expectedVals["el"] = "Complete"
	expectedVals["cd2"] = "()"
	expectedVals["an"] = "jiri"
	expectedVals["av"] = "test"
	expectedVals["cd1"] = fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
	as := NewAnalyticsSession(true, expectedVals["tid"], expectedVals["cid"])
	id := as.AddCommand(expectedVals["ea"], nil)
	as.Done(id)
	as.SendAllAndWaitToFinish()
	if !serverCalled {
		t.Fatal("Analytics should have been sent")
	}

	serverCalled = false
	expectedVals["cd2"] = "flag1:always,flag2:,flag3:3,multipart:false,v:true"
	expectedVals["el"] = ""
	expectedVals["ev"] = ""
	version.GitCommit = "test-commit"
	expectedVals["av"] = version.FormattedVersion()
	as = NewAnalyticsSession(true, expectedVals["tid"], expectedVals["cid"])
	id = as.AddCommand(expectedVals["ea"], map[string]string{"v": "true", "multipart": "false", "flag1": "always", "flag2": "not_allowed", "flag3": "3"})
	as.SendAllAndWaitToFinish()
	if !serverCalled {
		t.Fatal("Analytics should have been sent")
	}
}
