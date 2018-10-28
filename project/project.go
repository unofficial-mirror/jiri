// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package project

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/gitutil"
	"fuchsia.googlesource.com/jiri/log"
	"fuchsia.googlesource.com/jiri/retry"
)

var (
	JiriProject = "release.go.jiri"
	JiriName    = "jiri"
	JiriPackage = "fuchsia.googlesource.com/jiri"
	ssoRe       = regexp.MustCompile("^sso://(.*?)/")
)

var (
	// time in minutes
	DefaultHookTimeout = uint(5)
)

// Project represents a jiri project.
type Project struct {
	// Name is the project name.
	Name string `xml:"name,attr,omitempty"`
	// Path is the path used to store the project locally. Project
	// manifest uses paths that are relative to the root directory.
	// When a manifest is parsed (e.g. in RemoteProjects), the program
	// logic converts the relative paths to an absolute paths, using
	// the current root as a prefix.
	Path string `xml:"path,attr,omitempty"`
	// Remote is the project remote.
	Remote string `xml:"remote,attr,omitempty"`
	// RemoteBranch is the name of the remote branch to track.
	RemoteBranch string `xml:"remotebranch,attr,omitempty"`
	// Revision is the revision the project should be advanced to during "jiri
	// update".  If Revision is set, RemoteBranch will be ignored.  If Revision
	// is not set, "HEAD" is used as the default.
	Revision string `xml:"revision,attr,omitempty"`
	// HistoryDepth is the depth flag passed to git clone and git fetch
	// commands. It is used to limit downloading large histories for large
	// projects.
	HistoryDepth int `xml:"historydepth,attr,omitempty"`
	// GerritHost is the gerrit host where project CLs will be sent.
	GerritHost string `xml:"gerrithost,attr,omitempty"`
	// GitHooks is a directory containing git hooks that will be installed for
	// this project.
	GitHooks string `xml:"githooks,attr,omitempty"`

	XMLName struct{} `xml:"project"`

	// This is used to store computed key. This is useful when remote and
	// local projects are same but have different name or remote
	ComputedKey ProjectKey `xml:"-"`

	// This stores the local configuration file for the project
	LocalConfig LocalConfig `xml:"-"`
}

// ProjectsByPath implements the Sort interface. It sorts Projects by
// the Path field.
type ProjectsByPath []Project

func (projects ProjectsByPath) Len() int {
	return len(projects)
}
func (projects ProjectsByPath) Swap(i, j int) {
	projects[i], projects[j] = projects[j], projects[i]
}
func (projects ProjectsByPath) Less(i, j int) bool {
	return projects[i].Path+string(filepath.Separator) < projects[j].Path+string(filepath.Separator)
}

// ProjectKey is a unique string for a project.
type ProjectKey string

// MakeProjectKey returns the project key, given the project name and remote.
func MakeProjectKey(name, remote string) ProjectKey {
	return ProjectKey(name + KeySeparator + remote)
}

// KeySeparator is a reserved string used in ProjectKeys and HookKeys.
// It cannot occur in Project or Hook names.
const KeySeparator = "="

// ProjectKeys is a slice of ProjectKeys implementing the Sort interface.
type ProjectKeys []ProjectKey

func (pks ProjectKeys) Len() int           { return len(pks) }
func (pks ProjectKeys) Less(i, j int) bool { return string(pks[i]) < string(pks[j]) }
func (pks ProjectKeys) Swap(i, j int)      { pks[i], pks[j] = pks[j], pks[i] }

// ProjectFromFile returns a project parsed from the contents of filename,
// with defaults filled in and all paths absolute.
func ProjectFromFile(jirix *jiri.X, filename string) (*Project, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, fmtError(err)
	}

	p := new(Project)
	if err := xml.Unmarshal(data, p); err != nil {
		return nil, err
	}
	if err := p.fillDefaults(); err != nil {
		return nil, err
	}
	p.absolutizePaths(jirix.Root)
	return p, nil
}

// ToFile writes the project p to a file with the given filename, with defaults
// unfilled and all paths relative to the jiri root.
func (p Project) ToFile(jirix *jiri.X, filename string) error {
	if err := p.unfillDefaults(); err != nil {
		return err
	}
	// Replace absolute paths with relative paths to make it possible to move
	// the root directory locally.
	if err := p.relativizePaths(jirix.Root); err != nil {
		return err
	}
	data, err := xml.Marshal(p)
	if err != nil {
		return fmt.Errorf("project xml.Marshal failed: %v", err)
	}
	// Same logic as Manifest.ToBytes, to make the output more compact.
	data = bytes.Replace(data, endProjectSoloBytes, endElemSoloBytes, -1)
	if !bytes.HasSuffix(data, newlineBytes) {
		data = append(data, '\n')
	}
	return safeWriteFile(jirix, filename, data)
}

// absolutizePaths makes all relative paths absolute by prepending basepath.
func (p *Project) absolutizePaths(basepath string) {
	if p.Path != "" && !filepath.IsAbs(p.Path) {
		p.Path = filepath.Join(basepath, p.Path)
	}
	if p.GitHooks != "" && !filepath.IsAbs(p.GitHooks) {
		p.GitHooks = filepath.Join(basepath, p.GitHooks)
	}
}

// relativizePaths makes all absolute paths relative to basepath.
func (p *Project) relativizePaths(basepath string) error {
	if filepath.IsAbs(p.Path) {
		relPath, err := filepath.Rel(basepath, p.Path)
		if err != nil {
			return err
		}
		p.Path = relPath
	}
	if filepath.IsAbs(p.GitHooks) {
		relGitHooks, err := filepath.Rel(basepath, p.GitHooks)
		if err != nil {
			return err
		}
		p.GitHooks = relGitHooks
	}
	return nil
}

// Key returns the unique ProjectKey for the project.
func (p Project) Key() ProjectKey {
	if p.ComputedKey == "" {
		p.ComputedKey = MakeProjectKey(p.Name, p.Remote)
	}
	return p.ComputedKey
}

func (p *Project) fillDefaults() error {
	if p.RemoteBranch == "" {
		p.RemoteBranch = "master"
	}
	if p.Revision == "" {
		p.Revision = "HEAD"
	}
	return p.validate()
}

func (p *Project) unfillDefaults() error {
	if p.RemoteBranch == "master" {
		p.RemoteBranch = ""
	}
	if p.Revision == "HEAD" {
		p.Revision = ""
	}
	return p.validate()
}

func (p *Project) validate() error {
	if strings.Contains(p.Name, KeySeparator) {
		return fmt.Errorf("bad project: name cannot contain %q: %+v", KeySeparator, *p)
	}
	return nil
}

