// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package project

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"go.fuchsia.dev/jiri"
	"go.fuchsia.dev/jiri/cipd"
	"go.fuchsia.dev/jiri/gitutil"
	"go.fuchsia.dev/jiri/log"
	"go.fuchsia.dev/jiri/retry"
)

var (
	errVersionMismatch    = errors.New("snapshot file version mismatch")
	ssoRe                 = regexp.MustCompile("^sso://(.*?)/")
	DefaultHookTimeout    = uint(5)  // DefaultHookTimeout is the time in minutes to wait for a hook to timeout.
	DefaultPackageTimeout = uint(20) // DefaultPackageTimeout is the time in minutes to wait for cipd fetching packages.
)

const (
	JiriProject     = "release.go.jiri"
	JiriName        = "jiri"
	JiriPackage     = "go.fuchsia.dev/jiri"
	ManifestVersion = "1.1"
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

	// Attributes is a list of attributes for a project seperated by comma.
	// The project will not be fetched by default when attributes are present.
	Attributes string `xml:"attributes,attr,omitempty"`

	// GitAttributes is a list comma-separated attributes for a project,
	// which will be helpful to group projects with similar purposes together.
	// It will be used for .gitattributes file generation.
	GitAttributes string `xml:"git_attributes,attr,omitempty"`

	// Flag defines the content that should be written to a file when
	// this project is successfully fetched.
	Flag string `xml:"flag,attr,omitempty"`

	XMLName struct{} `xml:"project"`

	// This is used to store computed key. This is useful when remote and
	// local projects are same but have different name or remote
	ComputedKey ProjectKey `xml:"-"`

	// This stores the local configuration file for the project
	LocalConfig LocalConfig `xml:"-"`

	// ComputedAttributes stores computed attributes object
	// which is easiler to perform matching and comparing.
	ComputedAttributes attributes `xml:"-"`

	// ManifestPath stores the absolute path of the manifest.
	ManifestPath string `xml:"-"`
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

// MakeProjectKey returns the project key, given the project name and normalized remote.
func MakeProjectKey(name, remote string) ProjectKey {
	return ProjectKey(name + KeySeparator + rewriteAndNormalizeRemote(remote))
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
	if other.Flag != "" {
		p.Flag = other.Flag
	}
}

// WriteProjectFlags write flag files into project directory using in "flag"
// attribute from projs.
func WriteProjectFlags(jirix *jiri.X, projs Projects) error {
	// The flag attribute has a format of $FILE_NAME|$FLAG_SUCCESSFUL|$FLAG_FAILED
	// When a package is successfully downloaded, jiri will write $FLAG_SUCCESSFUL
	// to $FILE_NAME. If the package is not downloaded due to access reasons,
	// jiri will write $FLAG_FAILED to $FILE_NAME.
	// '|' is a forbidden symbol in Windows path, which is unlikely
	// to be used by path.

	// Unlike WritePackageFlags that writes the failure flags when the package was
	// not fetched due to permission issues, this function will not write failure
	// flags, as unfetchable projects are considered as errors.
	flagMap := make(map[string]string)
	fill := func(file, flag string) error {
		if v, ok := flagMap[file]; ok {
			if v != flag {
				return fmt.Errorf("encountered conflicting flags for file %q: %q conflicts with %q", file, v, flag)
			}
		} else {
			flagMap[file] = flag
		}
		return nil
	}

	for _, v := range projs {
		if v.Flag == "" {
			continue
		}
		fields := strings.Split(v.Flag, "|")
		if len(fields) != 3 {
			return fmt.Errorf("unknown project flag format found in project %+v", v)
		}
		if err := fill(fields[0], fields[1]); err != nil {
			return err
		}
	}

	var writeErrorBuf bytes.Buffer
	for k, v := range flagMap {
		if err := ioutil.WriteFile(filepath.Join(jirix.Root, k), []byte(v), 0644); err != nil {
			writeErrorBuf.WriteString(fmt.Sprintf("write package flag %q to file %q failed: %v\n", v, k, err))
		}
	}
	if writeErrorBuf.Len() > 0 {
		return errors.New(writeErrorBuf.String())
	}
	return nil
}

type attributes map[string]bool

// newAttributes will create a new attributes object
// which is used in Project and Package objects.
func newAttributes(attrs string) attributes {
	retMap := make(attributes)
	if strings.HasPrefix(attrs, "+") {
		attrs = attrs[1:]
	}
	for _, v := range strings.Split(attrs, ",") {
		key := strings.TrimSpace(v)
		if key != "" {
			retMap[key] = true
		}
	}
	return retMap
}

func (m attributes) IsEmpty() bool {
	return len(m) == 0
}

func (m attributes) Add(other attributes) {
	for k := range other {
		if _, ok := m[k]; !ok {
			m[k] = true
		}
	}
}

func (m attributes) Match(other attributes) bool {
	for k := range other {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

func (m attributes) String() string {
	attrs := make([]string, 0)
	var buf bytes.Buffer
	for k := range m {
		attrs = append(attrs, k)
	}
	sort.Strings(attrs)
	first := true
	for _, v := range attrs {
		if !first {
			buf.WriteString(",")
		}
		buf.WriteString(v)
		first = false
	}
	return buf.String()
}

// ProjectLock describes locked version information for a jiri managed project.
type ProjectLock struct {
	Remote   string `json:"repository_url"`
	Name     string `json:"name"`
	Revision string `json:"revision"`
}

// ProjectLockKey defines the key used in ProjectLocks type
type ProjectLockKey string

// ProjectLocks type is a map wrapper over ProjectLock for faster look up.
type ProjectLocks map[ProjectLockKey]ProjectLock

func (p ProjectLock) Key() ProjectLockKey {
	return ProjectLockKey(p.Name + KeySeparator + p.Remote)
}

// PackageLock describes locked version information for a jiri managed package.
type PackageLock struct {
	PackageName string `json:"package"`
	LocalPath   string `json:"path,omitempty"`
	VersionTag  string `json:"version"`
	InstanceID  string `json:"instance_id"`
}

// PackageLockKey defines the key used in PackageLocks type
type PackageLockKey string

// PackageLocks type is map wrapper over PackageLock for faster look up
type PackageLocks map[PackageLockKey]PackageLock

func (p PackageLock) Key() PackageLockKey {
	return PackageLockKey(p.PackageName + KeySeparator + p.VersionTag)
}

// LockEqual determines whether current PackageLock has same version and
// instance id with PackageLock O.
func (P PackageLock) LockEqual(O PackageLock) bool {
	if P.PackageName != O.PackageName {
		return false
	}
	if P.VersionTag != O.VersionTag {
		return false
	}
	if P.InstanceID != O.InstanceID {
		return false
	}
	return true
}

// ResolveConfig interface provides the configuration
// for jiri resolve command.
type ResolveConfig interface {
	AllowFloatingRefs() bool
	LockFilePath() string
	LocalManifest() bool
	EnablePackageLock() bool
	EnableProjectLock() bool
	HostnameAllowList() []string
	FullResolve() bool
}

// UnmarshalLockEntries unmarshals project locks and package locks from
// jsonData.
func UnmarshalLockEntries(jsonData []byte) (ProjectLocks, PackageLocks, error) {
	projectLocks := make(ProjectLocks)
	pkgLocks := make(PackageLocks)
	var entries []map[string]string
	if err := json.Unmarshal(jsonData, &entries); err != nil {
		return nil, nil, err
	}
	for _, entry := range entries {
		if pkgName, ok := entry["package"]; ok {
			pkgLock := PackageLock{
				PackageName: pkgName,
				VersionTag:  entry["version"],
				InstanceID:  entry["instance_id"],
				LocalPath:   entry["path"],
			}
			if v, ok := pkgLocks[pkgLock.Key()]; ok {
				if v != pkgLock {
					return nil, nil, fmt.Errorf("package %q has more than 1 version lock %q, %q", pkgName, v.InstanceID, pkgLock.InstanceID)
				}
			}
			pkgLocks[pkgLock.Key()] = pkgLock
		} else if repoURL, ok := entry["repository_url"]; ok {
			projectLock := ProjectLock{
				Remote:   repoURL,
				Name:     entry["name"],
				Revision: entry["revision"],
			}
			if v, ok := projectLocks[projectLock.Key()]; ok {
				if v != projectLock {
					return nil, nil, fmt.Errorf("package %q has more than 1 revision lock %q, %q", repoURL, v.Revision, projectLock.Revision)
				}
			}
			projectLocks[projectLock.Key()] = projectLock
		}
		// Ignore unknown lockfile entries without raising an error
	}
	return projectLocks, pkgLocks, nil
}

// MarshalLockEntries marshals project locks and package locks into
// json format data.
func MarshalLockEntries(projectLocks ProjectLocks, pkgLocks PackageLocks) ([]byte, error) {
	entries := make([]interface{}, len(projectLocks)+len(pkgLocks))
	projEntries := make([]ProjectLock, len(projectLocks))
	pkgEntries := make([]PackageLock, len(pkgLocks))

	i := 0
	for _, v := range projectLocks {
		projEntries[i] = v
		i++
	}
	sort.Slice(projEntries, func(i, j int) bool {
		if projEntries[i].Remote == projEntries[j].Remote {
			return projEntries[i].Name < projEntries[j].Name
		}
		return projEntries[i].Remote < projEntries[j].Remote
	})

	i = 0
	for _, v := range pkgLocks {
		pkgEntries[i] = v
		i++
	}
	sort.Slice(pkgEntries, func(i, j int) bool {
		if pkgEntries[i].PackageName != pkgEntries[j].PackageName {
			return pkgEntries[i].PackageName < pkgEntries[j].PackageName
		}
		if pkgEntries[i].LocalPath != pkgEntries[j].LocalPath {
			return pkgEntries[i].LocalPath < pkgEntries[j].LocalPath
		}
		return pkgEntries[i].VersionTag < pkgEntries[j].VersionTag
	})

	i = 0
	for _, v := range projEntries {
		entries[i] = v
		i++
	}
	for _, v := range pkgEntries {
		entries[i] = v
		i++
	}

	jsonData, err := json.MarshalIndent(&entries, "", "    ")
	if err != nil {
		return nil, err
	}
	return jsonData, nil
}

// overrideProject performs override on project if matching override declaration is found
// in manifest. It will return the original project if no suitable match is found.
func overrideProject(jirix *jiri.X, project Project, projectOverrides map[string]Project, importOverrides map[string]Import) (Project, error) {

	key := string(project.Key())
	if remoteOverride, ok := importOverrides[key]; ok {
		project.Revision = remoteOverride.Revision
		if _, ok := projectOverrides[key]; ok {
			// It's not allowed to have both import override and project override
			// on same project.
			return project, fmt.Errorf("detected both import and project overrides on project \"%s:%s\", which is not allowed", project.Name, project.Remote)
		}
	} else if projectOverride, ok := projectOverrides[key]; ok {
		project.update(&projectOverride)
	}
	return project, nil
}

// overrideImport performs override on remote import if matching override declaration is found
// in manifest. It will return the original remote import if no suitable match is found
func overrideImport(jirix *jiri.X, remote Import, projectOverrides map[string]Project, importOverrides map[string]Import) (Import, error) {
	key := string(remote.ProjectKey())
	if _, ok := projectOverrides[key]; ok {
		return remote, fmt.Errorf("project override \"%s:%s\" cannot be used to override an import", remote.Name, remote.Remote)
	}
	if importOverride, ok := importOverrides[key]; ok {
		remote.update(&importOverride)
	}
	return remote, nil
}

func cacheDirPathFromRemote(jirix *jiri.X, remote string) (string, error) {
	if jirix.Cache != "" {
		url, err := url.Parse(remote)
		if err != nil {
			return "", err
		}
		dirname := url.Host + strings.Replace(strings.Replace(url.Path, "-", "--", -1), "/", "-", -1)
		referenceDir := filepath.Join(jirix.Cache, dirname)
		if jirix.UsePartialClone(remote) {
			referenceDir = filepath.Join(jirix.Cache, "partial", dirname)
		}
		return referenceDir, nil
	}
	return "", nil
}

// CacheDirPath returns a generated path to a directory that can be used as a reference repo
// for the given project.
func (p *Project) CacheDirPath(jirix *jiri.X) (string, error) {
	return cacheDirPathFromRemote(jirix, p.Remote)

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

func (p *Project) setupDefaultPushTarget(jirix *jiri.X) error {
	if p.GerritHost == "" {
		// Skip projects w/o gerrit host
		return nil
	}
	scm := gitutil.New(jirix, gitutil.RootDirOpt(p.Path))
	if err := scm.Config("--get", "remote.origin.push"); err != nil {
		// remote.origin.push does not exist.
		if err := scm.Config("remote.origin.push", "HEAD:refs/for/master"); err != nil {
			return fmt.Errorf("not able to set remote.origin.push for project %s(%s) due to error: %v", p.Name, p.Path, err)
		}
	}
	if err := scm.Config("--get", "push.default"); err != nil {
		// push.default does not exist.
		if err := scm.Config("push.default", "nothing"); err != nil {
			return fmt.Errorf("not able to set push.default for project %s(%s) due to error: %v", p.Name, p.Path, err)
		}
	}
	jirix.Logger.Debugf("set remote.origin.push to \"HEAD:refs/for/master\" for project %s(%s)", p.Name, p.Path)
	return nil
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
func CreateSnapshot(jirix *jiri.X, file string, hooks Hooks, pkgs Packages, localManifest bool, submodules bool) error {
	jirix.TimerPush("create snapshot")
	defer jirix.TimerPop()

	// Create a new Manifest with a Jiri version and current attributes
	// pinned to each snapshot
	manifest := Manifest{
		Version:    ManifestVersion,
		Attributes: jirix.FetchingAttrs,
	}

	// Add all local projects to manifest.
	localProjects, err := LocalProjects(jirix, FullScan)
	if err != nil {
		return err
	}

	if submodules {
		_, treeRoot, err := GenerateSubmoduleTree(jirix, localProjects)
		if err != nil {
			return err
		}
		// Remove dropped projects
		for _, project := range localProjects {
			if _, ok := treeRoot.Dropped[project.Key()]; ok {
				delete(localProjects, project.Key())
			}
		}
	}

	for _, project := range localProjects {
		manifest.Projects = append(manifest.Projects, project)
	}

	if hooks == nil || pkgs == nil {
		if _, tmpHooks, tmpPkgs, err := LoadManifestFile(jirix, jirix.JiriManifestFile(), localProjects, localManifest); err != nil {
			return err
		} else {
			if hooks == nil {
				hooks = tmpHooks
			}
			if pkgs == nil {
				pkgs = tmpPkgs
			}
		}
	}

	// Skip hooks for submodules
	if !submodules {
		for _, hook := range hooks {
			manifest.Hooks = append(manifest.Hooks, hook)
		}
	}
	for _, pack := range pkgs {
		manifest.Packages = append(manifest.Packages, pack)
	}

	return manifest.ToFile(jirix, file)
}

// CheckoutSnapshot updates project state to the state specified in the given
// snapshot file.  Note that the snapshot file must not contain remote imports.
func CheckoutSnapshot(jirix *jiri.X, snapshot string, gc, runHooks, fetchPkgs bool, runHookTimeout, fetchTimeout uint) error {
	jirix.UsingSnapshot = true
	// Find all local projects.
	scanMode := FastScan
	if gc {
		scanMode = FullScan
	}
	localProjects, err := LocalProjects(jirix, scanMode)
	if err != nil {
		return err
	}
	remoteProjects, hooks, pkgs, err := LoadSnapshotFile(jirix, snapshot)
	if err != nil {
		return err
	}
	if err := updateProjects(jirix, localProjects, remoteProjects, hooks, pkgs, gc, runHookTimeout, fetchTimeout, false /*rebaseTracked*/, false /*rebaseUntracked*/, false /*rebaseAll*/, true /*snapshot*/, runHooks, fetchPkgs); err != nil {
		return err
	}
	return WriteUpdateHistorySnapshot(jirix, snapshot, hooks, pkgs, false)
}

// LoadSnapshotFile loads the specified snapshot manifest.  If the snapshot
// manifest contains a remote import, an error will be returned.
func LoadSnapshotFile(jirix *jiri.X, snapshot string) (Projects, Hooks, Packages, error) {
	// Snapshot files already have pinned Project revisions and Package instance IDs.
	// They will cause conflicts with current lockfiles. Disable the lockfile for now.
	enableLockfile := jirix.LockfileEnabled
	jirix.LockfileEnabled = false
	defer func() {
		jirix.LockfileEnabled = enableLockfile
	}()
	if _, err := os.Stat(snapshot); err != nil {
		if !os.IsNotExist(err) {
			return nil, nil, nil, fmtError(err)
		}
		u, err := url.ParseRequestURI(snapshot)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%q is neither a URL nor a valid file path", snapshot)
		}
		jirix.Logger.Infof("Getting snapshot from URL %q", u)
		resp, err := http.Get(u.String())
		if err != nil {
			return nil, nil, nil, fmt.Errorf("Error getting snapshot from URL %q: %v", u, err)
		}
		defer resp.Body.Close()
		tmpFile, err := ioutil.TempFile("", "snapshot")
		if err != nil {
			return nil, nil, nil, fmt.Errorf("Error creating tmp file: %v", err)
		}
		snapshot = tmpFile.Name()
		defer os.Remove(snapshot)
		if _, err = io.Copy(tmpFile, resp.Body); err != nil {
			return nil, nil, nil, fmt.Errorf("Error writing to tmp file: %v", err)
		}

	}

	m, err := ManifestFromFile(jirix, snapshot)
	if err != nil {
		return nil, nil, nil, err
	}
	if ManifestVersion != m.Version {
		return nil, nil, nil, errVersionMismatch
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

// rewriteAndNormalizeRemote rewrites sso:// prefixed remotes and removes the
// scheme (e.g. https://) from the remote.
func rewriteAndNormalizeRemote(remote string) string {
	if strings.HasPrefix(remote, "sso://") {
		remote = ssoRe.ReplaceAllString(remote, "https://$1.googlesource.com/")
	}
	u, err := url.Parse(remote)
	if err != nil {
		// If remote isn't parseable don't try to remove schema
		return remote
	}
	return strings.TrimPrefix(remote, u.Scheme)
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
		snapshotProjects, _, _, err := LoadSnapshotFile(jirix, latestSnapshot)
		if err != nil {
			if err == errVersionMismatch {
				return loadLocalProjectsSlow(jirix)
			}
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

	return loadLocalProjectsSlow(jirix)
}

func loadLocalProjectsSlow(jirix *jiri.X) (Projects, error) {
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
	for key := range localProjects {
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
			for localKey := range localKeysNotInRemote {
				localProject := localProjects[localKey]
				if localProject.Path == remoteProject.Path &&
					(localProject.Name == remoteProject.Name || rewriteAndNormalizeRemote(localProject.Remote) == rewriteAndNormalizeRemote(remoteProject.Remote)) {
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

func loadManifestFiles(jirix *jiri.X, manifestFiles []string, localManifest bool) (Projects, Packages, error) {
	localProjects, err := LocalProjects(jirix, FastScan)
	if err != nil {
		return nil, nil, err
	}
	jirix.Logger.Debugf("Print local projects: ")
	for _, v := range localProjects {
		jirix.Logger.Debugf("entry: %+v", v)
	}
	jirix.Logger.Debugf("Print local projects ends")
	allProjects := make(Projects)
	allPkgs := make(Packages)

	addProject := func(projects Projects) error {
		for _, project := range projects {
			if existingProject, ok := allProjects[project.Key()]; ok {
				if !reflect.DeepEqual(existingProject, project) {
					return fmt.Errorf("project: %v conflicts with project: %v", existingProject, project)
				}
				continue
			} else {
				allProjects[project.Key()] = project
			}
		}
		return nil
	}

	addPkg := func(pkgs Packages) error {
		for _, pkg := range pkgs {
			if existingPkg, ok := allPkgs[pkg.Key()]; ok {
				if !reflect.DeepEqual(existingPkg, pkg) {
					return fmt.Errorf("package: %v conflicts with package: %v", existingPkg, pkg)
				}
				continue
			} else {
				allPkgs[pkg.Key()] = pkg
			}
		}
		return nil
	}

	for _, manifestFile := range manifestFiles {
		remoteProjects, _, pkgs, err := LoadManifestFile(jirix, manifestFile, localProjects, localManifest)
		if err != nil {
			return nil, nil, err
		}
		if err := addProject(remoteProjects); err != nil {
			return nil, nil, err
		}
		if err := addPkg(pkgs); err != nil {
			return nil, nil, err
		}
	}

	return allProjects, allPkgs, nil
}

func writeLockFile(jirix *jiri.X, lockfilePath string, projectLocks ProjectLocks, pkgLocks PackageLocks) error {
	data, err := MarshalLockEntries(projectLocks, pkgLocks)
	if err != nil {
		return err
	}
	jirix.Logger.Debugf("Generated jiri lockfile content: \n%v", string(data))

	tempFile, err := ioutil.TempFile(path.Dir(lockfilePath), "jirilock.*")
	if err != nil {
		return err
	}
	defer tempFile.Close()
	defer os.Remove(tempFile.Name())
	if _, err := tempFile.Write(data); err != nil {
		return errors.New("I/O error while writing jiri lockfile")
	}
	tempFile.Close()
	if err := os.Rename(tempFile.Name(), lockfilePath); err != nil {
		return err
	}

	return nil
}

// HostnameAllowed determines if hostname is allowed under reference.
// This function allows a single prefix '*' for wildcard matching E.g.
// "*.google.com" will match "fuchsia.google.com" but does not match
// "google.com".
func HostnameAllowed(reference, hostname string) bool {
	if strings.Count(reference, "*") > 1 || (strings.Count(reference, "*") == 1 && reference[0] != '*') {
		return false
	}
	if !strings.HasPrefix(reference, "*") {
		return reference == hostname
	}
	reference = reference[1:]
	i := len(reference) - 1
	j := len(hostname) - 1
	for i >= 0 && j >= 0 {
		if hostname[j] != reference[i] {
			return false
		}
		i--
		j--
	}
	if i >= 0 {
		return false
	}
	return true
}

// CheckProjectsHostnames checks if the hostname of every project is allowed
// under allowList. If allowList is empty, the check is skipped.
func CheckProjectsHostnames(projects Projects, allowList []string) error {
	if len(allowList) > 0 {
		for _, item := range allowList {
			if strings.Count(item, "*") > 1 || (strings.Count(item, "*") == 1 && item[0] != '*') {
				return fmt.Errorf("failed to process %q. Only a single * at the beginning of a hostname is supported", item)
			}
		}
		for _, proj := range projects {
			projURL, err := url.Parse(proj.Remote)
			if err != nil {
				return fmt.Errorf("URL of project %q cannot be parsed due to error: %v", proj.Name, err)
			}
			remoteHost := projURL.Hostname()
			allowed := false
			for _, item := range allowList {
				if HostnameAllowed(item, remoteHost) {
					allowed = true
					break
				}
			}
			if !allowed {
				err := fmt.Errorf("hostname: %s in project %s is not allowed", remoteHost, proj.Name)
				return err
			}
		}
	}
	return nil
}

func getChangedLocksPkgs(ePkgLocks PackageLocks, pkgs Packages) (PackageLocks, Packages, error) {
	retPkgs := make(Packages)
	retLocks := make(PackageLocks)
	// lockMap is the mapping between package name -> PackageLock
	lockMap := make(map[string][]PackageLock)
	// pkgMap is the mappting between package name -> Package
	pkgMap := make(map[string][]Package)
	for _, v := range ePkgLocks {
		lockMap[v.PackageName] = append(lockMap[v.PackageName], v)
	}
	for _, v := range pkgs {
		plats, err := v.GetPlatforms()
		if err != nil {
			return nil, nil, err
		}
		expandedNames, err := cipd.Expand(v.Name, plats)
		if err != nil {
			return nil, nil, err
		}
		for _, expexpandedName := range expandedNames {
			pkgMap[expexpandedName] = append(pkgMap[expexpandedName], v)
		}
	}

	// If items exist in lockfile but not in manifest, return
	// an error to force full resolve.
	for k := range lockMap {
		if _, ok := pkgMap[k]; !ok {
			return ePkgLocks, pkgs, errors.New("existing lockfile does not match manifest")
		}
	}
	// For packages do not show up in lockfile,
	// force resolve on them as they may have been
	// changed from using instance-ids to using version tags.
	for k := range pkgMap {
		if _, ok := lockMap[k]; !ok {
			for _, v := range pkgMap[k] {
				retPkgs[v.Key()] = v
			}
		}
	}
	versionMatch := func(lockSlice []PackageLock, pkgSlice []Package) bool {
		lockVersions := make(map[string]struct{})
		pkgVersions := make(map[string]struct{})
		for _, v := range lockSlice {
			lockVersions[v.VersionTag] = struct{}{}
		}
		for _, v := range pkgSlice {
			pkgVersions[v.Version] = struct{}{}
		}
		return reflect.DeepEqual(lockVersions, pkgVersions)
	}
	for k := range lockMap {
		if !versionMatch(lockMap[k], pkgMap[k]) {
			for _, v := range pkgMap[k] {
				retPkgs[v.Key()] = v
			}
			for _, v := range lockMap[k] {
				retLocks[v.Key()] = v
			}
		}
	}
	return retLocks, retPkgs, nil
}

// GenerateJiriLockFile generates jiri lockfile to lockFilePath using
// manifests in manifestFiles slice.
func GenerateJiriLockFile(jirix *jiri.X, manifestFiles []string, resolveConfig ResolveConfig) error {
	jirix.Logger.Debugf("Generate jiri lockfile for manifests %v to %q", manifestFiles, resolveConfig.LockFilePath())

	resolveLocks := func(jirix *jiri.X, manifestFiles []string, localManifest bool, resolveFully bool, ePkgLocks PackageLocks) (projectLocks ProjectLocks, pkgLocks PackageLocks, err error) {
		projects, pkgs, err := loadManifestFiles(jirix, manifestFiles, localManifest)
		if err != nil {
			return nil, nil, err
		}
		// Check hostnames of projects.
		if err := CheckProjectsHostnames(projects, resolveConfig.HostnameAllowList()); err != nil {
			return nil, nil, err
		}
		if resolveConfig.EnableProjectLock() {
			// For project locks, there is no differences between
			// full or partial resolve.
			projectLocks, err = resolveProjectLocks(jirix, projects)
			if err != nil {
				return
			}
		}
		if resolveConfig.EnablePackageLock() {
			var pkgsToProcess Packages
			var locksToProcess PackageLocks
			if resolveFully {
				pkgsToProcess = pkgs
			} else {
				if locksToProcess, pkgsToProcess, err = getChangedLocksPkgs(ePkgLocks, pkgs); err != nil {
					jirix.Logger.Warningf("%v, fallback to full resolve", err)
					pkgsToProcess = pkgs
					resolveFully = true
				}
			}
			if !resolveConfig.AllowFloatingRefs() {
				pkgsForRefCheck := make(map[cipd.PackageInstance]bool)
				pkgsPlatformMap := make(map[cipd.PackageInstance][]cipd.Platform)
				for _, v := range pkgsToProcess {
					pkgInstance := cipd.PackageInstance{
						PackageName: v.Name,
						VersionTag:  v.Version,
					}
					pkgsForRefCheck[pkgInstance] = false
					plats, err := v.GetPlatforms()
					if err != nil {
						return nil, nil, err
					}
					pkgsPlatformMap[pkgInstance] = plats
				}
				if err := cipd.CheckFloatingRefs(jirix, pkgsForRefCheck, pkgsPlatformMap); err != nil {
					return nil, nil, err
				}
				for k, v := range pkgsForRefCheck {
					var errBuf bytes.Buffer
					if v {
						errBuf.WriteString(fmt.Sprintf("package %q used floating ref %q, which is not allowed\n", k.PackageName, k.VersionTag))
					}
					if errBuf.Len() != 0 {
						errBuf.Truncate(errBuf.Len() - 1)
						return nil, nil, errors.New(errBuf.String())
					}
				}
			}
			pkgsWithMultiVersionsMap := make(map[string]map[string]bool)
			for _, v := range pkgsToProcess {
				versionMap := make(map[string]bool)
				if _, ok := pkgsWithMultiVersionsMap[v.Name]; ok {
					versionMap = pkgsWithMultiVersionsMap[v.Name]
				}
				versionMap[v.Version] = true
				pkgsWithMultiVersionsMap[v.Name] = versionMap
			}
			for k := range pkgsWithMultiVersionsMap {
				if len(pkgsWithMultiVersionsMap[k]) <= 1 {
					delete(pkgsWithMultiVersionsMap, k)
				}
			}
			pkgLocks, err = resolvePackageLocks(jirix, pkgsToProcess)
			if err != nil {
				return
			}
			// Merge with existing locks.
			if !resolveFully {
				for k := range locksToProcess {
					delete(ePkgLocks, k)
				}
				for k, v := range ePkgLocks {
					pkgLocks[k] = v
				}
			}
			// sort the keys of pkgs to avoid nondeterministic output.
			pkgKeys := make([]PackageKey, 0)
			for k := range pkgs {
				pkgKeys = append(pkgKeys, k)
			}
			sort.Slice(pkgKeys, func(i, j int) bool {
				return pkgKeys[i] < pkgKeys[j]
			})
			for _, k := range pkgKeys {
				v := pkgs[k]
				if _, ok := pkgsWithMultiVersionsMap[v.Name]; ok {
					plats, err := v.GetPlatforms()
					if err != nil {
						return nil, nil, err
					}
					expandedNames, err := cipd.Expand(v.Name, plats)
					if err != nil {
						return nil, nil, err
					}
					for _, expandedName := range expandedNames {
						lockKey := PackageLockKey(expandedName + KeySeparator + v.Version)
						lockEntry, ok := pkgLocks[lockKey]
						if !ok {
							jirix.Logger.Errorf("lock key not found in pkgLocks: %v, package: %+v", lockKey, v)
							return nil, nil, err
						}
						lockEntry.LocalPath = v.Path
						pkgLocks[lockKey] = lockEntry
					}
				}
			}
		}
		return
	}

	resolveFully := false
	var ePkgLocks PackageLocks
	// Read existing lockfile.
	jsonData, err := ioutil.ReadFile(resolveConfig.LockFilePath())
	if err == nil {
		_, ePkgLocks, err = UnmarshalLockEntries(jsonData)
	}
	if err != nil {
		resolveFully = true
	}
	resolveFully = resolveFully || resolveConfig.FullResolve()

	projectLocks, pkgLocks, err := resolveLocks(jirix, manifestFiles, resolveConfig.LocalManifest(), resolveFully, ePkgLocks)
	if err != nil {
		return err
	}
	return writeLockFile(jirix, resolveConfig.LockFilePath(), projectLocks, pkgLocks)
}

// UpdateUniverse updates all local projects and tools to match the remote
// counterparts identified in the manifest. Optionally, the 'gc' flag can be
// used to indicate that local projects that no longer exist remotely should be
// removed.
func UpdateUniverse(jirix *jiri.X, gc, localManifest, rebaseTracked, rebaseUntracked, rebaseAll, runHooks, fetchPkgs bool, runHookTimeout, fetchTimeout uint) (e error) {
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
		remoteProjects, hooks, pkgs, err := LoadUpdatedManifest(jirix, localProjects, localManifest)
		MatchLocalWithRemote(localProjects, remoteProjects)

		if err != nil {
			return err
		}

		// Actually update the projects.
		return updateProjects(jirix, localProjects, remoteProjects, hooks, pkgs, gc, runHookTimeout, fetchTimeout, rebaseTracked, rebaseUntracked, rebaseAll, false /*snapshot*/, runHooks, fetchPkgs)
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

// WriteUpdateHistoryLog creates a log file of the current update process.
func WriteUpdateHistoryLog(jirix *jiri.X) error {
	logFile := filepath.Join(jirix.UpdateHistoryLogDir(), time.Now().Format((time.RFC3339)))
	if err := os.MkdirAll(filepath.Dir(logFile), 0755); err != nil {
		return fmtError(err)
	}
	if err := jirix.Logger.WriteLogToFile(logFile); err != nil {
		return err
	}

	latestLink, secondLatestLink := jirix.UpdateHistoryLogLatestLink(), jirix.UpdateHistoryLogSecondLatestLink()

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

	// Point the "latest" update history symlink to the new log file.  Try
	// to keep the symlink relative, to make it easy to move or copy the entire
	// update_history_log directory.
	if rel, err := filepath.Rel(filepath.Dir(latestLink), logFile); err == nil {
		logFile = rel
	}
	if err := os.RemoveAll(latestLink); err != nil {
		return fmtError(err)
	}
	return fmtError(os.Symlink(logFile, latestLink))
}

// WriteUpdateHistorySnapshot creates a snapshot of the current state of all
// projects and writes it to the update history directory.
func WriteUpdateHistorySnapshot(jirix *jiri.X, snapshotPath string, hooks Hooks, pkgs Packages, localManifest bool) error {
	snapshotFile := filepath.Join(jirix.UpdateHistoryDir(), time.Now().Format(time.RFC3339))
	if err := CreateSnapshot(jirix, snapshotFile, hooks, pkgs, localManifest, false); err != nil {
		return err
	}

	latestLink, secondLatestLink := jirix.UpdateHistoryLatestLink(), jirix.UpdateHistorySecondLatestLink()

	// If the "latest" hardlink exists, point the "second-latest" hardlink to its value.
	latestLinkExists, err := isFile(latestLink)
	if err != nil {
		return err
	}
	if latestLinkExists {
		if err := os.RemoveAll(secondLatestLink); err != nil {
			return fmtError(err)
		}
		if err := os.Link(latestLink, secondLatestLink); err != nil {
			return fmtError(err)
		}
	}

	// Point the "latest" update history hardlink to the new snapshot file.
	if err := os.RemoveAll(latestLink); err != nil {
		return fmtError(err)
	}
	return fmtError(os.Link(snapshotFile, latestLink))
}

// CleanupProjects restores the given jiri projects back to their detached
// heads, resets to the specified revision if there is one, and gets rid of
// all the local changes. If "cleanupBranches" is true, it will also delete all
// the non-master branches.
func CleanupProjects(jirix *jiri.X, localProjects Projects, cleanupBranches bool) (e error) {
	remoteProjects, _, _, err := LoadManifest(jirix)
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
	r := remote
	cachePath, err := project.CacheDirPath(jirix)
	if err != nil {
		return err
	}
	if cachePath != "" {
		r = cachePath
	}
	defer func() {
		if err := scm.SetRemoteUrl("origin", remote); err != nil {
			jirix.Logger.Errorf("failed to set remote back to %v for project %+v", remote, project)
		}
	}()
	if err := scm.SetRemoteUrl("origin", r); err != nil {
		return err
	}
	if project.HistoryDepth > 0 {
		if err := fetch(jirix, project.Path, "origin", gitutil.PruneOpt(true),
			gitutil.DepthOpt(project.HistoryDepth), gitutil.UpdateShallowOpt(true)); err != nil {
			return err
		}
	} else {
		if err := fetch(jirix, project.Path, "origin", gitutil.PruneOpt(true)); err != nil {
			return err
		}
	}
	return nil
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
	jirix.Logger.Debugf("Checkout %s to head revision %s failed, fallback to fetch: %v", project.Name, revision, err)
	if project.Revision != "" && project.Revision != "HEAD" {
		if err2 := git.FetchRefspec("origin", project.Revision); err2 != nil {
			return fmt.Errorf("error while fetching after failed to checkout revision %s for project %s (%s): %s\ncheckout error: %v", revision, project.Name, project.Path, err2, err)
		}
		return git.CheckoutBranch(revision, gitutil.DetachOpt(true), gitutil.ForceOpt(forceCheckout))
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

	if diff, err := scm.FilesWithUncommittedChanges(); err != nil {
		return fmt.Errorf("Cannot get uncommited changes for project %q: %s", project.Name, err)
	} else if len(diff) != 0 {
		msg := fmt.Sprintf("Project %s(%s) contains uncommited changes:", project.Name, relativePath)
		if jirix.Logger.LoggerLevel >= log.DebugLevel {
			for _, item := range diff {
				msg += "\n" + item
			}
		}
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

func updateOrCreateCache(jirix *jiri.X, dir, remote, branch, revision string, depth int) error {
	refspec := "+refs/heads/*:refs/heads/*"
	if depth > 0 {
		// Shallow cache, fetch only manifest tracked remote branch
		refspec = fmt.Sprintf("+refs/heads/%s:refs/heads/%s", branch, branch)
	}
	errCacheCorruption := errors.New("git cache corrupted")
	updateCache := func() error {
		// Test if git cache is intact
		var objectsDir string
		if jirix.UsePartialClone(remote) {
			// Partial clones do not use --bare so objects is in .git/
			objectsDir = filepath.Join(dir, ".git", "objects")
		} else {
			objectsDir = filepath.Join(dir, "objects")
		}
		if _, err := os.Stat(objectsDir); err != nil {
			jirix.Logger.Warningf("could not access objects directory under git cache directory %q due to error: %v", dir, err)
			return errCacheCorruption
		}
		scm := gitutil.New(jirix, gitutil.RootDirOpt(dir))
		if jirix.OffloadPackfiles {
			if err := scm.Config("fetch.uriprotocols", "https"); err != nil {
				return err
			}
		}
		if err := scm.Config("--remove-section", "remote.origin"); err != nil {
			jirix.Logger.Warningf("purge git config failed under git cache directory %q due to error: %v", dir, err)
			return errCacheCorruption
		}
		if err := scm.Config("remote.origin.url", remote); err != nil {
			jirix.Logger.Warningf("set remote.origin.url failed under git cache directory %q due to error: %v", dir, err)
			return errCacheCorruption
		}
		if err := scm.Config("--replace-all", "remote.origin.fetch", refspec); err != nil {
			jirix.Logger.Warningf("set remote.origin.fetch failed under git cache directory %q due to error: %v", dir, err)
			return errCacheCorruption
		}
		if jirix.UsePartialClone(remote) {
			if err := scm.AddOrReplacePartialRemote("origin", remote); err != nil {
				return err
			}
		}
		msg := fmt.Sprintf("Updating cache: %q", dir)
		task := jirix.Logger.AddTaskMsg(msg)
		defer task.Done()
		t := jirix.Logger.TrackTime(msg)
		defer t.Done()
		// Cache already present, update it
		// TODO : update this after implementing FetchAll using g
		if scm.IsRevAvailable(jirix, remote, revision) {
			jirix.Logger.Debugf("%s(%s) cache up-to-date; skipping\n", remote, dir)
			return nil
		}
		// We need to explicitly specify the ref for fetch to update in case
		// the cache was created with a previous version and uses "refs/*"
		if err := retry.Function(jirix, func() error {
			// Use --update-head-ok here to force fetch to update the current branch.
			// This is used in the case of a partial clone having a working tree
			// checked out in the cache.
			if err := scm.FetchRefspec("origin", refspec,
				gitutil.DepthOpt(depth), gitutil.PruneOpt(true), gitutil.UpdateShallowOpt(true), gitutil.UpdateHeadOkOpt(true)); err != nil {
				return err
			}
			if jirix.UsePartialClone(remote) {
				if err := scm.CheckoutBranch(revision, gitutil.DetachOpt(true), gitutil.ForceOpt(true)); err != nil {
					return err
				}
			}
			return nil
		}, fmt.Sprintf("Fetching for %s:%s", dir, refspec),
			retry.AttemptsOpt(jirix.Attempts)); err != nil {
			return err
		}
		return nil
	}

	createCache := func() error {
		// Create cache
		// TODO : If we in future need to support two projects with same remote url,
		// one with shallow checkout and one with full, we should create two caches
		msg := fmt.Sprintf("Creating cache: %q", dir)
		task := jirix.Logger.AddTaskMsg(msg)
		defer task.Done()
		t := jirix.Logger.TrackTime(msg)
		defer t.Done()

		opts := []gitutil.CloneOpt{gitutil.DepthOpt(depth)}
		if jirix.UsePartialClone(remote) {
			opts = append(opts, gitutil.NoCheckoutOpt(true), gitutil.OmitBlobsOpt(true))
		} else {
			opts = append(opts, gitutil.BareOpt(true))
		}
		if jirix.OffloadPackfiles {
			opts = append(opts, gitutil.OffloadPackfilesOpt(true))
		}
		if err := gitutil.New(jirix).Clone(remote, dir, opts...); err != nil {
			return err
		}

		git := gitutil.New(jirix, gitutil.RootDirOpt(dir))
		if jirix.OffloadPackfiles {
			if err := git.Config("fetch.uriprotocols", "https"); err != nil {
				return err
			}
		}
		if jirix.UsePartialClone(remote) {
			if err := git.CheckoutBranch(revision, gitutil.DetachOpt(true), gitutil.ForceOpt(true)); err != nil {
				return err
			}
		}
		// We need to explicitly specify the ref for fetch to update the bare
		// repository.
		if err := git.Config("remote.origin.fetch", refspec); err != nil {
			return err
		}
		if err := git.Config("uploadpack.allowFilter", "true"); err != nil {
			return err
		}
		return updateCache()
	}

	if isPathDir(dir) {
		if err := updateCache(); err != nil {
			if err == errCacheCorruption {
				jirix.Logger.Warningf("Updating git cache %q failed due to cache corruption, cache will be cleared", dir)
				if err := os.RemoveAll(dir); err != nil {
					return fmt.Errorf("failed to clear cache dir %q due to error: %v", dir, err)
				}
				return createCache()
			}
			return err
		}
		return nil
	}

	return createCache()
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
	processingPath := make(map[string]*sync.Mutex)
	fetchLimit := make(chan struct{}, jirix.Jobs)
	for _, project := range remoteProjects {
		if cacheDirPath, err := project.CacheDirPath(jirix); err == nil {
			if processingPath[cacheDirPath] == nil {
				processingPath[cacheDirPath] = &sync.Mutex{}
			}
			if err := project.fillDefaults(); err != nil {
				errs <- err
				continue
			}
			wg.Add(1)
			fetchLimit <- struct{}{}
			go func(dir, remote string, depth int, branch, revision string, cacheMutex *sync.Mutex) {
				cacheMutex.Lock()
				defer func() { <-fetchLimit }()
				defer wg.Done()
				defer cacheMutex.Unlock()
				remote = rewriteRemote(jirix, remote)
				if err := updateOrCreateCache(jirix, dir, remote, branch, revision, depth); err != nil {
					errs <- err
					return
				}
			}(cacheDirPath, project.Remote, project.HistoryDepth, project.RemoteBranch, project.Revision, processingPath[cacheDirPath])
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

// FilterOptionalProjectsPackages removes projects and packages in place if the Optional field is true and
// attributes in attrs does not match the Attributes field. Currently "match" means the intersection of
// both attributes is not empty.
func FilterOptionalProjectsPackages(jirix *jiri.X, attrs string, projects Projects, pkgs Packages) error {
	allowedAttrs := newAttributes(attrs)

	for k, v := range projects {
		if !v.ComputedAttributes.IsEmpty() {
			if v.ComputedAttributes == nil {
				return fmt.Errorf("project %+v should have valid ComputedAttributes, but it is nil", v)
			}
			if !allowedAttrs.Match(v.ComputedAttributes) {
				jirix.Logger.Debugf("project %q is filtered (%s:%s)", v.Name, v.ComputedAttributes, allowedAttrs)
				delete(projects, k)
			}
		}
	}

	for k, v := range pkgs {
		if !v.ComputedAttributes.IsEmpty() {
			if v.ComputedAttributes == nil {
				return fmt.Errorf("package %+v should have valid ComputedAttributes, but it is nil", v)
			}
			if !allowedAttrs.Match(v.ComputedAttributes) {
				jirix.Logger.Debugf("package %q is filtered (%s:%s)", v.Name, v.ComputedAttributes, allowedAttrs)
				delete(pkgs, k)
			}
		}
	}
	return nil
}

func updateProjects(jirix *jiri.X, localProjects, remoteProjects Projects, hooks Hooks, pkgs Packages, gc bool, runHookTimeout, fetchTimeout uint, rebaseTracked, rebaseUntracked, rebaseAll, snapshot, shouldRunHooks, shouldFetchPkgs bool) error {
	jirix.TimerPush("update projects")
	defer jirix.TimerPop()

	packageFetched := false
	hookRun := false
	defer func() {
		if shouldFetchPkgs && !packageFetched {
			jirix.Logger.Infof("Jiri packages are not fetched due to fatal errors when updating projects.")
		}
		if shouldRunHooks && !hookRun {
			jirix.Logger.Infof("Jiri hooks are not run due to fatal errors when updating projects or packages")
		}
	}()

	// filter optional projects
	if err := FilterOptionalProjectsPackages(jirix, jirix.FetchingAttrs, remoteProjects, pkgs); err != nil {
		return err
	}

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
	var wg sync.WaitGroup
	for _, project := range remoteProjects {
		wg.Add(1)
		go func(jirix *jiri.X, project Project) {
			defer wg.Done()
			if !(project.LocalConfig.Ignore || project.LocalConfig.NoUpdate) {
				project.writeJiriRevisionFiles(jirix)
				if err := project.setupDefaultPushTarget(jirix); err != nil {
					jirix.Logger.Debugf("set up default push target failed due to error: %v", err)
				}
			}
		}(jirix, project)
	}
	wg.Wait()
	jirix.TimerPop()

	jirix.TimerPush("jiri project flag files")
	if err := WriteProjectFlags(jirix, remoteProjects); err != nil {
		jirix.Logger.Errorf("failures in write jiri project flag files: %v", err)
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
				if jirix.Logger.LoggerLevel >= log.DebugLevel {
					msg = fmt.Sprintf("%s (%s: %s)", msg, jirix.Color.Yellow("Has changes"), p.Changes)
				} else {
					msg = fmt.Sprintf("%s (%s)", msg, jirix.Color.Yellow("Has changes"))
				}
			}
			if !p.IsOnJiriHead {
				msg = fmt.Sprintf("%s (%s)", msg, jirix.Color.Yellow("Not on JIRI_HEAD"))
			}
		}
		jirix.Logger.Warningf("%s\n\nTo force an update to JIRI_HEAD, you may run 'jiri runp 'git checkout JIRI_HEAD'", msg)
	}

	if shouldFetchPkgs {
		packageFetched = true
		if len(pkgs) > 0 {
			if err := FetchPackages(jirix, pkgs, fetchTimeout); err != nil {
				return err
			}
		}
	}

	if shouldRunHooks {
		hookRun = true
		if err := RunHooks(jirix, hooks, runHookTimeout); err != nil {
			return err
		}
	}

	if !jirix.KeepGitHooks {
		return applyGitHooks(jirix, ops)
	}
	jirix.Logger.Warningf("Git hooks are not updated. If you would like to update git hooks for all projects, please run 'jiri init -keep-git-hooks=false'.")
	return nil
}

type ProjectStatus struct {
	Project      Project
	HasChanges   bool
	IsOnJiriHead bool
	Changes      string
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
				diff, err := scm.FilesWithUncommittedChanges()
				if err != nil {
					errs <- fmt.Errorf("Cannot get uncommited changes for project %q: %s", project.Name, err)
					continue
				}
				uncommitted := false
				var changes bytes.Buffer
				if len(diff) != 0 {
					uncommitted = true
					for _, item := range diff {
						changes.WriteString(item + "\n")
					}
					changes.Truncate(changes.Len() - 1)
				}

				isOnJiriHead, err := project.IsOnJiriHead(jirix)
				if err != nil {
					errs <- err
					continue
				}
				if uncommitted || !isOnJiriHead {
					projectStatuses <- ProjectStatus{project, uncommitted, isOnJiriHead, changes.String()}
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

func writeAttributesJSON(jirix *jiri.X) error {
	attrs := make([]string, 0)
	for k := range newAttributes(jirix.FetchingAttrs) {
		attrs = append(attrs, k)
	}
	jsonData, err := json.MarshalIndent(&attrs, "", "    ")
	if err != nil {
		return err
	}
	jsonFile := filepath.Join(jirix.RootMetaDir(), jiri.AttrsJSON)
	if err := safeWriteFile(jirix, jsonFile, jsonData); err != nil {
		return err
	}
	jirix.Logger.Debugf("package optional attributes written to %s", jsonFile)
	return nil
}

type ProjectTree struct {
	project  *Project
	Children map[string]*ProjectTree
}

type ProjectTreeRoot struct {
	Root    *ProjectTree
	Dropped Projects
}

func GenerateSubmoduleTree(jirix *jiri.X, projects Projects) ([]Project, *ProjectTreeRoot, error) {
	projEntries := make([]Project, len(projects))

	// relativize the paths and copy projects from map to slice for sorting.
	i := 0
	for _, v := range projects {
		relPath, err := makePathRel(jirix.Root, v.Path)
		if err != nil {
			return nil, nil, err
		}
		v.Path = relPath
		projEntries[i] = v
		i++
	}
	sort.Slice(projEntries, func(i, j int) bool {
		return string(projEntries[i].Key()) < string(projEntries[j].Key())
	})

	// Create path prefix tree to collect all nested projects
	root := ProjectTree{nil, make(map[string]*ProjectTree)}
	treeRoot := ProjectTreeRoot{&root, make(Projects)}
	for _, v := range projEntries {
		if err := treeRoot.add(jirix, v); err != nil {
			return nil, nil, err
		}
	}
	return projEntries, &treeRoot, nil
}

func (p *ProjectTreeRoot) add(jirix *jiri.X, proj Project) error {
	if p == nil || p.Root == nil {
		return errors.New("add called with nil root pointer")
	}

	if proj.Path == "." || proj.Path == "" || proj.Path == string(filepath.Separator) {
		// Skip fuchsia.git project
		p.Dropped[proj.Key()] = proj
		return nil
	}

	// git submodule does not support one submodule to be placed under the path
	// of another submodule, therefore, it is necessary to detect nested
	// projects in jiri manifests and drop them from gitmodules file.
	//
	// The nested project detection is based on only 1 rule:
	// If the path of project A (pathA) is the parent directory of project B,
	// project B will be considered as nested under project A. It will be recorded
	// in "dropped" map.
	//
	// Due to the introduction of fuchsia.git, based on the rule above, all
	// other projects will be considered as nested project under fuchsia.git,
	// therefore, fuchsia.git is excluded in this detection process.
	//
	// The detection algorithm works in following ways:
	//
	// Assuming we have two project: "projA" and "projB", "projA" is located at
	// "$JIRI_ROOT/a" and projB is located as "$JIRI_ROOT/b/c".
	// The projectTree will look like the following chart:
	//
	//                   a    +-------+
	//               +--------+ projA |
	//               |        +-------+
	// +---------+   |
	// |nil(root)+---+
	// +---------+   |
	//               |   b    +-------+   c   +-------+
	//               +--------+  nil  +-------+ projB |
	//                        +-------+       +-------+
	//
	// The text inside each block represents the projectTree.project field,
	// each edge represents a key of projectTree.children field.
	//
	// Assuming we adds project "projC" whose path is "$JIRI_ROOT/a/d", it will
	// be dropped as the children of root already have key "a" and
	// children["a"].project is not pointed to nil, which means "projC" is
	// nested under "projA".
	//
	// Assuming we adds project "projD" whose path is "$JIRI_ROOT/d", it will
	// be added successfully since root.children does not have key "d" yet,
	// which means "projD" is not nested under any known project and no project
	// is currently nested under "projD" yet.
	//
	// Assuming we adds project "projE" whose path is "$JIRI_ROOT/b", it will
	// be added successfully and "projB" will be dropped. The reason is that
	// root.children["b"].project is nil but root.children["b"].children is not
	// empty, so any projects that can be reached from root.children["b"]
	// should be dropped as they are nested under "projE".
	elmts := strings.Split(proj.Path, string(filepath.Separator))
	pin := p.Root
	for i := 0; i < len(elmts); i++ {
		if child, ok := pin.Children[elmts[i]]; ok {
			if child.project != nil {
				// proj is nested under next.project, drop proj
				jirix.Logger.Debugf("project %q:%q nested under project %q:%q", proj.Path, proj.Remote, proj.Path, child.project.Remote)
				p.Dropped[proj.Key()] = proj
				return nil
			}
			pin = child
		} else {
			child = &ProjectTree{nil, make(map[string]*ProjectTree)}
			pin.Children[elmts[i]] = child
			pin = child
		}
	}
	if len(pin.Children) != 0 {
		// There is one or more project nested under proj.
		jirix.Logger.Debugf("following project nested under project %q:%q", proj.Path, proj.Remote)
		if err := p.prune(jirix, pin); err != nil {
			return err
		}
		jirix.Logger.Debugf("\n")
	}
	pin.project = &proj
	return nil
}

func (p *ProjectTreeRoot) prune(jirix *jiri.X, node *ProjectTree) error {
	// Looking for projects nested under node using BFS
	workList := make([]*ProjectTree, 0)
	workList = append(workList, node)

	for len(workList) > 0 {
		item := workList[0]
		if item == nil {
			return errors.New("purgeLeaves encountered a nil node")
		}
		workList = workList[1:]
		if item.project != nil {
			p.Dropped[item.project.Key()] = *item.project
			jirix.Logger.Debugf("\tnested project %q:%q", item.project.Path, item.project.Remote)
		}
		for _, v := range item.Children {
			workList = append(workList, v)
		}
	}

	// Purge leaves under node
	node.Children = make(map[string]*ProjectTree)
	return nil
}

func makePathRel(basepath, targpath string) (string, error) {
	if filepath.IsAbs(targpath) {
		relPath, err := filepath.Rel(basepath, targpath)
		if err != nil {
			return "", err
		}
		return relPath, nil
	}
	return targpath, nil
}
