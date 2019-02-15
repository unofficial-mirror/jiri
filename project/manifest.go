// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package project

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/cipd"
	"fuchsia.googlesource.com/jiri/envvar"
	"fuchsia.googlesource.com/jiri/gerrit"
	"fuchsia.googlesource.com/jiri/retry"
	"golang.org/x/net/publicsuffix"
)

// Manifest represents a setting used for updating the universe.
type Manifest struct {
	Version      string        `xml:"version,attr,omitempty"`
	Imports      []Import      `xml:"imports>import"`
	LocalImports []LocalImport `xml:"imports>localimport"`
	Projects     []Project     `xml:"projects>project"`
	Overrides    []Project     `xml:"overrides>project"`
	Hooks        []Hook        `xml:"hooks>hook"`
	Packages     []Package     `xml:"packages>package"`
	XMLName      struct{}      `xml:"manifest"`
}

// ManifestFromBytes returns a manifest parsed from data, with defaults filled
// in.
func ManifestFromBytes(data []byte) (*Manifest, error) {
	m := new(Manifest)
	if len(data) > 0 {
		if err := xml.Unmarshal(data, m); err != nil {
			return nil, err
		}
	}
	if err := m.fillDefaults(); err != nil {
		return nil, err
	}
	return m, nil
}

// ManifestFromFile returns a manifest parsed from the contents of filename,
// with defaults filled in.
//
// Note that unlike ProjectFromFile, ManifestFromFile does not convert project
// paths to absolute paths because it's possible to load a manifest with a
// specific root directory different from jirix.Root.  The usual way to load a
// manifest is through LoadManifest, which does absolutize the paths, and uses
// the correct root directory.
func ManifestFromFile(jirix *jiri.X, filename string) (*Manifest, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, fmtError(err)
	}
	m, err := ManifestFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("invalid manifest %s: %v", filename, err)
	}
	return m, nil
}

var (
	newlineBytes        = []byte("\n")
	emptyImportsBytes   = []byte("\n  <imports></imports>\n")
	emptyProjectsBytes  = []byte("\n  <projects></projects>\n")
	emptyOverridesBytes = []byte("\n  <overrides></overrides>\n")
	emptyHooksBytes     = []byte("\n  <hooks></hooks>\n")
	emptyPackagesBytes  = []byte("\n  <packages></packages>\n")

	endElemBytes        = []byte("/>\n")
	endImportBytes      = []byte("></import>\n")
	endLocalImportBytes = []byte("></localimport>\n")
	endProjectBytes     = []byte("></project>\n")
	endHookBytes        = []byte("></hook>\n")
	endPackageBytes     = []byte("></package>\n")

	endImportSoloBytes  = []byte("></import>")
	endProjectSoloBytes = []byte("></project>")
	endElemSoloBytes    = []byte("/>")

	errGitHookNotRequired = errors.New("git hooks are not required")
)

const (
	fuchsiaGerritHost = "https://fuchsia-review.googlesource.com"
)

// deepCopy returns a deep copy of Manifest.
func (m *Manifest) deepCopy() *Manifest {
	x := new(Manifest)
	x.Imports = append([]Import(nil), m.Imports...)
	x.LocalImports = append([]LocalImport(nil), m.LocalImports...)
	x.Projects = append([]Project(nil), m.Projects...)
	x.Overrides = append([]Project(nil), m.Overrides...)
	x.Hooks = append([]Hook(nil), m.Hooks...)
	x.Packages = append([]Package(nil), m.Packages...)
	x.Version = m.Version
	return x
}

// ToBytes returns m as serialized bytes, with defaults unfilled.
func (m *Manifest) ToBytes() ([]byte, error) {
	m = m.deepCopy() // avoid changing manifest when unfilling defaults.
	if err := m.unfillDefaults(); err != nil {
		return nil, err
	}
	data, err := xml.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("manifest xml.Marshal failed: %v", err)
	}
	// It's hard (impossible?) to get xml.Marshal to elide some of the empty
	// elements, or produce short empty elements, so we post-process the data.
	data = bytes.Replace(data, emptyImportsBytes, newlineBytes, -1)
	data = bytes.Replace(data, emptyProjectsBytes, newlineBytes, -1)
	data = bytes.Replace(data, emptyOverridesBytes, newlineBytes, -1)
	data = bytes.Replace(data, emptyHooksBytes, newlineBytes, -1)
	data = bytes.Replace(data, emptyPackagesBytes, newlineBytes, -1)
	data = bytes.Replace(data, endImportBytes, endElemBytes, -1)
	data = bytes.Replace(data, endLocalImportBytes, endElemBytes, -1)
	data = bytes.Replace(data, endProjectBytes, endElemBytes, -1)
	data = bytes.Replace(data, endHookBytes, endElemBytes, -1)
	data = bytes.Replace(data, endPackageBytes, endElemBytes, -1)
	if !bytes.HasSuffix(data, newlineBytes) {
		data = append(data, '\n')
	}
	return data, nil
}

