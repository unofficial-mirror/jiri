// Copyright 2016 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package jiri

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"syscall"

	"fuchsia.googlesource.com/jiri/osutil"
	"fuchsia.googlesource.com/jiri/version"
)

const (
	JiriRepository    = "https://fuchsia.googlesource.com/jiri"
	JiriStorageBucket = "https://storage.googleapis.com/fuchsia-build/jiri"
)

var (
	updateTestVersionErr  = fmt.Errorf("jiri has test version")
	updateVersionErr      = fmt.Errorf("jiri is already at latest version")
	updateNotAvailableErr = fmt.Errorf("latest version of jiri not available")
)

// Update checks whether a new version of Jiri is available and if so,
// it will download it and replace the current version with the new one.
func Update(force bool) error {
	if !force && version.GitCommit == "" {
		return updateTestVersionErr
	}
	commit, err := getCurrentCommit(JiriRepository)
	if err != nil {
		return err
	}
	if force || commit != version.GitCommit {
		// Check if the prebuilt for new version exsits.
		has, err := hasPrebuilt(JiriStorageBucket, commit)
		if err != nil {
			return fmt.Errorf("cannot check if prebuilt is available, %s", err)
		}
		if !has {
			return updateNotAvailableErr
		}

		// New version is available, download and update to it.
		b, err := downloadBinary(JiriStorageBucket, commit)
		if err != nil {
			return fmt.Errorf("cannot download latest jiri binary, %s", err)
		}
		path, err := osutil.Executable()
		if err != nil {
			return fmt.Errorf("cannot get executable path, %s", err)
		}
		return updateExecutable(path, b)
	}
	return updateVersionErr
}

func UpdateAndExecute(force bool) error {
	// Capture executable path before it is replaced in Update func
	path, err := osutil.Executable()
	if err != nil {
		return fmt.Errorf("cannot get executable path, %s", err)
	}
	if err := Update(force); err != nil {
		if err != updateNotAvailableErr && err != updateVersionErr &&
			err != updateTestVersionErr {
			return err
		} else {
			return nil
		}
	}
	// This will overwrite previous force autoupdate if present
	os.Args = append(os.Args, "-force-autoupdate=false")
	// Run the update version.
	if err = syscall.Exec(path, os.Args, os.Environ()); err != nil {
		return fmt.Errorf("cannot execute %s: %s", path, err)
	}
	return nil
}

func getCurrentCommit(repository string) (string, error) {
	u, err := url.Parse(repository)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("remote host scheme is not http(s): %s", repository)
	}
	u.Path = path.Join(u.Path, "+refs/heads/master")
	q := u.Query()
	q.Set("format", "json")
	u.RawQuery = q.Encode()

	// Use Gitiles to find out the latest revision.
	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Add("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP request failed: %v", http.StatusText(res.StatusCode))
	}

	r := bufio.NewReader(res.Body)

	// The first line of the input is the XSSI guard ")]}'".
	if _, err := r.ReadSlice('\n'); err != nil {
		return "", err
	}

	var result map[string]struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r).Decode(&result); err != nil {
		return "", err
	}
	if v, ok := result["refs/heads/master"]; ok {
		return v.Value, nil
	} else {
		return "", fmt.Errorf("cannot find current commit")
	}
}

func hasPrebuilt(bucket, version string) (bool, error) {
	url := fmt.Sprintf("%s/%s-%s/%s", bucket, runtime.GOOS, runtime.GOARCH, version)
	res, err := http.Head(url)
	if err != nil {
		return false, err
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNotFound {
		return false, fmt.Errorf("HTTP request failed: %v", http.StatusText(res.StatusCode))
	}
	return res.StatusCode == http.StatusOK, nil
}

func downloadBinary(bucket, version string) ([]byte, error) {
	url := fmt.Sprintf("%s/%s-%s/%s", bucket, runtime.GOOS, runtime.GOARCH, version)
	res, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP request failed: %v", http.StatusText(res.StatusCode))
	}
	defer res.Body.Close()

	bytes, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	return bytes, nil
}

func updateExecutable(path string, b []byte) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)

	// Write the new version to a file.
	newfile, err := ioutil.TempFile(dir, "jiri")
	if err != nil {
		return err
	}

	if _, err := newfile.Write(b); err != nil {
		return err
	}

	if err := newfile.Chmod(fi.Mode()); err != nil {
		return err
	}

	if err := newfile.Close(); err != nil {
		return err
	}

	// Backup the existing version.
	oldfile, err := ioutil.TempFile(dir, "jiri")
	if err != nil {
		return err
	}
	defer os.Remove(oldfile.Name())

	if err := oldfile.Close(); err != nil {
		return err
	}

	err = os.Rename(path, oldfile.Name())
	if err != nil {
		return err
	}

	// Replace the existing version.
	err = os.Rename(newfile.Name(), path)
	if err != nil {
		// Try to rollback the change in case of error.
		rerr := os.Rename(oldfile.Name(), path)
		if rerr != nil {
			return rerr
		}
		return err
	}

	return nil
}