func (p *Project) update(other *Project) {
	if other.Path != "" {
		p.Path = other.Path
	}
	if other.RemoteBranch != "" {
		p.RemoteBranch = other.RemoteBranch
	}
	if other.Revision != "" {
		p.Revision = other.Revision
	}
	if other.HistoryDepth != 0 {
		p.HistoryDepth = other.HistoryDepth
	}
	if other.GerritHost != "" {
		p.GerritHost = other.GerritHost
	}
	if other.GitHooks != "" {
		p.GitHooks = other.GitHooks
	}
}

func cacheDirPathFromRemote(cacheRoot, remote string) (string, error) {
	if cacheRoot != "" {
		url, err := url.Parse(remote)
		if err != nil {
			return "", err
		}
		dirname := url.Host + strings.Replace(strings.Replace(url.Path, "-", "--", -1), "/", "-", -1)
		referenceDir := filepath.Join(cacheRoot, dirname)
		return referenceDir, nil
	}
	return "", nil
}

// CacheDirPath returns a generated path to a directory that can be used as a reference repo
// for the given project.
func (p *Project) CacheDirPath(jirix *jiri.X) (string, error) {
	return cacheDirPathFromRemote(jirix.Cache, p.Remote)

}

func (p *Project) writeJiriRevisionFiles(jirix *jiri.X) error {
	scm := gitutil.New(jirix, gitutil.RootDirOpt(p.Path))
	file := filepath.Join(p.Path, ".git", "JIRI_HEAD")
	head := "refs/remotes/origin/master"
	var err error
	if p.Revision != "" && p.Revision != "HEAD" {
		head = p.Revision
	} else if p.RemoteBranch != "" {
		head = "refs/remotes/origin/" + p.RemoteBranch
	}
	head, err = scm.CurrentRevisionForRef(head)
	if err != nil {
		return fmt.Errorf("Cannot find revision for ref %q for project %s(%s): %s", head, p.Name, p.Path, err)
	}
	if err := safeWriteFile(jirix, file, []byte(head)); err != nil {
		return err
	}
	file = filepath.Join(p.Path, ".git", "JIRI_LAST_BASE")
	if rev, err := scm.CurrentRevision(); err != nil {
		return fmt.Errorf("Cannot find current revision for for project %s(%s): %s", p.Name, p.Path, err)
	} else {
		return safeWriteFile(jirix, file, []byte(rev))
	}
}

func (p *Project) IsOnJiriHead(jirix *jiri.X) (bool, error) {
	scm := gitutil.New(jirix, gitutil.RootDirOpt(p.Path))
	jiriHead := "refs/remotes/origin/master"
	var err error
	if p.Revision != "" && p.Revision != "HEAD" {
		jiriHead = p.Revision
	} else if p.RemoteBranch != "" {
		jiriHead = "refs/remotes/origin/" + p.RemoteBranch
	}
	jiriHead, err = scm.CurrentRevisionForRef(jiriHead)
	if err != nil {
		return false, fmt.Errorf("Cannot find revision for ref %q for project %s(%s): %s", jiriHead, p.Name, p.Path, err)
	}
	head, err := scm.CurrentRevision()
	if err != nil {
		return false, fmt.Errorf("Cannot find current revision  for project %s(%s): %s", p.Name, p.Path, err)
	}
	return head == jiriHead, nil
}

// Projects maps ProjectKeys to Projects.
type Projects map[ProjectKey]Project

// toSlice returns a slice of Projects in the Projects map.
func (ps Projects) toSlice() []Project {
	var pSlice []Project
	for _, p := range ps {
		pSlice = append(pSlice, p)
	}
	return pSlice
}

// Find returns all projects in Projects with the given key or name.
func (ps Projects) Find(keyOrName string) Projects {
	projects := Projects{}
	if p, ok := ps[ProjectKey(keyOrName)]; ok {
		projects[ProjectKey(keyOrName)] = p
	} else {
		for key, p := range ps {
			if keyOrName == p.Name {
				projects[key] = p
			}
		}
	}
	return projects
}

// FindUnique returns the project in Projects with the given key or name, and
// returns an error if none or multiple matching projects are found.
func (ps Projects) FindUnique(keyOrName string) (Project, error) {
	var p Project
	projects := ps.Find(keyOrName)
	if len(projects) == 0 {
		return p, fmt.Errorf("no projects found with key or name %q", keyOrName)
	}
	if len(projects) > 1 {
		return p, fmt.Errorf("multiple projects found with name %q", keyOrName)
	}
	// Return the only project in projects.
	for _, project := range projects {
		p = project
	}
	return p, nil
}

// ScanMode determines whether LocalProjects should scan the local filesystem
// for projects (FullScan), or optimistically assume that the local projects
// will match those in the manifest (FastScan).
type ScanMode bool

const (
	FastScan = ScanMode(false)
	FullScan = ScanMode(true)
)

func (sm ScanMode) String() string {
	if sm == FastScan {
		return "FastScan"
	} else {
		return "FullScan"
	}
}

// CreateSnapshot creates a manifest that encodes the current state of
// HEAD of all projects and writes this snapshot out to the given file.
// if hooks are not passed, jiri will read JiriManifestFile and get hooks from there,
// so always pass hooks incase updating from a snapshot
func CreateSnapshot(jirix *jiri.X, file string, hooks Hooks, localManifest bool) error {
	jirix.TimerPush("create snapshot")
	defer jirix.TimerPop()

	manifest := Manifest{}

	// Add all local projects to manifest.
	localProjects, err := LocalProjects(jirix, FullScan)
	if err != nil {
		return err
	}

	for _, project := range localProjects {
		manifest.Projects = append(manifest.Projects, project)
	}

	if hooks == nil {
		if _, hooks, err = LoadManifestFile(jirix, jirix.JiriManifestFile(), localProjects, localManifest); err != nil {
			return err
		}
	}
	for _, hook := range hooks {
		manifest.Hooks = append(manifest.Hooks, hook)
	}

	return manifest.ToFile(jirix, file)
}

// CheckoutSnapshot updates project state to the state specified in the given
// snapshot file.  Note that the snapshot file must not contain remote imports.
func CheckoutSnapshot(jirix *jiri.X, snapshot string, gc, runHooks bool, runHookTimeout uint) error {
	// Find all local projects.
	scanMode := FastScan
	if gc {
		scanMode = FullScan
	}
	localProjects, err := LocalProjects(jirix, scanMode)
	if err != nil {
		return err
	}
	remoteProjects, hooks, err := LoadSnapshotFile(jirix, snapshot)
	if err != nil {
		return err
	}
	if err := updateProjects(jirix, localProjects, remoteProjects, hooks, gc, runHookTimeout, false /*rebaseTracked*/, false /*rebaseUntracked*/, false /*rebaseAll*/, true /*snapshot*/, runHooks); err != nil {
		return err
	}
	return WriteUpdateHistorySnapshot(jirix, snapshot, hooks, false)
}