// ToFile writes the manifest m to a file with the given filename, with
// defaults unfilled and all project paths relative to the jiri root.
func (m *Manifest) ToFile(jirix *jiri.X, filename string) error {
	// Replace absolute paths with relative paths to make it possible to move
	// the root directory locally.
	projects := []Project{}
	for _, project := range m.Projects {
		if err := project.relativizePaths(jirix.Root); err != nil {
			return err
		}
		projects = append(projects, project)
	}
	// Sort the projects and hooks to ensure that the output of "jiri
	// snapshot" is deterministic.  Sorting the hooks by name allows
	// some control over the ordering of the hooks in case that is
	// necessary.
	sort.Sort(ProjectsByPath(projects))
	m.Projects = projects
	sort.Sort(HooksByName(m.Hooks))
	data, err := m.ToBytes()
	if err != nil {
		return err
	}
	return safeWriteFile(jirix, filename, data)
}

func (m *Manifest) fillDefaults() error {
	for index := range m.Imports {
		if err := m.Imports[index].fillDefaults(); err != nil {
			return err
		}
	}
	for index := range m.LocalImports {
		if err := m.LocalImports[index].validate(); err != nil {
			return err
		}
	}
	for index := range m.Projects {
		if err := m.Projects[index].fillDefaults(); err != nil {
			return err
		}
	}
	for index := range m.Overrides {
		if err := m.Overrides[index].fillDefaults(); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manifest) unfillDefaults() error {
	for index := range m.Imports {
		if err := m.Imports[index].unfillDefaults(); err != nil {
			return err
		}
	}
	for index := range m.LocalImports {
		if err := m.LocalImports[index].validate(); err != nil {
			return err
		}
	}
	for index := range m.Projects {
		if err := m.Projects[index].unfillDefaults(); err != nil {
			return err
		}
	}
	for index := range m.Overrides {
		if err := m.Overrides[index].unfillDefaults(); err != nil {
			return err
		}
	}
	return nil
}

// Import represents a remote manifest import.
type Import struct {
	// Manifest file to use from the remote manifest project.
	Manifest string `xml:"manifest,attr,omitempty"`
	// Name is the name of the remote manifest project, used to determine the
	// project key.
	Name string `xml:"name,attr,omitempty"`
	// Remote is the remote manifest project to import.
	Remote string `xml:"remote,attr,omitempty"`
	// Revision is the revison to checkout,
	// this takes precedence over RemoteBranch
	Revision string `xml:"revision,attr,omitempty"`
	// RemoteBranch is the name of the remote branch to track.
	RemoteBranch string `xml:"remotebranch,attr,omitempty"`
	// Root path, prepended to all project paths specified in the manifest file.
	Root    string   `xml:"root,attr,omitempty"`
	XMLName struct{} `xml:"import"`
}

func (i *Import) fillDefaults() error {
	if i.RemoteBranch == "" {
		i.RemoteBranch = "master"
	}
	if i.Revision == "" {
		i.Revision = "HEAD"
	}
	return i.validate()
}

func (i *Import) RemoveDefaults() {
	if i.RemoteBranch == "master" {
		i.RemoteBranch = ""
	}
	if i.Revision == "HEAD" {
		i.Revision = ""
	}
}

func (i *Import) unfillDefaults() error {
	i.RemoveDefaults()
	return i.validate()
}

func (i *Import) validate() error {
	if i.Manifest == "" || i.Remote == "" {
		return fmt.Errorf("bad import: both manifest and remote must be specified")
	}
	return nil
}

func (i *Import) toProject(path string) (Project, error) {
	p := Project{
		Name:         i.Name,
		Path:         path,
		Remote:       i.Remote,
		Revision:     i.Revision,
		RemoteBranch: i.RemoteBranch,
	}
	err := p.fillDefaults()
	return p, err
}

// ProjectKey returns the unique ProjectKey for the imported project.
func (i *Import) ProjectKey() ProjectKey {
	return MakeProjectKey(i.Name, i.Remote)
}

// projectKeyFileName returns a file name based on the ProjectKey.
func (i *Import) projectKeyFileName() string {
	// TODO(toddw): Disallow weird characters from project names.
	hash := fnv.New64a()
	hash.Write([]byte(i.ProjectKey()))
	return fmt.Sprintf("%s_%x", i.Name, hash.Sum64())
}

// cycleKey returns a key based on the remote and manifest, used for
// cycle-detection.  It's only valid for new-style remote imports; it's empty
// for the old-style local imports.
func (i *Import) cycleKey() string {
	if i.Remote == "" {
		return ""
	}
	// We don't join the remote and manifest with a slash or any other url-safe
	// character, since that might not be unique.  E.g.
	//   remote:   https://foo.com/a/b    remote:   https://foo.com/a
	//   manifest: c                      manifest: b/c
	// In both cases, the key would be https://foo.com/a/b/c.
	return i.Remote + " + " + i.Manifest
}

// LocalImport represents a local manifest import.
type LocalImport struct {
	// Manifest file to import from.
	File    string   `xml:"file,attr,omitempty"`
	XMLName struct{} `xml:"localimport"`
}

func (i *LocalImport) validate() error {
	if i.File == "" {
		return fmt.Errorf("bad localimport: must specify file: %+v", *i)
	}
	return nil
}

type LocalConfig struct {
	Ignore   bool     `xml:"ignore"`
	NoUpdate bool     `xml:"no-update"`
	NoRebase bool     `xml:"no-rebase"`
	XMLName  struct{} `xml:"config"`
}

// Reads localConfig from given reader. Returns incorrect bytes
func (lc *LocalConfig) ReadFrom(r io.Reader) (int64, error) {
	return 1, xml.NewDecoder(r).Decode(lc)
}

func LocalConfigFromFile(jirix *jiri.X, filename string) (LocalConfig, error) {
	var lc LocalConfig
	f, err := os.Open(filename)
	if os.IsNotExist(err) {
		return lc, nil
	} else if err != nil {
		return lc, fmtError(err)
	}
	_, err = lc.ReadFrom(f)
	return lc, err
}

// Writes the localConfig to given writer. Returns incorrect bytes
func (lc *LocalConfig) WriteTo(writer io.Writer) (int64, error) {
	encoder := xml.NewEncoder(writer)
	encoder.Indent("", " ")
	return 1, encoder.Encode(lc)
}

func (lc *LocalConfig) ToFile(jirix *jiri.X, filename string) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		return fmtError(err)
	}
	writer, err := os.Create(filename)
	if err != nil {
		return fmtError(err)
	}
	defer writer.Close()
	_, err = lc.WriteTo(writer)
	return err
}

