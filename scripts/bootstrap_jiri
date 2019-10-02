#!/usr/bin/env bash
# Copyright 2015 The Vanadium Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# bootstrap_jiri initializes a root directory for jiri.  The following
# directories and files will be created:
#   <root_dir>                         - root directory (picked by user)
#   <root_dir>/.jiri_root              - root metadata directory
#   <root_dir>/.jiri_root/bin/jiri     - jiri binary
#
# The jiri sources are downloaded and built into a temp directory, which is
# always deleted when this script finishes.  The <root_dir> is deleted on any
# failure.

set -euf -o pipefail

# Jiri repo, from which we will download the jiri script wrapper.
readonly JIRI_REPO_URL="https://fuchsia.googlesource.com/jiri"

# Google Storage bucket that contains prebuilt versions of jiri.
readonly CIPD_ENDPOINT_URL="https://chrome-infra-packages.appspot.com/dl/fuchsia/tools/jiri"

# fatal prints an error message, followed by the usage string, and then exits.
fatal() {
  usage='

Usage:
   bootstrap_jiri <root_dir>

A typical bootstrap workflow looks like this:

$ curl -s https://fuchsia.googlesource.com/jiri/+/master/scripts/bootstrap_jiri?format=TEXT | base64 --decode | bash -s myroot
$ export PATH=myroot/.jiri_root/bin:$PATH
$ cd myroot
$ jiri import manifest https://example.com/manifest
$ jiri update'
  echo "ERROR: $@${usage}" 1>&2
  exit 1
}

readonly HOST_ARCH=$(uname -m)
if [ "$HOST_ARCH" == "aarch64" ]; then
  readonly ARCH="arm64"
elif [ "$HOST_ARCH" == "x86_64" ]; then
  readonly ARCH="amd64"
else
  echo "Arch not supported: $HOST_ARCH"
  exit 1
fi

# toabs converts the possibly relative argument into an absolute path.  Run in a
# subshell to avoid changing the caller's working directory.
toabs() (
  cd $(dirname $1)
  echo ${PWD}/$(basename $1)
)

# Check the <root_dir> argument is supplied.
if [[ $# -ne 1 ]]; then
  fatal "need <root_dir> argument"
fi

readonly ROOT_DIR="$(toabs $1)"
readonly BIN_DIR="${ROOT_DIR}/.jiri_root/bin"

# If ROOT_DIR doesn't already exist, we will destroy it during the exit trap,
# otherwise we will only destroy the BIN_DIR
if [[ -d "${ROOT_DIR}" ]]; then
  CLEANUP_DIR="${BIN_DIR}"
else
  CLEANUP_DIR="${ROOT_DIR}"
fi
mkdir -p "${BIN_DIR}"

# Remove the root_dir if this script fails so as to not leave the environment in a strange half-state.
trap "rm -rf -- \"${CLEANUP_DIR}\"" INT TERM EXIT

# Determine and validate the version of jiri.
HOST_OS=$(uname | tr '[:upper:]' '[:lower:]')
if [[ ${HOST_OS} == "darwin" ]]; then
  HOST_OS="mac"
fi
readonly TARGET="${HOST_OS}-${ARCH}"
readonly COMMIT_URL="${JIRI_REPO_URL}/+refs/heads/master?format=JSON"
readonly LOG_URL="${JIRI_REPO_URL}/+log/refs/heads/master?format=JSON"
readonly VERSION=$(curl -sSf "${COMMIT_URL}" | sed -n 's/.*"value": "\([0-9a-f]\{40\}\)"/\1/p')
readonly VERSIONS=$(curl -sSf "${LOG_URL}" | sed -n 's/.*"commit": "\([0-9a-f]\{40\}\)".*/\1/p')
readonly TEMP_FILE=$(mktemp)

JIRI_URL=""
for version in ${VERSIONS}; do
  url="${CIPD_ENDPOINT_URL}/${TARGET}/+/git_revision:${version}"
  if curl --output /dev/null --silent --fail "${url}"; then
    JIRI_URL="${url}"
	break
  fi
done

if [[ -z "${JIRI_URL}" ]]; then
  echo "Cannot find prebuilt Jiri binary." 1>&2
  exit 1
fi

if ! curl -sfL -o "${TEMP_FILE}" "${JIRI_URL}"; then
  echo "Failed downloading prebuilt Jiri archive." 1>&2
  exit 1
fi

if ! unzip -o ${TEMP_FILE} jiri -d ${BIN_DIR}  > /dev/null; then
  echo "Failed unarchiving Jiri"
  rm -rf ${TEMP_FILE}
  exit 1
fi

rm -rf ${TEMP_FILE}
chmod 755 "${BIN_DIR}/jiri"
if [ "$ARCH" == "arm64" ]; then
  echo "WARNING: Jiri doesn't support timely updates for arch '$HOST_ARCH'. This or future binaries of Jiri might be out of date."
fi

# Install cipd, which is frequently needed to use manifests.
pushd "${BIN_DIR}" > /dev/null
if ! "${BIN_DIR}/jiri" bootstrap cipd; then
  echo "Running jiri bootstrap failed." 1>&2
  popd > /dev/null
  exit 1
fi
popd > /dev/null

echo "Please add ${BIN_DIR} to your PATH"

trap - EXIT
