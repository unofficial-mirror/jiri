// Copyright 2019 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gerrit

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"

	"fuchsia.googlesource.com/jiri"
)

var (
	// ErrCloneBundleNotAvailable will be returned from FetchCloneBundle
	// if clone.bundle file does not exist or cannot be fetched from remote.
	ErrCloneBundleNotAvailable = errors.New("git clone bundle not available")
)

const (
	bundleFile = "clone.bundle"
)

// FetchCloneBundle fetches git clone.bundle file from remote and save it to
// dir/clone.bundle . If if clone.bundle file does not exist or cannot be fetched
// from remote, ErrCloneBundleNotAvailable will be returned.
func FetchCloneBundle(jirix *jiri.X, remote, dir string) (string, error) {
	bundleURL, err := url.Parse(remote)
	if err != nil {
		return "", err
	}
	bundleURL.Path = path.Join(bundleURL.Path, bundleFile)
	if bundleURL.Scheme != "https" {
		// sso is not supported.
		return "", ErrCloneBundleNotAvailable
	}

	downloadPath := filepath.Join(dir, bundleFile)
	if err := fetchFileToLocal(bundleURL.String(), downloadPath); err != nil {
		jirix.Logger.Debugf("fetch clone.bundle for %q failed due to error: %v", bundleURL.String(), err)
		return "", ErrCloneBundleNotAvailable
	}
	return downloadPath, nil
}

func fetchFileToLocal(remoteURL, local string) error {
	client := &http.Client{}
	req, err := http.NewRequest("GET", remoteURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch failed due to Status code: %v", resp.StatusCode)
	}
	defer resp.Body.Close()

	tempFile, err := ioutil.TempFile("", "clone.bundle.*")
	if err != nil {
		return fmt.Errorf("create temp file failed due to error: %v", err)
	}
	defer tempFile.Close()
	defer os.Remove(tempFile.Name())

	_, err = io.Copy(tempFile, resp.Body)
	if err != nil {
		return fmt.Errorf("save remote data to local failed due to error: %v", err)
	}
	tempFile.Close()
	if err := os.Rename(tempFile.Name(), local); err != nil {
		return fmt.Errorf("failed to move %q to %q due to error: %v", tempFile.Name(), local, err)
	}
	return nil
}
