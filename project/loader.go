// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package project

import (
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/gitutil"
)

type importCache struct {
	localManifest bool
	ref           string

	// keeps track of first import tag, this is used to give helpful error
	// message in the case of import conflict
	parentImport string
}

type loader struct {
	Projects       Projects
	ProjectLocks   ProjectLocks
	Hooks          Hooks
	Packages       Packages
	PackageLocks   PackageLocks
	TmpDir         string
	localProjects  Projects
	importProjects Projects
	importCacheMap map[string]importCache
	update         bool
	cycleStack     []cycleInfo
	manifests      map[string]bool
	lockfiles      map[string]bool
	parentFile     string
}

func (ld *loader) cleanup() {
	if ld.TmpDir != "" {
		os.RemoveAll(ld.TmpDir)
		ld.TmpDir = ""
	}
}

type cycleInfo struct {
	file, key string
}

// newManifestLoader returns a new manifest loader.  The localProjects are used
// to resolve remote imports; if nil, encountering any remote import will result
// in an error.  If update is true, remote manifest import projects that don't
// exist locally are cloned under TmpDir, and inserted into localProjects.
//
// If update is true, remote changes to manifest projects will be fetched, and
// manifest projects that don't exist locally will be created in temporary
// directories, and added to localProjects.
func newManifestLoader(localProjects Projects, update bool, file string) *loader {
	return &loader{
		Projects:       make(Projects),
		ProjectLocks:   make(ProjectLocks),
		Hooks:          make(Hooks),
		Packages:       make(Packages),
		PackageLocks:   make(PackageLocks),
		localProjects:  localProjects,
		importProjects: make(Projects),
		update:         update,
		importCacheMap: make(map[string]importCache),
		manifests:      make(map[string]bool),
		lockfiles:      make(map[string]bool),
		parentFile:     file,
	}
}

// loadNoCycles checks for cycles in imports.  There are two types of cycles:
//   file - Cycle in the paths of manifest files in the local filesystem.
//   key  - Cycle in the remote manifests specified by remote imports.
//
// Example of file cycles.  File A imports file B, and vice versa.
//     file=manifest/A              file=manifest/B
//     <manifest>                   <manifest>
//       <localimport file="B"/>      <localimport file="A"/>
//     </manifest>                  </manifest>
//
// Example of key cycles.  The key consists of "remote/manifest", e.g.
//   https://vanadium.googlesource.com/manifest/v2/default
// In the example, key x/A imports y/B, and vice versa.
//     key=x/A                               key=y/B
//     <manifest>                            <manifest>
//       <import remote="y" manifest="B"/>     <import remote="x" manifest="A"/>
//     </manifest>                           </manifest>
//
// The above examples are simple, but the general strategy is demonstrated.  We
// keep a single stack for both files and keys, and push onto each stack before
// running the recursive read or update function, and pop the stack when the
// function is done.  If we see a duplicate on the stack at any point, we know
// there's a cycle.  Note that we know the file for both local and remote
// imports, but we only know the key for remote imports; the key for local
// imports is empty.
//
// A more complex case would involve a combination of local and remote imports,
// using the "root" attribute to change paths on the local filesystem.  In this
// case the key will eventually expose the cycle.
func (ld *loader) loadNoCycles(jirix *jiri.X, root, repoPath, file, ref, cycleKey, parentImport string, localManifest bool) error {
	f := file
	if repoPath != "" {
		f = filepath.Join(repoPath, file)
	}
	info := cycleInfo{f, cycleKey}
	for _, c := range ld.cycleStack {
		switch {
		case f == c.file:
			return fmt.Errorf("import cycle detected in local manifest files: %q", append(ld.cycleStack, info))
		case cycleKey == c.key && cycleKey != "":
			return fmt.Errorf("import cycle detected in remote manifest imports: %q", append(ld.cycleStack, info))
		}
	}
	ld.cycleStack = append(ld.cycleStack, info)
	if err := ld.load(jirix, root, repoPath, file, ref, parentImport, localManifest); err != nil {
		return err
	}
	ld.cycleStack = ld.cycleStack[:len(ld.cycleStack)-1]
	return nil
}

// shortFileName returns the relative path if file is relative to root,
// otherwise returns the file name unchanged.
func shortFileName(root, repoPath, file, ref string) string {
	if repoPath != "" {
		return fmt.Sprintf("%s %s:%s", shortFileName(root, "", repoPath, ""), ref, file)
	}
	if p := root + string(filepath.Separator); strings.HasPrefix(file, p) {
		return file[len(p):]
	}
	return file
}