func WriteLocalConfig(jirix *jiri.X, project Project, lc LocalConfig) error {
	configFile := filepath.Join(project.Path, jiri.ProjectMetaDir, jiri.ProjectConfigFile)
	return lc.ToFile(jirix, configFile)
}

// Hook represents a hook to run
type Hook struct {
	Name        string   `xml:"name,attr"`
	Action      string   `xml:"action,attr"`
	ProjectName string   `xml:"project,attr"`
	XMLName     struct{} `xml:"hook"`
	ActionPath  string   `xml:"-"`
}

// HookKey is a unique string for a project.
type HookKey string

type Hooks map[HookKey]Hook

// Key returns the unique HookKey for the hook.
func (h Hook) Key() HookKey {
	return MakeHookKey(h.Name, h.ProjectName)
}

// MakeHookKey returns the hook key, given the hook and project name.
func MakeHookKey(name, projectName string) HookKey {
	return HookKey(name + KeySeparator + projectName)
}

func (h *Hook) validate() error {
	if strings.Contains(h.Name, KeySeparator) {
		return fmt.Errorf("bad hook: name cannot contain %q: %+v", KeySeparator, *h)
	}
	if strings.Contains(h.ProjectName, KeySeparator) {
		return fmt.Errorf("bad hook: project cannot contain %q: %+v", KeySeparator, *h)
	}
	return nil
}

// HooksByName implements the Sort interface. It sorts Hooks by the Name
// and ProjectName field.
type HooksByName []Hook

func (hooks HooksByName) Len() int {
	return len(hooks)
}
func (hooks HooksByName) Swap(i, j int) {
	hooks[i], hooks[j] = hooks[j], hooks[i]
}
func (hooks HooksByName) Less(i, j int) bool {
	if hooks[i].Name == hooks[j].Name {
		return hooks[i].ProjectName < hooks[j].ProjectName
	}
	return hooks[i].Name < hooks[j].Name
}

// Package struct represents the <package> tag in manifest files.
type Package struct {
	Name      string            `xml:"name,attr"`
	Version   string            `xml:"version,attr"`
	Path      string            `xml:"path,attr,omitempty"`
	Internal  bool              `xml:"internal,attr,omitempty"`
	Platforms string            `xml:"platforms,attr,omitempty"`
	Instances []PackageInstance `xml:"instance"`
	XMLName   struct{}          `xml:"package"`
}

