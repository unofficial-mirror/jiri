// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package analytics_util provides functions to send google analytics
package analytics_util

import (
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"sync"
	"time"

	"fuchsia.googlesource.com/jiri/version"
)

var Version = "1.0v"
var analyticsUrl = "https://www.google-analytics.com/collect"

var CollectedData = `When opted in, jiri collects the following anonymized data in order to improve the user experience:

1. Tracks the commands that user runs.
2. Tracks the flags and their values passed with the commands. It does not track values for string flags unless they are true/false.
3. Creates a uuid for each jiri repository and sends that to track the session and user workflow.
4. Tracks user's operating system and its architecture.
5. Tracks the time taken by a command to complete.
6. Tracks jiri version.`

var customDimensionMapping map[string]string

func init() {
	customDimensionMapping = make(map[string]string)
	customDimensionMapping["os"] = "cd1"
	customDimensionMapping["flags"] = "cd2"
}

type AnayticsObject interface {
	send(as *AnalyticsSession)
}

type Event struct {
	Category        string
	Action          string
	Label           string
	Value           int64
	CustomDimension map[string]string
}

type AnalyticsSession struct {
	enabled bool
	tid     string
	cid     string
	objects map[int]JiriObject
	sending map[int]bool
	nextId  int
	lock    *sync.Mutex
	slock   *sync.RWMutex
}

func (e Event) send(as *AnalyticsSession) {
	params := make(map[string]string)
	params["t"] = "event"
	params["ec"] = e.Category
	params["ea"] = e.Action
	if e.Label != "" {
		params["el"] = e.Label
	}
	if e.Value != 0 {
		params["ev"] = strconv.FormatInt(e.Value, 10)
	}
	as.sendAnalytic(params, e.CustomDimension)

}

type JiriObject interface {
	AnalyticsObject() AnayticsObject
	Done()
}

func NewAnalyticsSession(enabled bool, tid string, cid string) *AnalyticsSession {
	return &AnalyticsSession{
		enabled: enabled,
		tid:     tid,
		cid:     cid,
		objects: make(map[int]JiriObject),
		sending: make(map[int]bool),
		nextId:  0,
		lock:    &sync.Mutex{},
		slock:   &sync.RWMutex{},
	}
}

func (as *AnalyticsSession) sendAnalytic(params, cds map[string]string) {
	if !as.enabled {
		return
	}
	val := url.Values{}
	val.Add("v", "1")
	val.Add("tid", as.tid)
	val.Add("cid", as.cid)
	v := version.FormattedVersion()
	if v == "" {
		v = "test"
	}
	val.Add("av", v)
	val.Add("an", "jiri")
	val.Add(customDimensionMapping["os"], fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH))
	for k, v := range cds {
		val.Add(customDimensionMapping[k], v)
	}
	for k, v := range params {
		val.Add(k, v)
	}
	http.PostForm(analyticsUrl, val)
}

func (as *AnalyticsSession) Add(obj JiriObject) int {
	if !as.enabled {
		return -1
	}
	as.lock.Lock()
	defer as.lock.Unlock()

	as.objects[as.nextId] = obj
	as.nextId++
	return as.nextId - 1
}

func (as *AnalyticsSession) AddCommand(name string, flags map[string]string) int {
	if !as.enabled {
		return -1
	}
	return as.Add(newCommand(name, flags))
}

func (as *AnalyticsSession) Send(id int) {
	if !as.enabled {
		return
	}
	as.slock.Lock()
	defer as.slock.Unlock()
	as.sending[id] = true

	go func() {
		as.lock.Lock()
		defer as.lock.Unlock()
		defer func() {
			as.slock.Lock()
			defer as.slock.Unlock()
			delete(as.sending, id)
		}()
		if v, ok := as.objects[id]; ok {
			delete(as.objects, id)
			gaobject := v.AnalyticsObject()
			gaobject.send(as)
		}
	}()
}

func (as *AnalyticsSession) Done(id int) {
	if !as.enabled {
		return
	}
	if v, ok := as.objects[id]; ok {
		v.Done()
		as.Send(id)
	}
}

func (as *AnalyticsSession) SendAllAndWaitToFinish() {
	if !as.enabled {
		return
	}
	for k, _ := range as.objects {
		as.Send(k)
	}
	for {
		as.slock.RLock()
		l := len(as.sending)
		as.slock.RUnlock()
		if l > 0 {
			time.Sleep(100 * time.Microsecond)
		} else {
			break
		}

	}
}