func (ld *loader) Load(jirix *jiri.X, root, repoPath, file, ref, cycleKey, parentImport string, localManifest bool) error {
	jirix.TimerPush("load " + shortFileName(jirix.Root, repoPath, file, ref))
	defer jirix.TimerPop()
	return ld.loadNoCycles(jirix, root, repoPath, file, ref, cycleKey, parentImport, localManifest)
}

func (ld *loader) cloneManifestRepo(jirix *jiri.X, remote *Import, cacheDirPath string, localManifest bool) error {
	if !ld.update || localManifest {
		jirix.Logger.Warningf("import %q not found locally, getting from server. Please check your manifest file (default: .jiri_manifest).\nMake sure that the 'name' attributes on the 'import' and 'project' tags match and that there is a corresponding 'project' tag for every 'import' tag.\n\n", remote.Name)
	}
	jirix.Logger.Debugf("clone manifest project %q", remote.Name)
	// The remote manifest project doesn't exist locally.  Clone it into a
	// temp directory, and add it to ld.localProjects.
	if ld.TmpDir == "" {
		var err error
		if ld.TmpDir, err = ioutil.TempDir("", "jiri-load"); err != nil {
			return fmt.Errorf("TempDir() failed: %v", err)
		}
	}
	path := filepath.Join(ld.TmpDir, remote.projectKeyFileName())
	p, err := remote.toProject(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmtError(err)
	}
	remoteUrl := rewriteRemote(jirix, p.Remote)
	task := jirix.Logger.AddTaskMsg("Creating manifest: %s", remote.Name)
	defer task.Done()
	if cacheDirPath != "" {
		logStr := fmt.Sprintf("update/create cache for project %q", remote.Name)
		jirix.Logger.Debugf(logStr)
		task := jirix.Logger.AddTaskMsg(logStr)
		defer task.Done()
		if err := updateOrCreateCache(jirix, cacheDirPath, remoteUrl, remote.RemoteBranch, 0); err != nil {
			return err
		}
	}
	if err := clone(jirix, remoteUrl, path, gitutil.ReferenceOpt(cacheDirPath),
		gitutil.NoCheckoutOpt(true)); err != nil {
		return err
	}
	p.Revision = remote.Revision
	p.RemoteBranch = remote.RemoteBranch
	if err := checkoutHeadRevision(jirix, p, false); err != nil {
		return fmt.Errorf("Not able to checkout head for %s(%s): %v", p.Name, p.Path, err)
	}
	ld.localProjects[remote.ProjectKey()] = p
	return nil
}

// loadLockfile will only report errors on lockfiles such as unknown format or confliting data.
// All I/O related errors will be ignored.
func (ld *loader) loadLockfile(jirix *jiri.X, dir, lockFileName string) error {
	lockfile := path.Join(dir, lockFileName)
	if ld.lockfiles[lockfile] {
		return nil
	}

	if !(dir == "" || dir == "." || dir == jirix.Root || dir == string(filepath.Separator)) {
		if err := ld.loadLockfile(jirix, path.Dir(dir), lockFileName); err != nil {
			return err
		}
	}
	if _, err := os.Stat(lockfile); err != nil {
		if os.IsNotExist(err) {
			jirix.Logger.Debugf("could not find %q file at %q", lockFileName, lockfile)
		} else {
			jirix.Logger.Debugf("could not access %q file at %q due to error %v", lockFileName, lockfile, err)
		}
		// Supress I/O errors as it is OK if a lockfile cannot be accessed.
		return nil
	}
	data, err := ioutil.ReadFile(lockfile)
	if err != nil {
		jirix.Logger.Debugf("could not read %q file at %q due to error %v", lockFileName, lockfile, err)
		// Supress I/O errors as it is OK if a lockfile cannot be accessed.
		return nil
	}
	if err = ld.parseLockData(jirix, data); err != nil {
		return err
	}
	jirix.Logger.Debugf("loaded lockfile at %s", lockfile)
	ld.lockfiles[lockfile] = true
	return nil
}

func (ld *loader) parseLockData(jirix *jiri.X, data []byte) error {
	projectLocks, pkgLocks, err := UnmarshalLockEntries(data)
	if err != nil {
		return err
	}

	for k, v := range projectLocks {
		if projLock, ok := ld.ProjectLocks[k]; ok {
			if projLock != v {
				return fmt.Errorf("conflicting project lock entries %+v with %+v", projLock, v)
			}
		} else {
			ld.ProjectLocks[k] = v
		}
	}

	for k, v := range pkgLocks {
		if pkgLock, ok := ld.PackageLocks[k]; ok {
			// Only package locks may conflict during a normal 'jiri resolve'.
			// Treating conflicts as errors in all other scenarios.
			if pkgLock != v && !jirix.IgnoreLockConflicts {
				return fmt.Errorf("conflicting package lock entries %+v with %+v", pkgLock, v)
			}
		} else {
			ld.PackageLocks[k] = v
		}
	}

	return nil
}