type PackageKey string

type Packages map[PackageKey]Package

func (p Package) Key() PackageKey {
	return PackageKey(p.Name)
}

type PackageInstance struct {
	Name    string   `xml:"name,attr"`
	ID      string   `xml:"id,attr"`
	XMLName struct{} `xml:"instance"`
}

// FillDefaults function fills default platforms information into
// Package struct if it is not defined and path is using template.
func (p *Package) FillDefaults() error {
	if cipd.MustExpand(p.Name) && p.Platforms == "" {
		for _, v := range cipd.DefaultPlatforms() {
			p.Platforms += v.String() + ","
		}
		if p.Platforms[len(p.Platforms)-1] == ',' {
			p.Platforms = p.Platforms[:len(p.Platforms)-1]
		}
	}
	return nil
}

// GetPlatforms returns the platforms information of
// this Package struct.
func (p *Package) GetPlatforms() ([]cipd.Platform, error) {
	if err := p.FillDefaults(); err != nil {
		return nil, err
	}
	retList := make([]cipd.Platform, 0)
	platStrs := strings.Split(p.Platforms, ",")
	for _, platStr := range platStrs {
		if platStr == "" {
			continue
		}
		plat, err := cipd.NewPlatform(platStr)
		if err != nil {
			return nil, err
		}
		retList = append(retList, plat)
	}
	return retList, nil
}

// LoadManifest loads the manifest, starting with the .jiri_manifest file,
// resolving remote and local imports.  Returns the projects specified by
// the manifest.
//
// WARNING: LoadManifest cannot be run multiple times in parallel!  It invokes
// git operations which require a lock on the filesystem.  If you see errors
// about ".git/index.lock exists", you are likely calling LoadManifest in
// parallel.
func LoadManifest(jirix *jiri.X) (Projects, Hooks, Packages, error) {
	jirix.TimerPush("load manifest")
	defer jirix.TimerPop()
	file := jirix.JiriManifestFile()
	localProjects, err := LocalProjects(jirix, FastScan)
	if err != nil {
		return nil, nil, nil, err
	}
	return LoadManifestFile(jirix, file, localProjects, false)
}

func (ld *loader) enforceLocks(jirix *jiri.X) error {
	enforceProjLocks := func(jirix *jiri.X) (err error) {
		for _, v := range ld.Projects {
			if projectLock, ok := ld.ProjectLocks[ProjectLockKey(v.Key())]; ok {
				if v.Revision == "" {
					v.Revision = projectLock.Revision
					ld.Projects[v.Key()] = v
				} else if v.Revision != projectLock.Revision {
					s := fmt.Sprintf("project %+v has conflicting revisions in manifest and jiri.lock: %s:%s", v, v.Revision, projectLock.Revision)
					jirix.Logger.Debugf(s)
					err = errors.New(s)
				}
			}
		}
		return
	}

	if err := enforceProjLocks(jirix); err != nil {
		return err
	}

	plats := cipd.DefaultPlatforms()
	usedPkgLocks := make(map[PackageLockKey]bool)
	for k := range ld.PackageLocks {
		usedPkgLocks[k] = false
	}
	for _, v := range ld.Packages {
		pkgs, err := cipd.Expand(v.Name, plats)
		if err != nil {
			return err
		}

		for _, pkg := range pkgs {
			if pkgLock, ok := ld.PackageLocks[PackageLockKey(pkg)]; ok {
				ins := PackageInstance{
					Name: pkgLock.PackageName,
					ID:   pkgLock.InstanceID,
				}
				v.Instances = append(v.Instances, ins)
				ld.Packages[v.Key()] = v
				usedPkgLocks[pkgLock.Key()] = true
			} else {
				jirix.Logger.Debugf("Package %q is not found in jiri.lock", pkg)
			}
		}
	}

	for k, v := range usedPkgLocks {
		if !v {
			jirix.Logger.Debugf("PackageLock %v is not used", k)
		}
	}
	return nil
}

// LoadManifestFile loads the manifest starting with the given file, resolving
// remote and local imports.  Local projects are used to resolve remote imports;
// if nil, encountering any remote import will result in an error.
//
// WARNING: LoadManifestFile cannot be run multiple times in parallel!  It
// invokes git operations which require a lock on the filesystem.  If you see
// errors about ".git/index.lock exists", you are likely calling
// LoadManifestFile in parallel.
func LoadManifestFile(jirix *jiri.X, file string, localProjects Projects, localManifest bool) (Projects, Hooks, Packages, error) {
	ld := newManifestLoader(localProjects, false, file)
	if err := ld.Load(jirix, "", "", file, "", "", "", localManifest); err != nil {
		return nil, nil, nil, err
	}
	jirix.AddCleanupFunc(ld.cleanup)
	if jirix.LockfileEnabled {
		if err := ld.enforceLocks(jirix); err != nil {
			return nil, nil, nil, err
		}
	}
	return ld.Projects, ld.Hooks, ld.Packages, nil
}