// LoadSnapshotFile loads the specified snapshot manifest.  If the snapshot
// manifest contains a remote import, an error will be returned.
func LoadSnapshotFile(jirix *jiri.X, snapshot string) (Projects, Hooks, error) {
	if _, err := os.Stat(snapshot); err != nil {
		if !os.IsNotExist(err) {
			return nil, nil, fmtError(err)
		}
		u, err := url.ParseRequestURI(snapshot)
		if err != nil {
			return nil, nil, fmt.Errorf("%q is neither a URL nor a valid file path", snapshot)
		}
		jirix.Logger.Infof("Getting snapshot from URL %q", u)
		resp, err := http.Get(u.String())
		if err != nil {
			return nil, nil, fmt.Errorf("Error getting snapshot from URL %q: %v", u, err)
		}
		defer resp.Body.Close()
		tmpFile, err := ioutil.TempFile("", "snapshot")
		if err != nil {
			return nil, nil, fmt.Errorf("Error creating tmp file: %v", err)
		}
		snapshot = tmpFile.Name()
		defer os.Remove(snapshot)
		if _, err = io.Copy(tmpFile, resp.Body); err != nil {
			return nil, nil, fmt.Errorf("Error writing to tmp file: %v", err)
		}

	}
	return LoadManifestFile(jirix, snapshot, nil, false)
}

// CurrentProject gets the current project from the current directory by
// reading the jiri project metadata located in a directory at the root of the
// current repository.
func CurrentProject(jirix *jiri.X) (*Project, error) {
	topLevel, err := gitutil.New(jirix).TopLevel()
	if err != nil {
		return nil, nil
	}
	metadataDir := filepath.Join(topLevel, jiri.ProjectMetaDir)
	if _, err := os.Stat(metadataDir); err == nil {
		project, err := ProjectFromFile(jirix, filepath.Join(metadataDir, jiri.ProjectMetaFile))
		if err != nil {
			return nil, err
		}
		return project, nil
	}
	return nil, nil
}

// setProjectRevisions sets the current project revision for
// each project as found on the filesystem
func setProjectRevisions(jirix *jiri.X, projects Projects) (Projects, error) {
	jirix.TimerPush("set revisions")
	defer jirix.TimerPop()
	for name, project := range projects {
		scm := gitutil.New(jirix, gitutil.RootDirOpt(project.Path))
		revision, err := scm.CurrentRevision()
		if err != nil {
			return nil, fmt.Errorf("Can't get revision for project %q: %v", project.Name, err)
		}
		project.Revision = revision
		projects[name] = project
	}
	return projects, nil
}

func rewriteRemote(jirix *jiri.X, remote string) string {
	if !jirix.RewriteSsoToHttps {
		return remote
	}
	if strings.HasPrefix(remote, "sso://") {
		return ssoRe.ReplaceAllString(remote, "https://$1.googlesource.com/")
	}
	return remote
}

// LocalProjects returns projects on the local filesystem.  If all projects in
// the manifest exist locally and scanMode is set to FastScan, then only the
// projects in the manifest that exist locally will be returned.  Otherwise, a
// full scan of the filesystem will take place, and all found projects will be
// returned.
func LocalProjects(jirix *jiri.X, scanMode ScanMode) (Projects, error) {
	jirix.TimerPush("local projects")
	defer jirix.TimerPop()

	latestSnapshot := jirix.UpdateHistoryLatestLink()
	latestSnapshotExists, err := isFile(latestSnapshot)
	if err != nil {
		return nil, err
	}
	if scanMode == FastScan && latestSnapshotExists {
		// Fast path: Full scan was not requested, and we have a snapshot containing
		// the latest update.  Check that the projects listed in the snapshot exist
		// locally.  If not, then fall back on the slow path.
		//
		// An error will be returned if the snapshot contains remote imports, since
		// that would cause an infinite loop; we'd need local projects, in order to
		// load the snapshot, in order to determine the local projects.
		snapshotProjects, _, err := LoadSnapshotFile(jirix, latestSnapshot)
		if err != nil {
			return nil, err
		}
		projectsExist, err := projectsExistLocally(jirix, snapshotProjects)
		if err != nil {
			return nil, err
		}
		if projectsExist {
			for key, p := range snapshotProjects {
				localConfigFile := filepath.Join(p.Path, jiri.ProjectMetaDir, jiri.ProjectConfigFile)
				if p.LocalConfig, err = LocalConfigFromFile(jirix, localConfigFile); err != nil {
					return nil, fmt.Errorf("Error while reading config for project %s(%s): %s", p.Name, p.Path, err)
				}
				snapshotProjects[key] = p
			}
			return setProjectRevisions(jirix, snapshotProjects)
		}
	}

	// Slow path: Either full scan was requested, or projects exist in manifest
	// that were not found locally.  Do a recursive scan of all projects under
	// the root.
	projects := Projects{}
	jirix.TimerPush("scan fs")
	multiErr := findLocalProjects(jirix, jirix.Root, projects)
	jirix.TimerPop()
	if multiErr != nil {
		return nil, multiErr
	}
	return setProjectRevisions(jirix, projects)
}

// projectsExistLocally returns true iff all the given projects exist on the
// local filesystem.
// Note that this may return true even if there are projects on the local
// filesystem not included in the provided projects argument.
func projectsExistLocally(jirix *jiri.X, projects Projects) (bool, error) {
	jirix.TimerPush("match manifest")
	defer jirix.TimerPop()
	for _, p := range projects {
		isLocal, err := IsLocalProject(jirix, p.Path)
		if err != nil {
			return false, err
		}
		if !isLocal {
			return false, nil
		}
	}
	return true, nil
}

func MatchLocalWithRemote(localProjects, remoteProjects Projects) {
	localKeysNotInRemote := make(map[ProjectKey]bool)
	for key, _ := range localProjects {
		if _, ok := remoteProjects[key]; !ok {
			localKeysNotInRemote[key] = true
		}
	}
	// no stray local projects
	if len(localKeysNotInRemote) == 0 {
		return
	}

	for remoteKey, remoteProject := range remoteProjects {
		if _, ok := localProjects[remoteKey]; !ok {
			for localKey, _ := range localKeysNotInRemote {
				localProject := localProjects[localKey]
				if localProject.Path == remoteProject.Path && (localProject.Name == remoteProject.Name || localProject.Remote == remoteProject.Remote) {
					delete(localProjects, localKey)
					delete(localKeysNotInRemote, localKey)
					// Change local project key
					localProject.ComputedKey = remoteKey
					localProjects[remoteKey] = localProject
					// no more stray local projects
					if len(localKeysNotInRemote) == 0 {
						return
					}
					break
				}
			}
		}
	}
}

