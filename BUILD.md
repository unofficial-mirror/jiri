# Building  Jiri

## Prerequisites
* cmake 3.7.2
* golang 1.7.3
* ninja 1.7.2
* git 2.7.4

## Get source

### Using jiri prebuilt
This method only works with linux and darwin `x86_64` systems.
The bootstrap procedure requires that you have Go 1.6 or newer and Git installed and on your `PATH`. Below command will create checkout in new folder called `fuchsia`.
```
curl -s https://raw.githubusercontent.com/fuchsia-mirror/jiri/master/scripts/bootstrap_jiri | bash -s fuchsia
cd fuchsia
export PATH=`pwd`/.jiri_root/bin:$PATH
jiri import jiri https://fuchsia.googlesource.com/manifest
jiri update
```
### Manually
Create a root folder called `fuchsia`, then use git to manually clone each of the projects mentioned in this [manifest][jiri manifest], put them in correct paths and checkout required revisions. `HEAD` should be on `origin/master` where no revision is mentioned in manifest.

## Build
Set GOPATH to `fuchsia/go`, cd into `fuchsia/go/src/fuchsia.googlesource.com/jiri` and run
```
./scripts/build.sh
```

The above command should build jiri and put it into your jiri repo root.

## Running the tests
To run jiri's tests, run the following from the `fuchsia/go` directory:
```
export GOPATH=$(pwd)
go test $(go list fuchsia.googlesource.com/jiri/... 2>/dev/null | grep -v /jiri/vendor/)
```

(The use of `grep` here excludes tests from packages below `src/fuchsia.googlesource.com/jiri/vendor/` which don't pass.)

## Known Issues

If build complains about undefined `http_parser_*` functions, please remove `http_parser` from your library path.

[jiri manifest]: https://fuchsia.googlesource.com/manifest/+/refs/heads/master/jiri "jiri manifest"