func (ld *loader) load(jirix *jiri.X, root, repoPath, file, ref, parentImport string, localManifest bool) error {
	f := file
	if repoPath != "" {
		f = filepath.Join(repoPath, file)
	}
	if ld.manifests[f] {
		return nil
	}
	ld.manifests[f] = true

	loadManifestAndLocks := func(jirix *jiri.X, file string) (*Manifest, error) {
		if repoPath == "" {
			m, err := ManifestFromFile(jirix, file)
			if err != nil {
				return nil, fmt.Errorf("Error reading from manifest file %s %s:%s:error(%s)", repoPath, ref, file, err)
			}
			if jirix.LockfileEnabled {
				if err := ld.loadLockfile(jirix, path.Dir(file), jirix.LockfileName); err != nil {
					return nil, err
				}
			}
			return m, err
		}
		// repoPath != ""
		s, err := gitutil.New(jirix, gitutil.RootDirOpt(repoPath)).Show(ref, file)
		if err != nil {
			return nil, fmt.Errorf("Unable to get manifest file for %s %s:%s:error(%s)", repoPath, ref, file, err)
		}
		m, err := ManifestFromBytes([]byte(s))
		if err != nil {
			return nil, fmt.Errorf("Error reading from manifest file %s %s:%s:error(%s)", repoPath, ref, file, err)
		}
		if jirix.LockfileEnabled {
			lockfile := path.Join(path.Dir(file), jirix.LockfileName)
			if s, err = gitutil.New(jirix, gitutil.RootDirOpt(repoPath)).Show(ref, lockfile); err != nil {
				// It's fine if jiri.lock cannot be read, skip the jiri.lock
				jirix.Logger.Debugf("Could not find jiri.lock at %s/%s", repoPath, lockfile)
			} else {
				if err = ld.parseLockData(jirix, []byte(s)); err != nil {
					return nil, err
				}
				jirix.Logger.Debugf("loaded lockfile at %s/%s", repoPath, lockfile)
			}
		}
		return m, nil
	}

	m, err := loadManifestAndLocks(jirix, file)
	if err != nil {
		return err
	}

	// Process remote imports.
	for _, remote := range m.Imports {
		nextRoot := filepath.Join(root, remote.Root)
		remote.Name = filepath.Join(nextRoot, remote.Name)
		key := remote.ProjectKey()
		p, ok := ld.localProjects[key]
		cacheDirPath, err := cacheDirPathFromRemote(jirix.Cache, remote.Remote)
		if err != nil {
			return err
		}

		if !ok {
			if err := ld.cloneManifestRepo(jirix, &remote, cacheDirPath, localManifest); err != nil {
				return err
			}
			p = ld.localProjects[key]
		}
		// Reset the project to its specified branch and load the next file.  Note
		// that we call load() recursively, so multiple files may be loaded by
		// loadImport.
		p.Revision = remote.Revision
		p.RemoteBranch = remote.RemoteBranch
		ld.importProjects[key] = p
		pi := parentImport
		if pi == "" {
			pi = fmt.Sprintf("import[manifest=%q, remote=%q]", remote.Manifest, remote.Remote)
		}

		if err := ld.loadImport(jirix, nextRoot, remote.Manifest, remote.cycleKey(), cacheDirPath, pi, p, localManifest); err != nil {
			return err
		}
	}

	// Process local imports.
	for _, local := range m.LocalImports {
		nextFile := filepath.Join(filepath.Dir(file), local.File)
		if err := ld.Load(jirix, root, repoPath, nextFile, ref, "", parentImport, localManifest); err != nil {
			return err
		}
	}

	hookMap := make(map[string][]*Hook)

	for idx, _ := range m.Hooks {
		hook := &m.Hooks[idx]
		if err := hook.validate(); err != nil {
			return err
		}
		hookMap[hook.ProjectName] = append(hookMap[hook.ProjectName], hook)
	}

	// Collect projects.
	for _, project := range m.Projects {
		// Make paths absolute by prepending <root>.
		project.absolutizePaths(filepath.Join(jirix.Root, root))

		if hooks, ok := hookMap[project.Name]; ok {
			for _, hook := range hooks {
				hook.ActionPath = project.Path
			}
		}

		// Prepend the root to the project name.  This will be a noop if the import is not rooted.
		project.Name = filepath.Join(root, project.Name)
		key := project.Key()

		if r, ok := ld.importProjects[key]; ok {
			// update revision for this project
			if r.Revision != "" && r.Revision != "HEAD" {
				if project.Revision == "" || project.Revision == "HEAD" {
					project.Revision = r.Revision
				} else {
					return fmt.Errorf("project %q found in %q defines different revision than its corresponding import tag.", key, shortFileName(jirix.Root, repoPath, file, ref))
				}
			}
		}

		if dup, ok := ld.Projects[key]; ok && dup != project {
			// TODO(toddw): Tell the user the other conflicting file.
			return fmt.Errorf("duplicate project %q found in %q", key, shortFileName(jirix.Root, repoPath, file, ref))
		}

		ld.Projects[key] = project
	}

	// Apply overrides.
	if parentImport == "" {
		for _, override := range m.Overrides {
			// Make paths absolute by prepending <root>.
			override.absolutizePaths(filepath.Join(jirix.Root, root))
			override.Name = filepath.Join(root, override.Name)
			key := override.Key()

			if _, ok := ld.importProjects[key]; ok {
				return fmt.Errorf("cannot override project %q because the project contains an imported manifest", key)
			}

			if _, ok := ld.Projects[key]; !ok {
				return fmt.Errorf("failed to override %q found in %q. Original project not found in manifest", key, shortFileName(jirix.Root, repoPath, file, ref))
			}

			project := ld.Projects[key]
			project.update(&override)
			ld.Projects[key] = project
		}
	} else if len(m.Overrides) != 0 {
		return fmt.Errorf("manifest %q contains overrides but was imported by %q. Overrides are allowed only in the root manifest.", shortFileName(jirix.Root, repoPath, file, ref), parentImport)
	}

	for _, hook := range m.Hooks {
		if hook.ActionPath == "" {
			return fmt.Errorf("invalid hook %q for project %q. Please make sure you are importing project %q and this hook is in the manifest which directly/indirectly imports that project.", hook.Name, hook.ProjectName, hook.ProjectName)
		}
		key := hook.Key()
		ld.Hooks[key] = hook
	}

	for _, pkg := range m.Packages {
		key := pkg.Key()
		ld.Packages[key] = pkg
	}
	return nil
}

