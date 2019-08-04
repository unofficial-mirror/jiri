// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package project

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
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
	Projects         Projects
	ProjectOverrides map[string]Project
	ImportOverrides  map[string]Import
	ProjectLocks     ProjectLocks
	Hooks            Hooks
	Packages         Packages
	PackageLocks     PackageLocks
	TmpDir           string
	localProjects    Projects
	importProjects   Projects
	importCacheMap   map[string]importCache
	importTree       importTree
	update           bool
	cycleStack       []cycleInfo
	manifests        map[string]bool
	lockfiles        map[string]bool
	parentFile       string
}

type importTreeNode struct {
	parents            map[*importTreeNode]bool
	children           map[*importTreeNode]bool
	tag                string
	computedAttributes attributes
}

type importTree struct {
	root          *importTreeNode
	pool          map[string]*importTreeNode
	filenameMap   map[string]bool
	projectKeyMap map[ProjectKey]*importTreeNode
}

func newImportTree() importTree {
	return importTree{
		root: &importTreeNode{
			make(map[*importTreeNode]bool),
			make(map[*importTreeNode]bool),
			"",
			nil,
		},
		pool:          make(map[string]*importTreeNode),
		filenameMap:   make(map[string]bool),
		projectKeyMap: make(map[ProjectKey]*importTreeNode),
	}
}

func (t *importTree) getNode(repoPath, file, ref string) *importTreeNode {
	key := filepath.Join(repoPath, file) + ":" + ref
	if v, ok := t.pool[key]; ok {
		return v
	}
	v := &importTreeNode{
		make(map[*importTreeNode]bool),
		make(map[*importTreeNode]bool),
		key,
		nil,
	}
	if len(t.pool) == 0 {
		t.root.addChild(v)
	}
	t.pool[key] = v
	return v
}

func (t *importTree) buildImportAttributes() {
	// Due to the logic in how remote imports are handled, the
	// manifest loader will create two nodes for a single remote import,
	// causing second one as an orphan. A simple solution is just
	// connect the orphan nodes to the Root to build a connected tree.
	for _, v := range t.pool {
		if len(v.parents) == 0 {
			t.root.addChild(v)
		}
	}
	// The file name of a manifest is used as a git attributes name only if that
	// file name is unique.
	dupMap := make(map[string]bool)
	for k := range t.filenameMap {
		filename := filepath.Base(k)
		if _, ok := dupMap[filename]; ok {
			dupMap[filename] = true
		} else {
			dupMap[filename] = false
		}
	}
	dfsAddAttribute(t.root, newAttributes(""))
	for _, node := range t.pool {
		for attr := range node.computedAttributes {
			if v, ok := dupMap[attr]; ok && v {
				delete(node.computedAttributes, attr)
			}
		}
	}
}

func dfsAddAttribute(curNode *importTreeNode, parentAttrs attributes) {
	if curNode.computedAttributes == nil {
		curNode.computedAttributes = newAttributes(curNode.tag)
	}
	curNode.computedAttributes.Add(parentAttrs)
	for k := range curNode.children {
		dfsAddAttribute(k, curNode.computedAttributes)
	}
}

// (TODO:haowei) generateAttributeGraph generate a graphviz .dot file of
// importTree for debugging purpose. It should be removed once .gitattribute
// generator is considered as stable.
func (t *importTree) generateAttributeGraph() string {
	var buf bytes.Buffer
	buf.WriteString("\ndigraph G {\n")
	nodeCount := 1
	nameMap := make(map[*importTreeNode]string)
	nameMap[t.root] = "n0"
	for _, v := range t.pool {
		nameMap[v] = fmt.Sprintf("n%d", nodeCount)
		nodeCount++
	}

	// The file name of a manifest is used as a git attributes name only if that
	// file name is unique.
	dupMap := make(map[string]bool)
	for k := range t.filenameMap {
		filename := filepath.Base(k)
		if _, ok := dupMap[filename]; ok {
			dupMap[filename] = true
		} else {
			dupMap[filename] = false
		}
	}
	// Output nodes
	for k, v := range nameMap {
		attrs := newAttributes(k.tag)
		for attr := range attrs {
			if v, ok := dupMap[attr]; ok && v {
				delete(attrs, attr)
			}
		}
		buf.WriteString(fmt.Sprintf("\t%s[label=\"%s,%p\"];\n", v, attrs.String(), k))
	}
	buf.WriteString("\n")
	// Output edges
	for k := range nameMap {
		for child := range k.children {
			nameSource := nameMap[k]
			nameTarget, ok := nameMap[child]
			if !ok {
				fmt.Printf("ERROR: cound not find target node %q,%p in nameMap\n", child.tag, child)
				continue
			}
			buf.WriteString(fmt.Sprintf("\t%s -> %s;\n", nameSource, nameTarget))
		}
	}

	buf.WriteString("\n}")
	return buf.String()
}

