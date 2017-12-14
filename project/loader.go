// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package project

import (
	"fmt"
	"io/ioutil"
	"os"
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
	Hooks          Hooks
	TmpDir         string
	localProjects  Projects
	importProjects Projects
	importCacheMap map[string]importCache
	update         bool
	cycleStack     []cycleInfo
	manifests      map[string]bool
	parentFile     string
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
		Hooks:          make(Hooks),
		localProjects:  localProjects,
		importProjects: make(Projects),
		update:         update,
		importCacheMap: make(map[string]importCache),
		manifests:      make(map[string]bool),
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
		jirix.Logger.Warningf("import %q not found locally, getting from server.\n\n", remote.Name)
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
		if jirix.Shared {
			if err := clone(jirix, cacheDirPath, path, gitutil.SharedOpt(true),
				gitutil.NoCheckoutOpt(true)); err != nil {
				return err
			}
		} else {
			if err := clone(jirix, remoteUrl, path, gitutil.ReferenceOpt(cacheDirPath),
				gitutil.NoCheckoutOpt(true)); err != nil {
				return err
			}
		}

	} else {
		if err := clone(jirix, remoteUrl, path, gitutil.NoCheckoutOpt(true)); err != nil {
			return err
		}
	}
	p.Revision = remote.Revision
	p.RemoteBranch = remote.RemoteBranch
	if err := checkoutHeadRevision(jirix, p, false); err != nil {
		return fmt.Errorf("Not able to checkout head for %s(%s): %v", p.Name, p.Path, err)
	}
	ld.localProjects[remote.ProjectKey()] = p
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
	var m *Manifest
	var err error
	if repoPath == "" {
		m, err = ManifestFromFile(jirix, file)
	} else {
		if s, err2 := gitutil.New(jirix, gitutil.RootDirOpt(repoPath)).Show(ref, file); err2 != nil {
			return fmt.Errorf("Unable to get manifest file for %s %s:%s, %s", repoPath, ref, file, err2)
		} else {
			m, err = ManifestFromBytes([]byte(s))
		}
	}
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

	for _, hook := range m.Hooks {
		if hook.ActionPath == "" {
			return fmt.Errorf("invalid hook \"%v\" for project \"%v\"", hook.Name, hook.ProjectName)
		}
		key := hook.Key()
		ld.Hooks[key] = hook
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
				if cacheDirPath != "" {
					remoteUrl := rewriteRemote(jirix, project.Remote)
					if err := updateOrCreateCache(jirix, cacheDirPath, remoteUrl, project.RemoteBranch, 0); err != nil {
						return err
					}
				}
				if err := fetchAll(jirix, project); err != nil {
					return fmt.Errorf("Fetch failed for project(%s), %s", project.Path, err)
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