// UpdateUniverse updates all local projects and tools to match the remote
// counterparts identified in the manifest. Optionally, the 'gc' flag can be
// used to indicate that local projects that no longer exist remotely should be
// removed.
func UpdateUniverse(jirix *jiri.X, gc bool, localManifest bool, rebaseTracked bool, rebaseUntracked bool, rebaseAll bool, runHooks bool, runHookTimeout uint) (e error) {
	jirix.Logger.Infof("Updating all projects")

	updateFn := func(scanMode ScanMode) error {
		jirix.TimerPush(fmt.Sprintf("update universe: %s", scanMode))
		defer jirix.TimerPop()

		// Find all local projects.
		localProjects, err := LocalProjects(jirix, scanMode)
		if err != nil {
			return err
		}

		// Determine the set of remote projects and match them up with the locals.
		remoteProjects, hooks, err := LoadUpdatedManifest(jirix, localProjects, localManifest)
		MatchLocalWithRemote(localProjects, remoteProjects)

		if err != nil {
			return err
		}

		// Actually update the projects.
		return updateProjects(jirix, localProjects, remoteProjects, hooks, gc, runHookTimeout, rebaseTracked, rebaseUntracked, rebaseAll, false /*snapshot*/, runHooks)
	}

	// Specifying gc should always force a full filesystem scan.
	if gc {
		return updateFn(FullScan)
	}

	// Attempt a fast update, which uses the latest snapshot to avoid doing
	// a filesystem scan.  Sometimes the latest snapshot can have problems, so if
	// any errors come up, fallback to the slow path.
	err := updateFn(FastScan)
	if err != nil {
		if err2 := updateFn(FullScan); err2 != nil {
			if err.Error() == err2.Error() {
				return err
			}
			return fmt.Errorf("%v, %v", err, err2)
		}
	}

	return nil
}

// WriteUpdateHistorySnapshot creates a snapshot of the current state of all
// projects and writes it to the update history directory.
func WriteUpdateHistorySnapshot(jirix *jiri.X, snapshotPath string, hooks Hooks, localManifest bool) error {
	snapshotFile := filepath.Join(jirix.UpdateHistoryDir(), time.Now().Format(time.RFC3339))
	if err := CreateSnapshot(jirix, snapshotFile, hooks, localManifest); err != nil {
		return err
	}

	latestLink, secondLatestLink := jirix.UpdateHistoryLatestLink(), jirix.UpdateHistorySecondLatestLink()

	// If the "latest" symlink exists, point the "second-latest" symlink to its value.
	latestLinkExists, err := isFile(latestLink)
	if err != nil {
		return err
	}
	if latestLinkExists {
		latestFile, err := os.Readlink(latestLink)
		if err != nil {
			return fmtError(err)
		}
		if err := os.RemoveAll(secondLatestLink); err != nil {
			return fmtError(err)
		}
		if err := os.Symlink(latestFile, secondLatestLink); err != nil {
			return fmtError(err)
		}
	}

	// Point the "latest" update history symlink to the new snapshot file.  Try
	// to keep the symlink relative, to make it easy to move or copy the entire
	// update_history directory.
	if rel, err := filepath.Rel(filepath.Dir(latestLink), snapshotFile); err == nil {
		snapshotFile = rel
	}
	if err := os.RemoveAll(latestLink); err != nil {
		return fmtError(err)
	}
	return fmtError(os.Symlink(snapshotFile, latestLink))
}

// CleanupProjects restores the given jiri projects back to their detached
// heads, resets to the specified revision if there is one, and gets rid of
// all the local changes. If "cleanupBranches" is true, it will also delete all
// the non-master branches.
func CleanupProjects(jirix *jiri.X, localProjects Projects, cleanupBranches bool) (e error) {
	remoteProjects, _, err := LoadManifest(jirix)
	if err != nil {
		return err
	}
	cleanLimit := make(chan struct{}, jirix.Jobs)
	errs := make(chan error, len(localProjects))
	var wg sync.WaitGroup
	for _, local := range localProjects {
		wg.Add(1)
		cleanLimit <- struct{}{}
		go func(local Project) {
			defer func() { <-cleanLimit }()
			defer wg.Done()

			if local.LocalConfig.Ignore || local.LocalConfig.NoUpdate {
				jirix.Logger.Warningf("Project %s(%s) won't be updated due to it's local-config\n\n", local.Name, local.Path)
				return
			}
			remote, ok := remoteProjects[local.Key()]
			if !ok {
				jirix.Logger.Errorf("Not cleaning project %q(%v). It was not found in manifest\n\n", local.Name, local.Path)
				jirix.IncrementFailures()
				return
			}
			if err := resetLocalProject(jirix, local, remote, cleanupBranches); err != nil {
				errs <- fmt.Errorf("Erorr cleaning project %q: %v", local.Name, err)
			}
		}(local)
	}
	wg.Wait()
	close(errs)

	multiErr := make(MultiError, 0)
	for err := range errs {
		multiErr = append(multiErr, err)
	}
	if len(multiErr) != 0 {
		return multiErr
	}
	return nil
}

// resetLocalProject checks out the detached_head, cleans up untracked files
// and uncommitted changes, and optionally deletes all the branches except master.
func resetLocalProject(jirix *jiri.X, local, remote Project, cleanupBranches bool) error {
	scm := gitutil.New(jirix, gitutil.RootDirOpt(local.Path))
	headRev, err := GetHeadRevision(jirix, remote)
	if err != nil {
		return err
	} else {
		if headRev, err = scm.CurrentRevisionForRef(headRev); err != nil {
			return fmt.Errorf("Cannot find revision for ref %q for project %q: %v", headRev, local.Name, err)
		}
	}
	if local.Revision != headRev {
		if err := scm.CheckoutBranch(headRev, gitutil.DetachOpt(true), gitutil.ForceOpt(true)); err != nil {
			return err
		}
	}
	// Cleanup changes.
	if err := scm.RemoveUntrackedFiles(); err != nil {
		return err
	}
	if !cleanupBranches {
		return nil
	}

	// Delete all the other branches.
	branches, _, err := scm.GetBranches()
	if err != nil {
		return fmt.Errorf("Cannot get branches for project %q: %v", local.Name, err)
	}
	for _, branch := range branches {
		if err := scm.DeleteBranch(branch, gitutil.ForceOpt(true)); err != nil {
			return err
		}
	}
	return nil
}