func (n *importTreeNode) addChild(other *importTreeNode) {
	if other == nil {
		return
	}
	n.children[other] = true
	other.parents[n] = true
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
		Projects:         make(Projects),
		ProjectOverrides: make(map[string]Project),
		ImportOverrides:  make(map[string]Import),
		ProjectLocks:     make(ProjectLocks),
		Hooks:            make(Hooks),
		Packages:         make(Packages),
		PackageLocks:     make(PackageLocks),
		localProjects:    localProjects,
		importProjects:   make(Projects),
		update:           update,
		importCacheMap:   make(map[string]importCache),
		manifests:        make(map[string]bool),
		lockfiles:        make(map[string]bool),
		importTree:       newImportTree(),
		parentFile:       file,
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
	r := remoteUrl
	task := jirix.Logger.AddTaskMsg("Creating manifest: %s", remote.Name)
	defer task.Done()
	if cacheDirPath != "" {
		logStr := fmt.Sprintf("update/create cache for project %q", remote.Name)
		jirix.Logger.Debugf(logStr)
		task := jirix.Logger.AddTaskMsg(logStr)
		defer task.Done()
		if err := updateOrCreateCache(jirix, cacheDirPath, remoteUrl, remote.RemoteBranch, remote.Revision, 0); err != nil {
			return err
		}
		r = cacheDirPath
	}
	opts := []gitutil.CloneOpt{gitutil.ReferenceOpt(cacheDirPath), gitutil.NoCheckoutOpt(true)}
	if jirix.Partial {
		opts = append(opts, gitutil.OmitBlobsOpt(true))
	}
	if err := clone(jirix, r, path, opts...); err != nil {
		return err
	}
	scm := gitutil.New(jirix, gitutil.RootDirOpt(path))
	defer func() {
		if err := scm.AddOrReplaceRemote("origin", remoteUrl); err != nil {
			jirix.Logger.Errorf("failed to set remote back to %v for project %+v", remoteUrl, p)
		}
	}()
	p.Revision = remote.Revision
	p.RemoteBranch = remote.RemoteBranch
	if err := checkoutHeadRevision(jirix, p, false); err != nil {
		return fmt.Errorf("Not able to checkout head for %s(%s): %v", p.Name, p.Path, err)
	}
	ld.localProjects[remote.ProjectKey()] = p
	return nil
}

// loadLockFile will recursively load lockfiles from dir to its parent directories until it
// reaches $JIRI_ROOT. It will only report errors on lockfiles such as unknown format or confliting data.
// All I/O related errors will be ignored.
func (ld *loader) loadLockFile(jirix *jiri.X, repoPath, dir, lockFileName, ref string) error {
	lockfile := filepath.Join(dir, lockFileName)
	lockKey := lockfile
	if repoPath != "" {
		lockKey = filepath.Join(repoPath, lockfile) + ":" + ref
	}
	if ld.lockfiles[lockKey] {
		return nil
	}

	if !(dir == "" || dir == "." || dir == jirix.Root || dir == string(filepath.Separator)) {
		if err := ld.loadLockFile(jirix, repoPath, filepath.Dir(dir), lockFileName, ref); err != nil {
			return err
		}
	}

	var data []byte
	if repoPath != "" {
		s, err := gitutil.New(jirix, gitutil.RootDirOpt(repoPath)).Show(ref, lockfile)
		if err != nil {
			// It's fine if jiri.lock cannot be find, skip this jiri.lock
			jirix.Logger.Debugf("Could not find %q in repository %q for ref %q", lockfile, repoPath, ref)
			return nil
		}
		data = []byte(s)
	} else {
		if _, err := os.Stat(lockfile); err != nil {
			if os.IsNotExist(err) {
				jirix.Logger.Debugf("could not find %q file at %q", lockFileName, lockfile)
			} else {
				jirix.Logger.Debugf("could not access %q file at %q due to error %v", lockFileName, lockfile, err)
			}
			return nil
		}
		temp, err := ioutil.ReadFile(lockfile)
		if err != nil {
			// Supress I/O errors as it is OK if a lockfile cannot be accessed.
			return nil
		}
		data = temp
	}
	if err := ld.parseLockData(jirix, data); err != nil {
		return err
	}
	if repoPath == "" {
		jirix.Logger.Debugf("loaded lockfile at %s", lockfile)
	} else {
		jirix.Logger.Debugf("loaded lockfile at %q in repository %q for ref %q", lockfile, repoPath, ref)
	}
	ld.lockfiles[lockKey] = true
	return nil
}