func (ld *loader) loadImport(jirix *jiri.X, root, file, cycleKey, cacheDirPath, parentImport string, project Project, localManifest bool) (e error) {
	lm := localManifest
	ref := ""

	if v, ok := ld.importCacheMap[strings.Trim(project.Remote, "/")]; ok {
		// local manifest in cache takes precedence as this might be manifest mentioned in .jiri_manifest
		lm = v.localManifest
		ref = v.ref
		// check conflicting imports
		if !lm && ref != "JIRI_HEAD" {
			if tref, err := GetHeadRevision(jirix, project); err != nil {
				return err
			} else if tref != ref {
				return fmt.Errorf("Conflicting ref for import %s - %q and %q. There are conflicting imports in file:\n%s:\n'%s' and '%s'",
					jirix.Color.Red(project.Remote), ref, tref, jirix.Color.Yellow(ld.parentFile),
					jirix.Color.Yellow(v.parentImport), jirix.Color.Yellow(parentImport))
			}
		}
	} else {
		// We don't need to fetch or find ref for local manifest changes
		if !lm {
			// We only fetch on updates.
			if ld.update {
				// Fetch only if project not pinned or revision not available in
				// local git as we anyways update all the projects later.
				fetch := true
				if project.Revision != "" && project.Revision != "HEAD" {
					if _, err := gitutil.New(jirix, gitutil.RootDirOpt(project.Path)).Show(project.Revision, ""); err == nil {
						fetch = false
					}
				}
				if fetch {
					if cacheDirPath != "" {
						remoteUrl := rewriteRemote(jirix, project.Remote)
						if err := updateOrCreateCache(jirix, cacheDirPath, remoteUrl, project.RemoteBranch, 0); err != nil {
							return err
						}
					}
					if err := fetchAll(jirix, project); err != nil {
						return fmt.Errorf("Fetch failed for project(%s), %s", project.Path, err)
					}
				}
			} else {
				// If not updating then try to get file from JIRI_HEAD
				if _, err := gitutil.New(jirix, gitutil.RootDirOpt(project.Path)).Show("JIRI_HEAD", ""); err == nil {
					// JIRI_HEAD available, set ref
					ref = "JIRI_HEAD"
				}
			}
			if ref == "" {
				var err error
				if ref, err = GetHeadRevision(jirix, project); err != nil {
					return err
				}
			}
		}
		ld.importCacheMap[strings.Trim(project.Remote, "/")] = importCache{
			localManifest: lm,
			ref:           ref,
			parentImport:  parentImport,
		}
	}
	if lm {
		// load from local checked out file
		return ld.Load(jirix, root, "", filepath.Join(project.Path, file), "", cycleKey, parentImport, false)
	}
	return ld.Load(jirix, root, project.Path, file, ref, cycleKey, parentImport, false)
}
