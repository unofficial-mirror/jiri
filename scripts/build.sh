#!/usr/bin/env bash
# Copyright 2017 The Fuchsia Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly GIT_DIR="$(dirname "${SCRIPT_DIR}")"

readonly PKG_PATH="fuchsia.googlesource.com/jiri"

# These are embedded directly into jiri itself and are available through `jiri version`.
readonly GIT_COMMIT=$(git --git-dir="${GIT_DIR}/.git" --work-tree="${GIT_DIR}" rev-parse HEAD)
readonly BUILD_TIME=$(python -c "import datetime; print datetime.datetime.utcnow().isoformat()")

readonly CMAKE_PROGRAM=${CMAKE_PROGRAM:-cmake}
readonly NINJA_PROGRAM=${NINJA_PROGRAM:-ninja}

if [[ -n "${GO_PROGRAM}" ]]; then
  readonly CMAKE_EXTRA_ARGS="-DGO_EXECUTABLE=${GO_PROGRAM}"
  export GOROOT="$(dirname "$(dirname ${GO_PROGRAM})")"
fi

BORINGSSL_SRC="${GIT_DIR}/vendor/github.com/libgit2/git2go/vendor/boringssl"
BORINGSSL_BUILD="${BORINGSSL_SRC}/build"
mkdir -p -- "${BORINGSSL_BUILD}"
pushd "${BORINGSSL_BUILD}"
[[ -f "${BORINGSSL_BUILD}/build.ninja" ]] || ${CMAKE_PROGRAM} -GNinja \
  -DCMAKE_MAKE_PROGRAM=${NINJA_PROGRAM} \
  -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_C_FLAGS=-fPIC \
  ${CMAKE_EXTRA_ARGS:-} \
  ..
${NINJA_PROGRAM}
popd

LIBSSH2_SRC="${GIT_DIR}/vendor/github.com/libgit2/git2go/vendor/libssh2"
LIBSSH2_BUILD="${LIBSSH2_SRC}/build"
mkdir -p -- "${LIBSSH2_BUILD}"
pushd "${LIBSSH2_BUILD}"
[[ -f "${LIBSSH2_BUILD}/build.ninja" ]] || ${CMAKE_PROGRAM} -GNinja \
  -DCMAKE_MAKE_PROGRAM=${NINJA_PROGRAM} \
  -DCMAKE_BUILD_TYPE=Release \
  -DBUILD_SHARED_LIBS=OFF \
  -DENABLE_ZLIB_COMPRESSION=ON \
  -DBUILD_EXAMPLES=OFF \
  -DBUILD_TESTING=OFF \
  -DCRYPTO_BACKEND=OpenSSL \
  -DOPENSSL_INCLUDE_DIR="${BORINGSSL_SRC}/include" \
  -DOPENSSL_SSL_LIBRARY="${BORINGSSL_BUILD}/ssl/libssl.a" \
  -DOPENSSL_CRYPTO_LIBRARY="${BORINGSSL_BUILD}/crypto/libcrypto.a" \
  ..
${NINJA_PROGRAM}
popd

CURL_SRC="${GIT_DIR}/vendor/github.com/libgit2/git2go/vendor/curl"
CURL_BUILD="${CURL_SRC}/build"
mkdir -p -- "${CURL_BUILD}"
pushd "${CURL_BUILD}"
[[ -f "${CURL_BUILD}/build.ninja" ]] || ${CMAKE_PROGRAM} -GNinja \
  -DCMAKE_MAKE_PROGRAM=${NINJA_PROGRAM} \
  -DCMAKE_BUILD_TYPE=Release \
  -DBUILD_CURL_EXE=OFF \
  -DBUILD_TESTING=OFF \
  -DCURL_STATICLIB=ON \
  -DHTTP_ONLY=ON \
  -DCMAKE_USE_OPENSSL=ON \
  -DCMAKE_USE_LIBSSH2=OFF \
  -DENABLE_UNIX_SOCKETS=OFF \
  -DOPENSSL_INCLUDE_DIR="${BORINGSSL_SRC}/include" \
  -DOPENSSL_SSL_LIBRARY="${BORINGSSL_BUILD}/ssl/libssl.a" \
  -DOPENSSL_CRYPTO_LIBRARY="${BORINGSSL_BUILD}/crypto/libcrypto.a" \
  -DHAVE_OPENSSL_ENGINE_H=OFF \
  ..
${NINJA_PROGRAM}
popd

LIBGIT2_SRC="${GIT_DIR}/vendor/github.com/libgit2/git2go/vendor/libgit2"
LIBGIT2_BUILD="${LIBGIT2_SRC}/build"
mkdir -p "${LIBGIT2_BUILD}"
pushd "${LIBGIT2_BUILD}"
[[ -f "${LIBGIT2_BUILD}/build.ninja" ]] || ${CMAKE_PROGRAM} -GNinja \
  -DCMAKE_MAKE_PROGRAM=${NINJA_PROGRAM} \
  -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_C_FLAGS=-fPIC \
  -DTHREADSAFE=ON \
  -DBUILD_CLAR=OFF \
  -DBUILD_SHARED_LIBS=OFF \
  -DOPENSSL_INCLUDE_DIR="${BORINGSSL_SRC}/include" \
  -DOPENSSL_SSL_LIBRARY="${BORINGSSL_BUILD}/ssl/libssl.a" \
  -DOPENSSL_CRYPTO_LIBRARY="${BORINGSSL_BUILD}/crypto/libcrypto.a" \
  -DCURL_INCLUDE_DIRS="${CURL_BUILD}/include/curl;${CURL_SRC}/include" \
  -DCURL_LIBRARIES="${CURL_BUILD}/libcurl.a" \
  ..
${NINJA_PROGRAM}
popd

# Build Jiri
export GOPATH="$(cd ${GIT_DIR}/../../.. && pwd)"
${GO_PROGRAM:-go} build -ldflags "-X \"${PKG_PATH}/version.GitCommit=${GIT_COMMIT}\" -X \"${PKG_PATH}/version.BuildTime=${BUILD_TIME}\"" -a -o "jiri" "${PKG_PATH}/cmd/jiri"