// IsLocalProject returns true if there is a project at the given path.
func IsLocalProject(jirix *jiri.X, path string) (bool, error) {
	// Existence of a metadata directory is how we know we've found a
	// Jiri-maintained project.
	metadataDir := filepath.Join(path, jiri.ProjectMetaDir)
	if _, err := os.Stat(metadataDir); err != nil {
		if os.IsNotExist(err) {
			// Check for old meta directory
			oldMetadataDir := filepath.Join(path, jiri.OldProjectMetaDir)
			if _, err := os.Stat(oldMetadataDir); err != nil {
				if os.IsNotExist(err) {
					return false, nil

				}
				return false, fmtError(err)
			}
			// Old metadir found, move it
			if err := os.Rename(oldMetadataDir, metadataDir); err != nil {
				return false, fmtError(err)
			}
			return true, nil
		} else if os.IsPermission(err) {
			jirix.Logger.Warningf("Directory %q doesn't have read permission, skipping it\n\n", path)
			return false, nil
		}
		return false, fmtError(err)
	}
	return true, nil
}

// ProjectAtPath returns a Project struct corresponding to the project at the
// path in the filesystem.
func ProjectAtPath(jirix *jiri.X, path string) (Project, error) {
	metadataFile := filepath.Join(path, jiri.ProjectMetaDir, jiri.ProjectMetaFile)
	project, err := ProjectFromFile(jirix, metadataFile)
	if err != nil {
		return Project{}, err
	}
	localConfigFile := filepath.Join(path, jiri.ProjectMetaDir, jiri.ProjectConfigFile)
	if project.LocalConfig, err = LocalConfigFromFile(jirix, localConfigFile); err != nil {
		return *project, fmt.Errorf("Error while reading config for project %s(%s): %s", project.Name, path, err)
	}
	return *project, nil
}

// findLocalProjects scans the filesystem for all projects.  Note that project
// directories can be nested recursively.
func findLocalProjects(jirix *jiri.X, path string, projects Projects) MultiError {
	log := make(chan string, jirix.Jobs)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for str := range log {
			jirix.Logger.Warningf("%s", str)
		}
	}()
	errs := make(chan error, jirix.Jobs)
	var multiErr MultiError
	go func() {
		defer wg.Done()
		for err := range errs {
			multiErr = append(multiErr, err)
		}
	}()
	var pwg sync.WaitGroup
	workq := make(chan string, jirix.Jobs)
	projectsMutex := &sync.Mutex{}
	processPath := func(path string) {
		defer pwg.Done()
		isLocal, err := IsLocalProject(jirix, path)
		if err != nil {
			errs <- fmt.Errorf("Error while processing path %q: %v", path, err)
			return
		}
		if isLocal {
			project, err := ProjectAtPath(jirix, path)
			if err != nil {
				errs <- fmt.Errorf("Error while processing path %q: %v", path, err)
				return
			}
			if path != project.Path {
				logs := []string{fmt.Sprintf("Project %q has path %s, but was found in %s.", project.Name, project.Path, path),
					fmt.Sprintf("jiri will treat it as a stale project. To remove this warning please delete this or move it out of your root folder\n\n")}
				log <- strings.Join(logs, "\n")
				return
			}
			projectsMutex.Lock()
			if p, ok := projects[project.Key()]; ok {
				projectsMutex.Unlock()
				errs <- fmt.Errorf("name conflict: both %s and %s contain project with key %v", p.Path, project.Path, project.Key())
				return
			}
			projects[project.Key()] = project
			projectsMutex.Unlock()
		}

		// Recurse into all the sub directories.
		fileInfos, err := ioutil.ReadDir(path)
		if err != nil && !os.IsPermission(err) {
			errs <- fmt.Errorf("cannot read dir %q: %v", path, err)
			return
		}
		pwg.Add(1)
		go func(fileInfos []os.FileInfo) {
			defer pwg.Done()
			for _, fileInfo := range fileInfos {
				if fileInfo.IsDir() && !strings.HasPrefix(fileInfo.Name(), ".") {
					pwg.Add(1)
					workq <- filepath.Join(path, fileInfo.Name())
				}
			}
		}(fileInfos)
	}
	pwg.Add(1)
	workq <- path
	for i := uint(0); i < jirix.Jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range workq {
				processPath(path)
			}
		}()
	}
	pwg.Wait()
	close(errs)
	close(log)
	close(workq)
	wg.Wait()
	return multiErr
}

func fetchAll(jirix *jiri.X, project Project) error {
	if project.Remote == "" {
		return fmt.Errorf("project %q does not have a remote", project.Name)
	}
	scm := gitutil.New(jirix, gitutil.RootDirOpt(project.Path))
	remote := rewriteRemote(jirix, project.Remote)
	if err := scm.SetRemoteUrl("origin", remote); err != nil {
		return err
	}
	if project.HistoryDepth > 0 {
		return fetch(jirix, project.Path, "origin", gitutil.PruneOpt(true),
			gitutil.DepthOpt(project.HistoryDepth), gitutil.UpdateShallowOpt(true))
	} else {
		return fetch(jirix, project.Path, "origin", gitutil.PruneOpt(true))
	}
}

func GetHeadRevision(jirix *jiri.X, project Project) (string, error) {
	if err := project.fillDefaults(); err != nil {
		return "", err
	}
	// Having a specific revision trumps everything else.
	if project.Revision != "HEAD" {
		return project.Revision, nil
	}
	return "remotes/origin/" + project.RemoteBranch, nil
}

func checkoutHeadRevision(jirix *jiri.X, project Project, forceCheckout bool) error {
	revision, err := GetHeadRevision(jirix, project)
	if err != nil {
		return err
	}
	git := gitutil.New(jirix, gitutil.RootDirOpt(project.Path))
	err = git.CheckoutBranch(revision, gitutil.DetachOpt(true), gitutil.ForceOpt(forceCheckout))
	if err == nil {
		return nil
	}
	if project.Revision != "" && project.Revision != "HEAD" {
		//might be a tag
		if err2 := fetch(jirix, project.Path, "origin", gitutil.FetchTagOpt(project.Revision)); err2 != nil {
			// error while fetching tag, return original err and debug log this err
			jirix.Logger.Debugf("Error while fetching tag for project %s (%s): %s\n\n", project.Name, project.Path, err2)
			return err
		} else {
			return git.CheckoutBranch(revision, gitutil.DetachOpt(true), gitutil.ForceOpt(forceCheckout))
		}
	}
	return err
}

func tryRebase(jirix *jiri.X, project Project, branch string) (bool, error) {
	scm := gitutil.New(jirix, gitutil.RootDirOpt(project.Path))
	if err := scm.Rebase(branch); err != nil {
		err := scm.RebaseAbort()
		return false, err
	}
	return true, nil
}

