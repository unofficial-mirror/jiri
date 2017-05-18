# Jiri

[TOC]

## Feedback
If you work at Google, please file a bug at [go/file-jiri-bug][file bug], or to request new features use [go/jiri-new-feature][request new feature].
If filing a bug please include output from `jiri [command]`. If you think that jiri did not update projects correctly, please also include outputs of `jiri status` and `jiri project` and if possible `jiri update -v`.

## Intended Behavior

### update {#intended-jiri-update}

* Gets latest manifest repository first, then applied below update rules to all local projects.
* Always fetches origin in all the repos except when configured using [`jiri project-config`](#intended-project-config).
* Point the tree name JIRI_HEAD to the manifest selected revision.
* Checkout new repositories at JIRI_HEAD (detached).
* Fast-forward existing repositories to JIRI_HEAD unless further conditions apply (see below).
* If local repo is on a tracked branch, it will fast forward merge to upstream changes. If merge fails, user would be shown a error.
* If project is on un-tracked branch it would be left alone and jiri will show warning.
* It will leave all other local branches as it is.
* If a project is deleted from manifest it  won't be deleted unless command is run with `-gc` flag.
* If a project contains uncommitted changes, jiri will leave it alone and will not fast-forward or merge/rebase the branches.
* Sometimes projects are pinned to particular revision in manifest, in that case if local project is on a local branch, jiri will update them according to above rules and will not throw warnings about those projects.
    * Please note that this can leave projects on revisions other than `JIRI_HEAD` which can cause build failures. In that case user can run [`jiri status`](/howdoi.md#use-jiri-status) which will output all the projects which have changes and/or are not on `JIRI_HEAD`. User can manually checkout `JIRI_HEAD` by running `git checkout JIRI_HEAD` from inside the project.
* If user doesn't want jiri to update a project, he/she can use `jiri project-config`.
* Always updates your jiri tool to latest.

#### checkout snapshot {#intended-checkout-snapshot}
Snaphot file captures current state of all the projects. It can be created using command `jiri snapshot`.
* Snapshot file or a url can be passed to `update` command to checkout snapshot.
* If project has changes, it would **not** be checked-out to snapshot version.
	* else it would be checked out to *DETACHED_HEAD* and snapshot version.
* If `project-config` specifies `ignore` or `noUpdate`, it would be ignored.
* Local branches would **not** be rebased.

### project-config {#intended-project-config}

* If `ignore` is true, jiri will completely ignore this project, ie **no** *fetch*, *update*, *move*, *clean*, *delete* or *rebase*.
* If `noUpdate` is true, jiri will  **not** *fetch*, *update*, *clean* or *rebase* the project.
* For both `ignore` and `noUpdate`, `JIRI_HEAD` is **not** updated for the project.
* If `noRebase` is true, local branches in project **won't be** *updated* or *rebased*.
* This only works with `update` and `project -clean` commands.

### project -clean {#intended-project-clean}

* Puts projects on `JIRI_HEAD`.
* Removes un-tracked files.
* if `-clean-all` flag is used, force deletes all the local branches, even **master**.

### upload {#intended-project-upload}

* Sets topic (default *User-Branch*) for each upload unless `set-topic=false` is used.
* Doesn't rebase the changes before uploading unless `-rebase` is passed.
* Uploads multipart change only when `-multipart` is passed.

### patch {#intended-patch}

* Can patch multipart and single changes.
* If topic is provided patch will try to download whole topic and patch all the affected projects, and will try to create branch derived from topic name.
* If topic is not provided default branch would be *change/{id}/{patch-set}*.
* It will **not** rebase downloaded patch-set unless `-rebase` flag is provided.





[file bug]:http://go/file-jiri-bug
[request new feature]: http://go/jiri-new-feature