// LoadUpdatedManifest loads an updated manifest starting with the .jiri_manifest file for localProjects. It will use
// local manifest files instead of manifest files in remote repositories if localManifest is set to true.
func LoadUpdatedManifest(jirix *jiri.X, localProjects Projects, localManifest bool) (Projects, Hooks, Packages, error) {
	jirix.TimerPush("load updated manifest")
	defer jirix.TimerPop()
	ld := newManifestLoader(localProjects, true, jirix.JiriManifestFile())
	if err := ld.Load(jirix, "", "", jirix.JiriManifestFile(), "", "", "", localManifest); err != nil {
		return nil, nil, nil, err
	}
	jirix.AddCleanupFunc(ld.cleanup)
	if jirix.LockfileEnabled {
		if err := ld.enforceLocks(jirix); err != nil {
			return nil, nil, nil, err
		}
	}
	return ld.Projects, ld.Hooks, ld.Packages, nil
}

// resovlePackageLocks resolves instance ids using versions described in given
// pkgs using cipd.
func resolvePackageLocks(jirix *jiri.X, pkgs Packages) (PackageLocks, error) {
	jirix.TimerPush("resove instance id for cipd packages")
	defer jirix.TimerPop()

	ensureFilePath, err := generateEnsureFile(jirix, pkgs, false)
	if err != nil {
		return nil, err
	}
	defer os.Remove(ensureFilePath)

	pkgInstances, err := cipd.Resolve(jirix, ensureFilePath)
	if err != nil {
		return nil, err
	}
	// TODO: Remove this boilerplate once we have a better package
	// layout that doesn't cause import cycles
	pkgLocks := make(PackageLocks)
	for _, val := range pkgInstances {
		pkgLock := PackageLock{val.PackageName, val.InstanceID}
		pkgLocks[pkgLock.Key()] = pkgLock
	}

	return pkgLocks, nil
}

// resolveProjectLocks resolves project revisions <project> tags in manifests
func resolveProjectLocks(jirix *jiri.X, projects Projects) (ProjectLocks, error) {
	projectLocks := make(ProjectLocks)
	for _, v := range projects {
		projectLock := ProjectLock{v.Remote, v.Name, v.Revision}
		projectLocks[projectLock.Key()] = projectLock
	}
	return projectLocks, nil
}

// FetchPackages fetches prebuilt packages described in given pkgs using cipd.
// Parameter fetchTimeout is in minutes.
func FetchPackages(jirix *jiri.X, pkgs Packages, fetchTimeout uint) error {
	jirix.TimerPush("fetch cipd packages")
	defer jirix.TimerPop()

	ensureFilePath, err := generateEnsureFile(jirix, pkgs, !jirix.LockfileEnabled)
	if err != nil {
		return err
	}
	defer os.Remove(ensureFilePath)

	if jirix.LockfileEnabled {
		versionFilePath, err := generateVersionFile(jirix, ensureFilePath, pkgs)
		if err != nil {
			return err
		}
		defer os.Remove(versionFilePath)
	}

	if err := cipd.Ensure(jirix, ensureFilePath, jirix.Root, fetchTimeout); err != nil {
		return err
	}

	return nil
}