// syncProjectMaster checks out latest detached head if project is on one
// else it rebases current branch onto its tracking branch
func syncProjectMaster(jirix *jiri.X, project Project, state ProjectState, rebaseTracked, rebaseUntracked, rebaseAll, snapshot bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmtError(err)
	}
	relativePath, err := filepath.Rel(cwd, project.Path)
	if err != nil {
		// Just use the full path if an error occurred.
		relativePath = project.Path
	}
	if project.LocalConfig.Ignore || project.LocalConfig.NoUpdate {
		jirix.Logger.Warningf("Project %s(%s) won't be updated due to it's local-config\n\n", project.Name, relativePath)
		return nil
	}

	scm := gitutil.New(jirix, gitutil.RootDirOpt(project.Path))

	if uncommitted, err := scm.HasUncommittedChanges(); err != nil {
		return fmt.Errorf("Cannot get uncommited changes for project %q: %s", project.Name, err)
	} else if uncommitted {
		msg := fmt.Sprintf("Project %s(%s) contains uncommited changes.", project.Name, relativePath)
		msg += fmt.Sprintf("\nCommit or discard the changes and try again.\n\n")
		jirix.Logger.Errorf(msg)
		jirix.IncrementFailures()
		return nil
	}

	if state.CurrentBranch.Name == "" || snapshot { // detached head
		if err := checkoutHeadRevision(jirix, project, false); err != nil {
			revision, err2 := GetHeadRevision(jirix, project)
			if err2 != nil {
				return err2
			}
			gitCommand := jirix.Color.Yellow("git -C %q checkout --detach %s", relativePath, revision)
			msg := fmt.Sprintf("For project %q, not able to checkout latest, error: %s", project.Name, err)
			msg += fmt.Sprintf("\nPlease checkout manually use: '%s'\n\n", gitCommand)
			jirix.Logger.Errorf(msg)
			jirix.IncrementFailures()
		}
		if snapshot || !rebaseAll {
			return nil
		}
		// This should run after program exit so that detached head can be restored
		defer func() {
			if err := checkoutHeadRevision(jirix, project, false); err != nil {
				// This should not happen, panic
				panic(fmt.Sprintf("for project %s(%s), not able to checkout head revision: %s", project.Name, relativePath, err))
			}
		}()
	} else if rebaseAll {
		// This should run after program exit so that original branch can be restored
		defer func() {
			if err := scm.CheckoutBranch(state.CurrentBranch.Name); err != nil {
				// This should not happen, panic
				panic(fmt.Sprintf("for project %s(%s), not able to checkout branch %q: %s", project.Name, relativePath, state.CurrentBranch.Name, err))
			}
		}()
	}

	// if rebase flag is false, merge fast forward current branch
	if !rebaseTracked && !rebaseAll && state.CurrentBranch.Tracking != nil {
		tracking := state.CurrentBranch.Tracking
		if tracking.Revision == state.CurrentBranch.Revision {
			return nil
		}
		if project.LocalConfig.NoRebase {
			jirix.Logger.Warningf("For project %s(%s), not merging your local branches due to it's local-config\n\n", project.Name, relativePath)
			return nil
		}
		if err := scm.Merge(tracking.Name, gitutil.FfOnlyOpt(true)); err != nil {
			msg := fmt.Sprintf("For project %s(%s), not able to fast forward your local branch %q to %q\n\n", project.Name, relativePath, state.CurrentBranch.Name, tracking.Name)
			jirix.Logger.Errorf(msg)
			jirix.IncrementFailures()
		}
		return nil
	}

	branches := state.Branches
	if !rebaseAll {
		branches = []BranchState{state.CurrentBranch}
	}
	branchMap := make(map[string]BranchState)
	for _, branch := range branches {
		branchMap[branch.Name] = branch
	}
	rebaseUntrackedMessage := false
	headRevision, err := GetHeadRevision(jirix, project)
	if err != nil {
		return err
	}
	branchesContainingHead, err := scm.ListBranchesContainingRef(headRevision)
	if err != nil {
		return err
	}
	for _, branch := range branches {
		tracking := branch.Tracking
		circularDependencyMap := make(map[string]bool)
		circularDependencyMap[branch.Name] = true
		rebase := true
		if tracking != nil {
			circularDependencyMap[tracking.Name] = true
			_, ok := branchMap[tracking.Name]
			for ok {
				t := branchMap[tracking.Name].Tracking
				if t == nil {
					break
				}
				if circularDependencyMap[t.Name] {
					rebase = false
					msg := fmt.Sprintf("For project %s(%s), branch %q has circular dependency, not rebasing it.\n\n", project.Name, relativePath, branch.Name)
					jirix.Logger.Errorf(msg)
					jirix.IncrementFailures()
					break
				}
				circularDependencyMap[t.Name] = true
				tracking = t
				_, ok = branchMap[tracking.Name]
			}
		}
		if !rebase {
			continue
		}
		if tracking != nil { // tracked branch
			if branch.Revision == tracking.Revision {
				continue
			}
			if project.LocalConfig.NoRebase {
				jirix.Logger.Warningf("For project %s(%s), not rebasing your local branches due to it's local-config\n\n", project.Name, relativePath)
				break
			}

			if err := scm.CheckoutBranch(branch.Name); err != nil {
				msg := fmt.Sprintf("For project %s(%s), not able to rebase your local branch %q onto %q", project.Name, relativePath, branch.Name, tracking.Name)
				msg += "\nPlease do it manually\n\n"
				jirix.Logger.Errorf(msg)
				jirix.IncrementFailures()
				continue
			}
			rebaseSuccess, err := tryRebase(jirix, project, tracking.Name)
			if err != nil {
				return err
			}
			if rebaseSuccess {
				jirix.Logger.Debugf("For project %q, rebased your local branch %q on %q", project.Name, branch.Name, tracking.Name)
			} else {
				msg := fmt.Sprintf("For project %s(%s), not able to rebase your local branch %q onto %q", project.Name, relativePath, branch.Name, tracking.Name)
				msg += "\nPlease do it manually\n\n"
				jirix.Logger.Errorf(msg)
				jirix.IncrementFailures()
				continue
			}
		} else {
			if branchesContainingHead[branch.Name] {
				continue
			}
			if rebaseUntracked {
				if project.LocalConfig.NoRebase {
					jirix.Logger.Warningf("For project %s(%s), not rebasing your local branches due to it's local-config\n\n", project.Name, relativePath)
					break
				}

				if err := scm.CheckoutBranch(branch.Name); err != nil {
					msg := fmt.Sprintf("For project %s(%s), not able to rebase your untracked branch %q onto JIRI_HEAD.", project.Name, relativePath, branch.Name)
					msg += "\nPlease do it manually\n\n"
					jirix.Logger.Errorf(msg)
					jirix.IncrementFailures()
					continue
				}
				rebaseSuccess, err := tryRebase(jirix, project, headRevision)
				if err != nil {
					return err
				}
				if rebaseSuccess {
					jirix.Logger.Debugf("For project %q, rebased your untracked branch %q on %q", project.Name, branch.Name, headRevision)
				} else {
					msg := fmt.Sprintf("For project %s(%s), not able to rebase your untracked branch %q onto JIRI_HEAD.", project.Name, relativePath, branch.Name)
					msg += "\nPlease do it manually\n\n"
					jirix.Logger.Errorf(msg)
					jirix.IncrementFailures()
					continue
				}
			} else if !rebaseUntrackedMessage {
				// Post this message only once
				rebaseUntrackedMessage = true
				gitCommand := jirix.Color.Yellow("git -C %q checkout %s && git -C %q rebase %s", relativePath, branch.Name, relativePath, headRevision)
				msg := fmt.Sprintf("For Project %q, branch %q does not track any remote branch.", project.Name, branch.Name)
				msg += fmt.Sprintf("\nTo rebase it update with -rebase-untracked flag, or to rebase it manually run")
				msg += fmt.Sprintf("\n%s\n\n", gitCommand)
				jirix.Logger.Warningf(msg)
				continue
			}
		}
	}
	return nil
}