func (ld *loader) parseLockData(jirix *jiri.X, data []byte) error {
	projectLocks, pkgLocks, err := UnmarshalLockEntries(data)
	if err != nil {
		return err
	}

	for k, v := range projectLocks {
		if projLock, ok := ld.ProjectLocks[k]; ok {
			if projLock != v && !jirix.UsingImportOverride {
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
			if pkgLock != v && !jirix.IgnoreLockConflicts && !jirix.UsingImportOverride {
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
				if err := ld.loadLockFile(jirix, repoPath, filepath.Dir(file), jirix.LockfileName, ref); err != nil {
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
			if err := ld.loadLockFile(jirix, repoPath, filepath.Dir(file), jirix.LockfileName, ref); err != nil {
				return nil, err
			}
		}
		return m, nil
	}

	m, err := loadManifestAndLocks(jirix, file)
	if err != nil {
		return err
	}

	if jirix.UsingSnapshot && !jirix.OverrideOptional {
		// using attributes defined in snapshot file instead of
		// using predefined ones in jiri init.
		jirix.FetchingAttrs = m.Attributes
	}

	// Add override information
	if parentImport == "" {
		for _, p := range m.ProjectOverrides {
			// Reuse the MakeProjectKey function in case it is changed
			// in the future.
			key := string(p.Key())
			ld.ProjectOverrides[key] = p
		}
		for _, p := range m.ImportOverrides {
			// Reuse the MakeProjectKey function in case it is changed
			// in the future.
			key := string(p.ProjectKey())
			if !jirix.UsingImportOverride {
				jirix.UsingImportOverride = true
			}
			ld.ImportOverrides[key] = p
		}
	} else if len(m.ProjectOverrides)+len(m.ImportOverrides) > 0 {
		return fmt.Errorf("manifest %q contains overrides but was imported by %q. Overrides are allowed only in the root manifest", shortFileName(jirix.Root, repoPath, file, ref), parentImport)
	}

	// Use manifest's directory name and file name as default
	// git attributes. It will be later expanded using the
	// import relationships.
	defaultGitAttrs := func() string {
		manifestFile := file
		if repoPath != "" {
			manifestFile = filepath.Join(repoPath, manifestFile)
		}
		containingDir := filepath.Base(filepath.Dir(manifestFile))
		filename := file
		ld.importTree.filenameMap[filename] = false
		return containingDir + "," + filepath.Base(file)
	}
	self := ld.importTree.getNode(repoPath, file, ref)
	self.tag = defaultGitAttrs()
	// Process remote imports.
	for _, remote := range m.Imports {
		// Apply override if it exists.
		remote, err := overrideImport(jirix, remote, ld.ProjectOverrides, ld.ImportOverrides)
		if err != nil {
			return err
		}
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

		self.addChild(ld.importTree.getNode(repoPath, remote.Manifest, ""))
		if err := ld.loadImport(jirix, nextRoot, remote.Manifest, remote.cycleKey(), cacheDirPath, pi, p, localManifest); err != nil {
			return err
		}
	}

	// Process local imports.
	for _, local := range m.LocalImports {
		nextFile := filepath.Join(filepath.Dir(file), local.File)
		self.addChild(ld.importTree.getNode(repoPath, nextFile, ref))
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
		// Apply override if it exists.
		project, err := overrideProject(jirix, project, ld.ProjectOverrides, ld.ImportOverrides)
		if err != nil {
			return err
		}
		// normalize project attributes
		project.ComputedAttributes = newAttributes(project.Attributes)
		project.Attributes = project.ComputedAttributes.String()
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
				} else if r.Revision != project.Revision {
					return fmt.Errorf("project %q found in %q defines different revision than its corresponding import tag.", key, shortFileName(jirix.Root, repoPath, file, ref))
				}
			}
		}

		if dup, ok := ld.Projects[key]; ok && !reflect.DeepEqual(dup, project) {
			// TODO(toddw): Tell the user the other conflicting file.
			return fmt.Errorf("duplicate project %q found in %q", key, shortFileName(jirix.Root, repoPath, file, ref))
		}

		// Record manifest location.
		project.ManifestPath = f

		// Associate project with importTreeNode for git attributes propagation.
		ld.importTree.projectKeyMap[key] = self

		ld.Projects[key] = project
	}

	for _, hook := range m.Hooks {
		if hook.ActionPath == "" {
			return fmt.Errorf("invalid hook %q for project %q. Please make sure you are importing project %q and this hook is in the manifest which directly/indirectly imports that project.", hook.Name, hook.ProjectName, hook.ProjectName)
		}
		key := hook.Key()
		ld.Hooks[key] = hook
	}

	for _, pkg := range m.Packages {
		// normalize package attributes.
		pkg.ComputedAttributes = newAttributes(pkg.Attributes)
		pkg.Attributes = pkg.ComputedAttributes.String()
		// Record manifest location.
		pkg.ManifestPath = f
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
						if err := updateOrCreateCache(jirix, cacheDirPath, remoteUrl, project.RemoteBranch, project.Revision, 0); err != nil {
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

func (ld *loader) GenerateGitAttributesForProjects(jirix *jiri.X) {
	ld.importTree.buildImportAttributes()
	for k, v := range ld.Projects {
		if treeNode, ok := ld.importTree.projectKeyMap[k]; ok {
			v.GitAttributes = treeNode.computedAttributes.String()
			ld.Projects[k] = v
		}
	}
	jirix.Logger.Debugf("Generated dot file: %s", ld.importTree.generateAttributeGraph())
}