func generateEnsureFile(jirix *jiri.X, pkgs Packages, ignoreCryptoCheck bool) (string, error) {
	ensureFile, err := ioutil.TempFile("", "jiri*.ensure")
	if err != nil {
		return "", fmt.Errorf("not able to create tmp file: %v", err)
	}
	defer ensureFile.Close()
	ensureFilePath := ensureFile.Name()

	// Write header information
	// TODO: add "verfy_platform" attribute to each package tag
	// to avoid hardcoding platform names in Jiri
	var ensureFileBuf bytes.Buffer
	if !ignoreCryptoCheck {
		// Collect platforms used by this project
		allPlats := make(map[string]cipd.Platform)
		// CIPD ensure-file-resolve requires $VerifiedPlatform to be present
		// even if the package name is not using ${platform} template.
		// Put DefaultPlatforms into header to walkaround this issue.
		for _, plat := range cipd.DefaultPlatforms() {
			allPlats[plat.String()] = plat
		}
		for _, pkg := range pkgs {
			plats, err := pkg.GetPlatforms()
			if err != nil {
				return "", err
			}
			for _, plat := range plats {
				allPlats[plat.String()] = plat
			}
		}

		for _, plat := range allPlats {
			ensureFileBuf.WriteString(fmt.Sprintf("$VerifiedPlatform %s\n", plat))
		}
		versionFileName := ensureFilePath[:len(ensureFilePath)-len(".ensure")] + ".version"
		ensureFileBuf.WriteString("$ResolvedVersions " + versionFileName + "\n")
	}
	ensureFileBuf.WriteString("$ParanoidMode CheckPresence\n")
	ensureFileBuf.WriteString("\n")

	// Perform ACL checks on internal projects
	pkgACLMap := make(map[string]bool)
	pkgVersionMap := make(map[string]string)
	for _, pkg := range pkgs {
		pkg.Name = strings.TrimRight(pkg.Name, "/")
		if pkg.Internal {
			pkgACLMap[pkg.Name] = false
			pkgVersionMap[pkg.Name] = pkg.Version
		}
	}
	if len(pkgACLMap) != 0 {
		if err := cipd.CheckPackageACL(jirix, pkgACLMap, pkgVersionMap); err != nil {
			return "", err
		}
	}

	hasSkippedPkgs := false
	for _, pkg := range pkgs {
		if val, ok := pkgACLMap[pkg.Name]; ok && !val {
			hasSkippedPkgs = true
			continue
		}
		cipdDecl, err := pkg.cipdDecl()
		if err != nil {
			return "", err
		}
		ensureFileBuf.WriteString(cipdDecl)
		ensureFileBuf.WriteString("\n")
	}

	jirix.Logger.Debugf("Generated ensure file content:\n%v", ensureFileBuf.String())
	if _, err := ensureFileBuf.WriteTo(ensureFile); err != nil {
		return "", err
	}
	if err := ensureFile.Sync(); err != nil {
		return "", err
	}
	if hasSkippedPkgs {
		cipdLoggedIn, err := cipd.CheckLoggedIn(jirix)
		if err != nil {
			return "", err
		}
		if !cipdLoggedIn {
			jirix.Logger.Warningf("Some packages are skipped by cipd due to lack of access, you might want to run \"cipd auth-login\" and try again")
		}
	}
	return ensureFilePath, nil
}

func (p *Package) cipdDecl() (string, error) {
	var buf bytes.Buffer
	// Write "@Subdir" line to cipd declaration
	subdir := p.Path
	tmpl, err := template.New("pack").Parse(subdir)
	if err != nil {
		return "", fmt.Errorf("parsing package path %q failed", subdir)
	}
	var subdirBuf bytes.Buffer
	tmpl.Execute(&subdirBuf, cipd.CipdPlatform)
	subdir = subdirBuf.String()
	buf.WriteString(fmt.Sprintf("@Subdir %s\n", subdir))
	// Write package version line to cipd declaration
	plats, err := p.GetPlatforms()
	if err != nil {
		return "", err
	}
	cipdPath, err := cipd.Decl(p.Name, plats)
	if err != nil {
		return "", err
	}
	buf.WriteString(fmt.Sprintf("%s %s\n", cipdPath, p.Version))
	return buf.String(), nil
}

func generateVersionFile(jirix *jiri.X, ensureFile string, pkgs Packages) (string, error) {
	versionFileName := ensureFile[:len(ensureFile)-len(".ensure")] + ".version"

	var versionFileBuf bytes.Buffer
	// Just pour everything in pkgLocks into version file without matching package
	// names. cipd will do the matching for us.
	for _, pkg := range pkgs {
		jirix.Logger.Debugf("Generate version file using %+v", pkg)
		for _, ins := range pkg.Instances {
			decl := fmt.Sprintf("\n%s\n\t%s\n\t%s\n", ins.Name, pkg.Version, ins.ID)
			versionFileBuf.WriteString(decl)
		}
	}
	jirix.Logger.Debugf("Generated version file content:\n%v", versionFileBuf.String())
	return versionFileName, ioutil.WriteFile(versionFileName, versionFileBuf.Bytes(), 0655)
}