// setRemoteHeadRevisions set the repo statuses from remote for
// projects at HEAD so we can detect when a local project is already
// up-to-date.
func setRemoteHeadRevisions(jirix *jiri.X, remoteProjects Projects, localProjects Projects) MultiError {
	jirix.TimerPush("Set Remote Revisions")
	defer jirix.TimerPop()

	keys := make(chan ProjectKey, len(remoteProjects))
	updatedRemotes := make(chan Project, len(remoteProjects))
	errs := make(chan error, len(remoteProjects))
	var wg sync.WaitGroup

	for i := uint(0); i < jirix.Jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range keys {
				local := localProjects[key]
				remote := remoteProjects[key]
				scm := gitutil.New(jirix, gitutil.RootDirOpt(local.Path))
				b := "master"
				if remote.RemoteBranch != "" {
					b = remote.RemoteBranch
				}
				rev, err := scm.CurrentRevisionForRef("remotes/origin/" + b)
				if err != nil {
					errs <- err
					return
				}
				remote.Revision = rev
				updatedRemotes <- remote
			}
		}()
	}

	for key, local := range localProjects {
		remote, ok := remoteProjects[key]
		// Don't update when project has pinned revision or it's remote has changed
		if !ok || remote.Revision != "HEAD" || local.Remote != remote.Remote {
			continue
		}
		keys <- key
	}

	close(keys)
	wg.Wait()
	close(updatedRemotes)
	close(errs)

	for remote := range updatedRemotes {
		remoteProjects[remote.Key()] = remote
	}

	var multiErr MultiError
	for err := range errs {
		multiErr = append(multiErr, err)
	}

	return multiErr
}

func updateOrCreateCache(jirix *jiri.X, dir, remote, branch string, depth int) error {
	refspec := "+refs/heads/*:refs/heads/*"
	if depth > 0 {
		// Shallow cache, fetch only manifest tracked remote branch
		refspec = fmt.Sprintf("+refs/heads/%s:refs/heads/%s", branch, branch)
	}
	if isPathDir(dir) {
		if err := gitutil.New(jirix, gitutil.RootDirOpt(dir)).SetRemoteUrl("origin", remote); err != nil {
			return err
		}
		// Cache already present, update it
		// TODO : update this after implementing FetchAll using g
		msg := fmt.Sprintf("Updating cache: %q", dir)
		task := jirix.Logger.AddTaskMsg(msg)
		defer task.Done()
		t := jirix.Logger.TrackTime(msg)
		defer t.Done()
		// We need to explicitly specify the ref for fetch to update in case
		// the cache was created with a previous version and uses "refs/*"
		if err := retry.Function(jirix, func() error {
			return gitutil.New(jirix, gitutil.RootDirOpt(dir)).FetchRefspec("origin", refspec, gitutil.PruneOpt(true))
		}, fmt.Sprintf("Fetching for %s:%s", dir, refspec),
			retry.AttemptsOpt(jirix.Attempts)); err != nil {
			return err
		}
	} else {
		// Create cache
		// TODO : If we in future need to support two projects with same remote url,
		// one with shallow checkout and one with full, we should create two caches
		msg := fmt.Sprintf("Creating cache: %q", dir)
		task := jirix.Logger.AddTaskMsg(msg)
		defer task.Done()
		t := jirix.Logger.TrackTime(msg)
		defer t.Done()
		if err := gitutil.New(jirix).Clone(remote, dir, gitutil.BareOpt(true), gitutil.DepthOpt(depth)); err != nil {
			return err
		}
		// We need to explicitly specify the ref for fetch to update the bare
		// repository.
		if err := gitutil.New(jirix, gitutil.RootDirOpt(dir)).Config("remote.origin.fetch", refspec); err != nil {
			return err
		}
	}
	return nil
}

// updateCache creates the cache or updates it if already present.
func updateCache(jirix *jiri.X, remoteProjects Projects) error {
	jirix.TimerPush("update cache")
	defer jirix.TimerPop()
	if jirix.Cache == "" {
		return nil
	}

	errs := make(chan error, len(remoteProjects))
	var wg sync.WaitGroup
	processingPath := make(map[string]bool)
	fetchLimit := make(chan struct{}, jirix.Jobs)
	for _, project := range remoteProjects {
		if cacheDirPath, err := project.CacheDirPath(jirix); err == nil {
			if processingPath[cacheDirPath] {
				continue
			}
			processingPath[cacheDirPath] = true
			wg.Add(1)
			fetchLimit <- struct{}{}
			if err := project.fillDefaults(); err != nil {
				errs <- err
				continue
			}
			go func(dir, remote string, depth int, branch string) {
				defer func() { <-fetchLimit }()
				defer wg.Done()
				remote = rewriteRemote(jirix, remote)
				if err := updateOrCreateCache(jirix, dir, remote, branch, depth); err != nil {
					errs <- err
					return
				}
			}(cacheDirPath, project.Remote, project.HistoryDepth, project.RemoteBranch)
		} else {
			errs <- err
		}
	}
	wg.Wait()
	close(errs)

	multiErr := make(MultiError, 0)
	for err := range errs {
		multiErr = append(multiErr, err)
	}
	if len(multiErr) != 0 {
		return multiErr
	}

	return nil
}

