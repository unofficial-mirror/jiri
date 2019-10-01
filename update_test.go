// Copyright 2016 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package jiri

import (
	"bytes"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestGetCurrentCommit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `)]}'
{
  "refs/heads/master": {
    "value": "68661f351339107f397749c9689334fe9893bcea"
  }
}`)
	}))
	defer ts.Close()

	c, err := getCurrentCommit(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	if want, got := "68661f351339107f397749c9689334fe9893bcea", c; want != got {
		t.Errorf("wrong commit, want: %s, got: %s\n", want, got)
	}
}

func TestHasPrebuilt(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	_, err := downloadBinary(ts.URL, "abc123")
	if err != nil {
		t.Fatal(err)
	}
}

func TestDoesNotHavePrebuilt(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	_, err := downloadBinary(ts.URL, "abc123")
	if err != updateNotAvailableErr {
		t.Fatal(err)
	}
}

func TestDownloadBinary(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		b := bytes.NewBuffer([]byte("jiri"))
		b.WriteTo(w)
	}))
	defer ts.Close()

	b, err := downloadBinary(ts.URL, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if want, got := []byte("jiri"), b; bytes.Compare(want, got) != 0 {
		t.Errorf("wrong file content, want: %s, got: %s\n", want, got)
	}
}

func TestUpdateExecutable(t *testing.T) {
	content := []byte("old")

	f, err := ioutil.TempFile("", "jiri")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(f.Name())

	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if err := updateExecutable(f.Name(), []byte("new")); err != nil {
		t.Fatal(err)
	}

	b, err := ioutil.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}

	if want, got := []byte("new"), b; bytes.Compare(want, got) != 0 {
		t.Errorf("wrong file content, want: %s, got: %s\n", want, got)
	}
}
