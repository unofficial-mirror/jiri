// Copyright 2016 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package jiri

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"io"
	"io/ioutil"
	"os"
	"testing"
)

func TestGetCurrentCommit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `)]}'
{
  "log": [
    {
      "commit": "68661f351339107f397749c9689334fe9893bcea",
      "tree": "505df3f0370434ce02437e67b6d50208fa1b10b0",
      "parents": [
        "c96fe08c1ee898a19b0c4517e563a74f272a167a"
      ],
      "author": {
        "name": "John Doe",
        "email": "john.doe@example.com",
        "time": "Thu Sep 22 00:22:34 2016 -0700"
      },
      "committer": {
        "name": "John Doe",
        "email": "john.doe@example.com",
        "time": "Thu Sep 22 12:04:39 2016 -0700"
      },
      "message": "Test message"
    }
  ],
  "next": "c96fe08c1ee898a19b0c4517e563a74f272a167a"
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

	has, err := hasPrebuilt(ts.URL, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Errorf("wrong response\n")
	}
}

func TestDoesNotHavePrebuilt(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	has, err := hasPrebuilt(ts.URL, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Errorf("wrong response\n")
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