func fetchLocalProjects(jirix *jiri.X, localProjects, remoteProjects Projects) error {
	jirix.TimerPush("fetch local projects")
	defer jirix.TimerPop()
	fetchLimit := make(chan struct{}, jirix.Jobs)
	errs := make(chan error, len(localProjects))
	var wg sync.WaitGroup
	for key, project := range localProjects {
		if r, ok := remoteProjects[key]; ok {
			if project.LocalConfig.Ignore || project.LocalConfig.NoUpdate {
				jirix.Logger.Warningf("Not updating remotes for project %s(%s) due to its local-config\n\n", project.Name, project.Path)
				continue
			}
			// Don't fetch when remote url has changed as that may cause fetch to fail
			if r.Remote != project.Remote {
				continue
			}
			wg.Add(1)
			fetchLimit <- struct{}{}
			project.HistoryDepth = r.HistoryDepth
			go func(project Project) {
				defer func() { <-fetchLimit }()
				defer wg.Done()
				task := jirix.Logger.AddTaskMsg("Fetching remotes for project %q", project.Name)
				defer task.Done()
				if err := fetchAll(jirix, project); err != nil {
					errs <- fmt.Errorf("fetch failed for %v: %v", project.Name, err)
					return
				}
			}(project)
		}
	}
	wg.Wait()
	close(errs)

	multiErr := make(MultiError, 0)
	for err := range errs {
		multiErr = append(multiErr, err)
	}
	if len(multiErr) != 0 {
		return multiErr
	}
	return nil
}

func updateProjects(jirix *jiri.X, localProjects, remoteProjects Projects, hooks Hooks, gc bool, runHookTimeout uint, rebaseTracked, rebaseUntracked, rebaseAll, snapshot, shouldRunHooks bool) error {
	jirix.TimerPush("update projects")
	defer jirix.TimerPop()

	if err := updateCache(jirix, remoteProjects); err != nil {
		return err
	}
	if err := fetchLocalProjects(jirix, localProjects, remoteProjects); err != nil {
		return err
	}
	states, err := GetProjectStates(jirix, localProjects, false)
	if err != nil {
		return err
	}
	if err := setRemoteHeadRevisions(jirix, remoteProjects, localProjects); err != nil {
		return err
	}

	ops := computeOperations(localProjects, remoteProjects, states, gc, rebaseTracked, rebaseUntracked, rebaseAll, snapshot)
	moveOperations := []moveOperation{}
	changeRemoteOperations := operations{}
	deleteOperations := []deleteOperation{}
	updateOperations := operations{}
	createOperations := []createOperation{}
	nullOperations := operations{}
	updates := newFsUpdates()
	for _, op := range ops {
		if err := op.Test(jirix, updates); err != nil {
			return err
		}
		switch o := op.(type) {
		case deleteOperation:
			deleteOperations = append(deleteOperations, o)
		case changeRemoteOperation:
			changeRemoteOperations = append(changeRemoteOperations, o)
		case moveOperation:
			moveOperations = append(moveOperations, o)
		case updateOperation:
			updateOperations = append(updateOperations, o)
		case createOperation:
			createOperations = append(createOperations, o)
		case nullOperation:
			nullOperations = append(nullOperations, o)
		}
	}
	if err := runDeleteOperations(jirix, deleteOperations, gc); err != nil {
		return err
	}
	if err := runCommonOperations(jirix, changeRemoteOperations, log.DebugLevel); err != nil {
		return err
	}
	if err := runMoveOperations(jirix, moveOperations); err != nil {
		return err
	}
	if err := runCommonOperations(jirix, updateOperations, log.DebugLevel); err != nil {
		return err
	}
	if err := runCreateOperations(jirix, createOperations); err != nil {
		return err
	}
	if err := runCommonOperations(jirix, nullOperations, log.TraceLevel); err != nil {
		return err
	}
	jirix.TimerPush("jiri revision files")
	for _, project := range remoteProjects {
		if !(project.LocalConfig.Ignore || project.LocalConfig.NoUpdate) {
			project.writeJiriRevisionFiles(jirix)
		}
	}
	jirix.TimerPop()

	if projectStatuses, err := getProjectStatus(jirix, remoteProjects); err != nil {
		return fmt.Errorf("Error getting project status: %s", err)
	} else if len(projectStatuses) != 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return fmtError(err)
		}
		msg := "Projects with local changes and/or not on JIRI_HEAD:"
		for _, p := range projectStatuses {
			relativePath, err := filepath.Rel(cwd, p.Project.Path)
			if err != nil {
				// Just use the full path if an error occurred.
				relativePath = p.Project.Path
			}
			msg = fmt.Sprintf("%s\n%s (%s):", msg, p.Project.Name, relativePath)
			if p.HasChanges {
				msg = fmt.Sprintf("%s (%s)", msg, jirix.Color.Yellow("Has changes"))
			}
			if !p.IsOnJiriHead {
				msg = fmt.Sprintf("%s (%s)", msg, jirix.Color.Yellow("Not on JIRI_HEAD"))
			}
		}
		jirix.Logger.Warningf("%s\n\n", msg)
	}

	if shouldRunHooks {
		if err := RunHooks(jirix, hooks, runHookTimeout); err != nil {
			return err
		}
	}
	return applyGitHooks(jirix, ops)
}

type ProjectStatus struct {
	Project      Project
	HasChanges   bool
	IsOnJiriHead bool
}

func getProjectStatus(jirix *jiri.X, ps Projects) ([]ProjectStatus, MultiError) {
	jirix.TimerPush("jiri status")
	defer jirix.TimerPop()
	workQueue := make(chan Project, len(ps))
	projectStatuses := make(chan ProjectStatus, len(ps))
	errs := make(chan error, len(ps))
	var wg sync.WaitGroup
	for _, project := range ps {
		workQueue <- project
	}
	close(workQueue)
	for i := uint(0); i < jirix.Jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for project := range workQueue {
				if project.LocalConfig.Ignore || project.LocalConfig.NoUpdate {
					continue
				}
				scm := gitutil.New(jirix, gitutil.RootDirOpt(project.Path))
				uncommitted, err := scm.HasUncommittedChanges()
				if err != nil {
					errs <- fmt.Errorf("Cannot get uncommited changes for project %q: %s", project.Name, err)
					continue
				}

				isOnJiriHead, err := project.IsOnJiriHead(jirix)
				if err != nil {
					errs <- err
					continue
				}
				if uncommitted || !isOnJiriHead {
					projectStatuses <- ProjectStatus{project, uncommitted, isOnJiriHead}
				}
			}
		}()
	}
	wg.Wait()
	close(projectStatuses)
	close(errs)

	var multiErr MultiError
	for err := range errs {
		multiErr = append(multiErr, err)
	}
	var psa []ProjectStatus
	for projectStatus := range projectStatuses {
		psa = append(psa, projectStatus)
	}
	return psa, multiErr
}

// writeMetadata stores the given project metadata in the directory
// identified by the given path.
func writeMetadata(jirix *jiri.X, project Project, dir string) (e error) {
	metadataDir := filepath.Join(dir, jiri.ProjectMetaDir)
	if err := os.MkdirAll(metadataDir, os.FileMode(0755)); err != nil {
		return fmtError(err)
	}
	metadataFile := filepath.Join(metadataDir, jiri.ProjectMetaFile)
	return project.ToFile(jirix, metadataFile)
}