// RunHooks runs all given hooks.
func RunHooks(jirix *jiri.X, hooks Hooks, runHookTimeout uint) error {
	jirix.TimerPush("run hooks")
	defer jirix.TimerPop()
	type result struct {
		outFile *os.File
		errFile *os.File
		err     error
	}
	ch := make(chan result)
	tmpDir, err := ioutil.TempDir("", "run-hooks")
	if err != nil {
		return fmt.Errorf("not able to create tmp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	for _, hook := range hooks {
		go func(hook Hook) {
			logStr := fmt.Sprintf("running hook(%s) for project %q", hook.Name, hook.ProjectName)
			jirix.Logger.Debugf(logStr)
			task := jirix.Logger.AddTaskMsg(logStr)
			defer task.Done()
			outFile, err := ioutil.TempFile(tmpDir, hook.Name+"-out")
			if err != nil {
				ch <- result{nil, nil, fmtError(err)}
				return
			}
			errFile, err := ioutil.TempFile(tmpDir, hook.Name+"-err")
			if err != nil {
				ch <- result{nil, nil, fmtError(err)}
				return
			}

			fmt.Fprintf(outFile, "output for hook(%v) for project %q\n", hook.Name, hook.ProjectName)
			fmt.Fprintf(errFile, "Error for hook(%v) for project %q\n", hook.Name, hook.ProjectName)
			cmdLine := filepath.Join(hook.ActionPath, hook.Action)
			err = retry.Function(jirix, func() error {
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(runHookTimeout)*time.Minute)
				defer cancel()
				command := exec.CommandContext(ctx, cmdLine)
				command.Dir = hook.ActionPath
				command.Stdin = os.Stdin
				command.Stdout = outFile
				command.Stderr = errFile
				env := jirix.Env()
				command.Env = envvar.MapToSlice(env)
				jirix.Logger.Tracef("Run: %q", cmdLine)
				err = command.Run()
				if ctx.Err() == context.DeadlineExceeded {
					err = ctx.Err()
				}
				return err
			}, fmt.Sprintf("running hook(%s) for project %s", hook.Name, hook.ProjectName),
				retry.AttemptsOpt(jirix.Attempts))
			ch <- result{outFile, errFile, err}
		}(hook)

	}

	err = nil
	timeout := false
	for range hooks {
		out := <-ch
		defer func() {
			if out.outFile != nil {
				out.outFile.Close()
			}
			if out.errFile != nil {
				out.errFile.Close()
			}
		}()
		if out.err == context.DeadlineExceeded {
			timeout = true
			out.outFile.Sync()
			out.outFile.Seek(0, 0)
			var buf bytes.Buffer
			io.Copy(&buf, out.outFile)
			jirix.Logger.Errorf("Timeout while executing hook\n%s\n\n", buf.String())
			err = fmt.Errorf("Hooks execution failed.")
			continue
		}
		var outBuf bytes.Buffer
		if out.outFile != nil {
			out.outFile.Sync()
			out.outFile.Seek(0, 0)
			io.Copy(&outBuf, out.outFile)
		}
		if out.err != nil {
			var buf bytes.Buffer
			if out.errFile != nil {
				out.errFile.Sync()
				out.errFile.Seek(0, 0)
				io.Copy(&buf, out.errFile)
			}
			jirix.Logger.Errorf("%s\n%s\n%s\n", out.err, buf.String(), outBuf.String())
			err = fmt.Errorf("Hooks execution failed.")
		} else {
			if outBuf.String() != "" {
				jirix.Logger.Debugf("%s\n", outBuf.String())
			}
		}
	}
	if timeout {
		err = fmt.Errorf("%s Use %s flag to set timeout.", err, jirix.Color.Yellow("-hook-timeout"))
	}
	return err
}

type commitMsgFetcher map[string][]byte

