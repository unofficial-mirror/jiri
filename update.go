// Copyright 2016 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package jiri

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"fuchsia.googlesource.com/jiri/osutil"
	"fuchsia.googlesource.com/jiri/version"
)

const (
	JiriRepository   = "https://fuchsia.googlesource.com/jiri"
	JiriCIPDEndPoint = "https://chrome-infra-packages.appspot.com/dl/fuchsia/tools/jiri"
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
		// CIPD HTTP endpoint does not allow HTTP HEAD.
		// Download the Jiri archive directly.
		b, err := downloadBinary(JiriCIPDEndPoint, commit)
		if err != nil {
			return fmt.Errorf("cannot download latest jiri binary, %s", err)
		}
		unarchivedBinary, err := unarchiveJiri(b)
		if err != nil {
			return err
		}
		path, err := osutil.Executable()
		if err != nil {
			return fmt.Errorf("cannot get executable path, %s", err)
		}
		return updateExecutable(path, unarchivedBinary)
	}
	return updateVersionErr
}

func unarchiveJiri(b []byte) ([]byte, error) {
	zipReader, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, fmt.Errorf("Failed to read jiri archive: %v", err)
	}
	for _, file := range zipReader.File {
		if file.Name == "jiri" {
			fileReader, err := file.Open()
			defer fileReader.Close()
			if err != nil {
				return nil, fmt.Errorf("Failed to read jiri archive: %v", err)
			}
			return ioutil.ReadAll(fileReader)
		}
	}
	return nil, fmt.Errorf("Cannot find jiri in update archive")
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

	args := []string{}
	for _, a := range os.Args {
		if !strings.HasPrefix(a, "-force-autoupdate") {
			args = append(args, a)
		}
	}

	// Run the update version.
	if err = syscall.Exec(path, args, os.Environ()); err != nil {
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

func downloadBinary(endpoint, version string) ([]byte, error) {
	os := runtime.GOOS
	if os == "darwin" {
		os = "mac"
	}
	url := fmt.Sprintf("%s/%s-%s/+/git_revision:%s", endpoint, os, runtime.GOARCH, version)
	res, err := http.Get(url)
	if err != nil {
		return nil, err
	}

	if res.StatusCode == http.StatusNotFound {
		return nil, updateNotAvailableErr
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

	err = osutil.Rename(path, oldfile.Name())
	if err != nil {
		return err
	}

	// Replace the existing version.
	err = osutil.Rename(newfile.Name(), path)
	if err != nil {
		// Try to rollback the change in case of error.
		rerr := osutil.Rename(oldfile.Name(), path)
		if rerr != nil {
			return rerr
		}
		return err
	}

	return nil
}
