# Copyright 2016 The Fuchsia Authors. All rights reserved.
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.

"""Recipe for building Jiri."""

from recipe_engine.recipe_api import Property

import os


DEPS = [
    'recipe_engine/path',
    'recipe_engine/properties',
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
    'target': Property(kind=str, help='Target to build'),
}


def RunSteps(api, category, repo_url, refspec, gerrit_host, gerrit_project,
             gerrit_patch_ref):
    if category == 'cq':
        assert gerrit_host.startswith('https://')
        repo_url = '%s/%s' % (gerrit_host.rstrip('/'), gerrit_project)
        refspec = gerrit_patch_ref

    assert repo_url and refspec, 'repository url and refspec must be given'
    assert repo_url.startswith('https://')

    assert 'checkout' not in api.path
    checkout = api.path['cwd'].join('go', 'src', 'fuchsia.googlesource.com',
                                    'jiri')
    api.path['checkout'] = checkout

    with api.step.nest('clean'):
        api.step('rm', ['rm', '-rf', api.path['checkout']])
        api.step('mkdir', ['mkdir', '-p', api.path['checkout']])

    with api.step.context({'cwd': api.path['checkout']}):
        api.step('git init', ['git', 'init'])
        api.step('git reset', ['git', 'reset', '--hard'])
        api.step('git fetch', ['git', 'fetch', repo_url, '%s' % refspec])
        api.step('git checkout', ['git', 'checkout', 'FETCH_HEAD'])


def GenTests(api):
    yield api.test('ci') + api.properties(
        repository='https://fuchsia.googlesource.com/jiri',
    )
    yield api.test('cq_try') + api.properties.tryserver_gerrit(
        full_project_name='magenta',
    )
