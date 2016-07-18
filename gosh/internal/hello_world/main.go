// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"

	"fuchsia.googlesource.com/jiri/gosh"
)

func main() {
	gosh.InitChildMain()
	fmt.Println("Hello, world!")
}
