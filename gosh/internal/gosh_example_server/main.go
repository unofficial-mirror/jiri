// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fuchsia.googlesource.com/jiri/gosh"
	"fuchsia.googlesource.com/jiri/gosh/internal/gosh_example_lib"
)

func main() {
	gosh.InitChildMain()
	lib.Serve()
}
