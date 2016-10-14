# Copyright 2016 The Fuchsia Authors. All rights reserved.
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.

"""Recipe for building Jiri."""

from recipe_engine.recipe_api import Property
from recipe_engine import config

import datetime


DEPS = [
    'infra/jiri',
    'infra/git',
    'infra/go',
    'recipe_engine/path',
    'recipe_engine/properties',
    'recipe_engine/raw_io',
    'recipe_engine/step',
]

PROPERTIES = {
    'gerrit': Property(kind=str, help='Gerrit host', default=None,
                       param_name='gerrit_host'),
    'patch_project': Property(kind=str, help='Gerrit project', default=None,
                              param_name='gerrit_project'),
    'event.patchSet.ref': Property(kind=str, help='Gerrit patch ref',
                                   default=None, param_name='gerrit_patch_ref'),
    'repository': Property(kind=str, help='Full url to a Git repository',
                           default=None, param_name='repo_url'),
    'refspec': Property(kind=str, help='Refspec to checkout', default='master'),
    'category': Property(kind=str, help='Build category', default=None),
    'manifest': Property(kind=str, help='Jiri manifest to use'),
    'remote': Property(kind=str, help='Remote manifest repository'),
    'target': Property(kind=str, help='Target to build'),
}


def RunSteps(api, category, repo_url, refspec, gerrit_host, gerrit_project,
             gerrit_patch_ref, manifest, remote, target):
    if category == 'cq':
        assert gerrit_host.startswith('https://')
        repo_url = '%s/%s' % (gerrit_host.rstrip('/'), gerrit_project)
        refspec = gerrit_patch_ref

    assert repo_url and refspec, 'repository url and refspec must be given'
    assert repo_url.startswith('https://')

    api.jiri.ensure_jiri()
    api.jiri.set_config('jiri')

    api.jiri.init()
    api.jiri.clean_project()
    api.jiri.import_manifest(manifest, remote, overwrite=True)
    api.jiri.update(gc=True)
    if category == 'cq':
        api.jiri.patch(gerrit_patch_ref, host=gerrit_host, delete=True, force=True)

    api.go.install_go()

    gitdir = api.path['slave_build'].join(
        'go', 'src', 'fuchsia.googlesource.com', 'jiri')
    git_commit = api.git.get_hash(cwd=gitdir)
    result = api.step('date', ['date', '--rfc-3339=seconds'],
        stdout=api.raw_io.output(),
        step_test_data=lambda:
            api.raw_io.test_api.stream_output('2016-10-11 14:40:25-07:00'))
    build_time = result.stdout.strip()

    ldflags = "-X \"fuchsia.googlesource.com/jiri/version.GitCommit=%s\" -X \"fuchsia.googlesource.com/jiri/version.BuildTime=%s\"" % (git_commit, build_time)
    gopath = api.path['slave_build'].join('go')
    goos, goarch = target.split("-", 2)

    with api.step.context({'env': {'GOPATH': gopath, 'GOOS': goos, 'GOARCH': goarch}}):
        api.go('build', '-ldflags', ldflags, '-a',
               'fuchsia.googlesource.com/jiri/cmd/jiri')

    with api.step.context({'env': {'GOPATH': gopath}}):
        api.go('test', 'fuchsia.googlesource.com/jiri/cmd/jiri')


def GenTests(api):
    yield api.test('scheduler') + api.properties(
        repository='https://fuchsia.googlesource.com/jiri',
        refspec='master',
        manifest='jiri',
        remote='https://fuchsia.googlesource.com/manifest',
        target='linux-amd64',
    )
    yield api.test('cq_try') + api.properties.tryserver_gerrit(
        'jiri',
        gerrit_host='https://fuchsia-review.googlesource.com',
        manifest='jiri',
        remote='https://fuchsia.googlesource.com/manifest',
        target='linux-amd64',
    )
