// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package project

import (
	"fmt"

	"fuchsia.googlesource.com/jiri"
	"fuchsia.googlesource.com/jiri/git"
	"fuchsia.googlesource.com/jiri/tool"
)

type ReferenceState struct {
	Name     string
	Revision string
}

type BranchState struct {
	*ReferenceState
	Tracking *ReferenceState
}

type ProjectState struct {
	Branches       []BranchState
	CurrentBranch  BranchState
	HasUncommitted bool
	HasUntracked   bool
	Project        Project
}

func setProjectState(jirix *jiri.X, state *ProjectState, checkDirty bool, ch chan<- error) {
	var err error
	g := git.NewGit(state.Project.Path)
	branches, err := g.GetAllBranchesInfo()
	if err != nil {
		ch <- err
		return
	}
	state.CurrentBranch = BranchState{
		&ReferenceState{
			Name: "",
		},
		nil,
	}
	for _, branch := range branches {
		b := BranchState{
			&ReferenceState{
				Name:     branch.Name,
				Revision: branch.Revision,
			},
			nil,
		}
		if branch.Tracking != nil {
			b.Tracking = &ReferenceState{
				Name:     branch.Tracking.Name,
				Revision: branch.Tracking.Revision,
			}
		}
		state.Branches = append(state.Branches, b)
		if branch.IsHead {
			state.CurrentBranch = b
		}
	}
	if state.CurrentBranch.Name == "" {
		if state.CurrentBranch.Revision, err = g.CurrentRevision(); err != nil {
			ch <- err
			return
		}
	}
	if checkDirty {
		state.HasUncommitted, err = g.HasUncommittedChanges()
		if err != nil {
			ch <- fmt.Errorf("Cannot get uncommited changes for project %q: %v", state.Project.Name, err)
			return
		}
		state.HasUntracked, err = g.HasUntrackedFiles()
		if err != nil {
			ch <- fmt.Errorf("Cannot get untracked changes for project %q: %v", state.Project.Name, err)
			return
		}
	}
	ch <- nil
}

func GetProjectStates(jirix *jiri.X, projects Projects, checkDirty bool) (map[ProjectKey]*ProjectState, error) {
	jirix.TimerPush("Get project states")
	defer jirix.TimerPop()
	states := make(map[ProjectKey]*ProjectState, len(projects))
	sem := make(chan error, len(projects))
	for key, project := range projects {
		state := &ProjectState{
			Project: project,
		}
		states[key] = state
		// jirix is not threadsafe, so we make a clone for each goroutine.
		go setProjectState(jirix.Clone(tool.ContextOpts{}), state, checkDirty, sem)
	}
	for _ = range projects {
		err := <-sem
		if err != nil {
			return nil, err
		}
	}
	return states, nil
}

func GetProjectState(jirix *jiri.X, project Project, checkDirty bool) (*ProjectState, error) {
	sem := make(chan error, 1)
	state := &ProjectState{
		Project: project,
	}
	setProjectState(jirix, state, checkDirty, sem)
	return state, <-sem
}