func (f commitMsgFetcher) fetch(jirix *jiri.X, gerritHost, path string) ([]byte, error) {
	bytes, ok := f[gerritHost]
	if !ok {
		jirix.Logger.Debugf("Fetching %q", gerritHost+"/tools/hooks/commit-msg")
		data, err := gerrit.FetchFile(gerritHost, "/tools/hooks/commit-msg")
		if err != nil {
			if err != gerrit.ErrRedirectOnGerrit {
				// Network or disk IO error, halt jiri
				return nil, err
			}
			// gerritHost require SSO login
			if jirix.RewriteSsoToHttps {
				// Gerrit host require SSO but jiri has rewritesso flag turned on
				// In this case git hooks are useless, stop fetching git hooks
				return nil, errGitHookNotRequired
			}

			// Use commit-msg in cache if the domain has same eTLD and SLD.
			for k, v := range f {
				urlK, err := url.Parse(k)
				if err != nil {
					// This should not happen as this url is already downloaded before.
					return nil, fmt.Errorf("download commit-msg hook for host %q failed due to error %v", gerritHost, err)
				}
				urlG, err := url.Parse(gerritHost)
				if err != nil {
					// This should not happen either as gerritHost will be parsed by gerrit.FetchFile
					return nil, fmt.Errorf("download commit-msg hook from host %q failed due to error %v", gerritHost, err)
				}
				etpoK, err := publicsuffix.EffectiveTLDPlusOne(urlK.Hostname())
				if err != nil {
					// This should not happen as Both SLD and TLD should exist.
					return nil, fmt.Errorf("download commit-msg hook from host %q failed due to error %v", gerritHost, err)
				}
				etpoG, err := publicsuffix.EffectiveTLDPlusOne(urlG.Hostname())
				if err != nil {
					// This should not happen as Both SLD and TLD should exist.
					return nil, fmt.Errorf("download commit-msg hook from host %q failed due to error %v", gerritHost, err)
				}

				if etpoK == etpoG {
					jirix.Logger.Debugf("use commit-msg hook from host %q for host %q due to access limitations", k, gerritHost)
					data = v
					err = nil
					break
				}
			}

			if data == nil {
				// Could not find commit-msg in cache from domains with same eTLD and SLD.
				// Fetch commit-msg from fuchsia's gerrit server.
				data, err = gerrit.FetchFile(fuchsiaGerritHost, "/tools/hooks/commit-msg")
				if err != nil {
					// This will only happen if configuration error occured on fuchsia gerrit server
					return nil, fmt.Errorf("download commit-msg hook from host %q failed due to error %v", fuchsiaGerritHost, err)
				}
				jirix.Logger.Debugf("fallback to commit-msg from host %q for host %q due to access limitations", fuchsiaGerritHost, gerritHost)
			}
		}
		f[gerritHost] = data
		return data, nil
	}
	jirix.Logger.Debugf("Cached %q", gerritHost+"/tools/hooks/commit-msg")
	return bytes, nil
}

func applyGitHooks(jirix *jiri.X, ops []operation) error {
	jirix.TimerPush("apply githooks")
	defer jirix.TimerPop()
	commitMsgFetcher := commitMsgFetcher{}
	for _, op := range ops {
		if op.Kind() != "delete" && !op.Project().LocalConfig.Ignore && !op.Project().LocalConfig.NoUpdate {
			if op.Project().GerritHost != "" {
				hookPath := filepath.Join(op.Project().Path, ".git", "hooks", "commit-msg")
				commitHook, err := os.Create(hookPath)
				if err != nil {
					return fmtError(err)
				}
				bytes, err := commitMsgFetcher.fetch(jirix, op.Project().GerritHost, "/tools/hooks/commit-msg")
				if err != nil {
					if err != errGitHookNotRequired {
						jirix.Logger.Debugf("%v", err)
					}
					commitHook.Close()
					os.Remove(hookPath)
					continue
				}

				if _, err := commitHook.Write(bytes); err != nil {
					return err
				}
				jirix.Logger.Debugf("Saved commit-msg hook to project %q", op.Project().Path)
				commitHook.Close()
				if err := os.Chmod(hookPath, 0750); err != nil {
					return fmtError(err)
				}
			}
			hookPath := filepath.Join(op.Project().Path, ".git", "hooks", "post-commit")
			commitHook, err := os.Create(hookPath)
			if err != nil {
				return err
			}
			bytes := []byte(`#!/bin/sh

if ! git symbolic-ref HEAD &> /dev/null; then
  echo -e "WARNING: You are in a detached head state! You might lose this commit.\nUse 'git checkout -b <branch> to put it on a branch.\n"
fi
`)
			if _, err := commitHook.Write(bytes); err != nil {
				return err
			}
			commitHook.Close()
			if err := os.Chmod(hookPath, 0750); err != nil {
				return err
			}
		}
		if op.Project().GitHooks == "" {
			continue
		}
		// Don't want to run hooks when repo is deleted
		if op.Kind() == "delete" {
			continue
		}
		// Apply git hooks, overwriting any existing hooks.  Jiri is in control of
		// writing all hooks.
		gitHooksDstDir := filepath.Join(op.Project().Path, ".git", "hooks")
		// Copy the specified GitHooks directory into the project's git
		// hook directory.  We walk the file system, creating directories
		// and copying files as we encounter them.
		copyFn := func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			relPath, err := filepath.Rel(op.Project().GitHooks, path)
			if err != nil {
				return err
			}
			dst := filepath.Join(gitHooksDstDir, relPath)
			if info.IsDir() {
				return fmtError(os.MkdirAll(dst, 0755))
			}
			src, err := ioutil.ReadFile(path)
			if err != nil {
				return fmtError(err)
			}
			// The file *must* be executable to be picked up by git.
			return fmtError(ioutil.WriteFile(dst, src, 0755))
		}
		if err := filepath.Walk(op.Project().GitHooks, copyFn); err != nil {
			return err
		}
	}
	return nil
}
