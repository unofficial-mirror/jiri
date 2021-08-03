// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gitutil

type CheckoutOpt interface {
	checkoutOpt()
}

type CloneOpt interface {
	cloneOpt()
}

type CommitOpt interface {
	commitOpt()
}
type DeleteBranchOpt interface {
	deleteBranchOpt()
}
type FetchOpt interface {
	fetchOpt()
}
type MergeOpt interface {
	mergeOpt()
}
type PushOpt interface {
	pushOpt()
}
type ResetOpt interface {
	resetOpt()
}

type RebaseOpt interface {
	rebaseOpt()
}

type SubmoduleUpdateOpt interface {
	submoduleUpdateOpt()
}

type FollowTagsOpt bool

func (FollowTagsOpt) pushOpt() {}

type ForceOpt bool

func (ForceOpt) checkoutOpt()     {}
func (ForceOpt) deleteBranchOpt() {}
func (ForceOpt) pushOpt()         {}

type DetachOpt bool

func (DetachOpt) checkoutOpt() {}

type MessageOpt string

func (MessageOpt) commitOpt() {}

type ModeOpt string

func (ModeOpt) resetOpt() {}

type ResetOnFailureOpt bool

func (ResetOnFailureOpt) mergeOpt() {}

type SquashOpt bool

func (SquashOpt) mergeOpt() {}

type StrategyOpt string

func (StrategyOpt) mergeOpt() {}

type FfOnlyOpt bool

func (FfOnlyOpt) mergeOpt() {}

type TagsOpt bool

func (TagsOpt) fetchOpt() {}

type FetchTagOpt string

func (FetchTagOpt) fetchOpt() {}

type AllOpt bool

func (AllOpt) fetchOpt() {}

type PruneOpt bool

func (PruneOpt) fetchOpt() {}

type DepthOpt int

func (DepthOpt) fetchOpt() {}

type UpdateShallowOpt bool

func (UpdateShallowOpt) fetchOpt() {}

type VerifyOpt bool

func (VerifyOpt) pushOpt() {}

type SharedOpt bool

func (SharedOpt) cloneOpt() {}

type ReferenceOpt string

func (ReferenceOpt) cloneOpt() {}

type NoCheckoutOpt bool

func (NoCheckoutOpt) cloneOpt() {}

func (DepthOpt) cloneOpt() {}

type BareOpt bool

func (BareOpt) cloneOpt() {}

type OmitBlobsOpt bool

func (OmitBlobsOpt) cloneOpt() {}

type RebaseMerges bool

func (RebaseMerges) rebaseOpt() {}

type UpdateHeadOkOpt bool

func (UpdateHeadOkOpt) fetchOpt() {}

type OffloadPackfilesOpt bool

func (OffloadPackfilesOpt) cloneOpt() {}

type RecurseSubmodulesOpt bool

func (RecurseSubmodulesOpt) cloneOpt() {}
func (RecurseSubmodulesOpt) fetchOpt() {}

type InitOpt bool

func (InitOpt) submoduleUpdateOpt() {}

type JobsOpt uint

func (JobsOpt) fetchOpt() {}
