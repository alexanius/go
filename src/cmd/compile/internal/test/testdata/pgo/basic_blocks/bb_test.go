// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// WARNING: Please avoid updating this file. If this file needs to be updated,
// then a new inline_hot.pprof file should be generated:
//
//  $ cd $GOROOT/src/cmd/compile/internal/test/testdata/pgo/basic_blocks
//  $ go test -bench=. -count=5 -cpuprofile ./bb_test.pprof

package main

import (
	"math"
	"testing"
)

// Just a global for accumulating results
var Acc int

// This is frequent pattern of nil-check in the start of a function
// and return if nil. In this case we should detect, that the rest
// of the function is never executed
//go:noinline
func testIf1(n *int) {
	if n == nil {
		// We check, that this branch has non-zero counter
		return
	}
	// This branch has zero counter
	println("Should not be here")
	Acc++
}

// Similar pattern as above, but the return is with 0.5 probability
// The end of the function should be no-zero
//go:noinline
func testIf2(n int) {
	if n % 2 != 0 {
		return
	}
	Acc++
}

// Test with only one executed branch
//go:noinline
func testFor1(v bool, a, b []int) int {
	s := b[len(b)-1] + a[len(a)-1]
	for i := range a {
		if v {
			s += a[i] + b[i]
		} else {
			s += i + 13
		}
	}
	return s
}

// Same as above, but with AST-corrections. Should not loose counters during nodes replacement
//go:noinline
func testFor2(v bool, a, b []int) int {
	s := b[len(a)-1] + a[len(a)-1]
	for i := range a {
		if v {
			s += a[i] + b[i] + int(math.Sqrt(float64(i)))
		} else {
			s += i + 13
		}
	}

	return s

}

// Same as above, but with another branch
//go:noinline
func testFor3(v bool, a, b []int) int {
	s := b[len(a)-1] + a[len(a)-1]
	for i := range a {
		if v {
			s += a[i] + b[i] + int(math.Sqrt(float64(i)))
		} else {
			s += i + 13
		}
	}

	return s

}

// This function should be inlined
func funcToInline1(v bool, a []int, i int) int {
	if v {
		s := a[i] * int(math.Sqrt(float64(i)))
		s += a[i] * int(math.Sqrt(float64(i)))
		s += a[i] * int(math.Sqrt(float64(i)))
		s += a[i] * int(math.Sqrt(float64(i)))
		s += a[i] * int(math.Sqrt(float64(i)))
	} else {
		return i - 12
	}
	return 0
}

// This function should be inlined
func funcToInline2(v bool, a []int, i int) int {
	return 0
}

// Test for counters of inlined function
//go:noinline
func testInline1(v bool, a []int) int {
	s := a[len(a)-1]
	for i := range a {
		s += funcToInline1(v, a, i)
	}

	return s

}

// This test inlines two same functions. One of them should have zero counter
//go:noinline
func testInline2(v bool, a []int) int {
	s := a[len(a)-1]
	for i := range a {
		if v {
			s += funcToInline1(v, a, i)
			s += a[i] / int(a[i] * 2 + 1)
			s += funcToInline2(v, a, i)
		} else {
			// Always zero counter
			s -= funcToInline1(v, a, i)
		}
	}

	return s

}

// This test checks that nodes of the function inlined twice have different
// line positions
//go:noinline
func testInline3(v bool, a []int) int {
	s := a[len(a)-1]
	const prob = 10
	for i := range a {
		s += funcToInline1(v, a, i)

		if i%prob == 0 {
			s += funcToInline1(v, a, i)
		}
	}

	return s
}

func BenchmarkBBProfIf(b *testing.B) {
	for i := 0; i < b.N; i++ {
			testIf1(nil)
			testIf2(i)
		}
}

func BenchmarkBasicBlockProfFor(b *testing.B) {
	size := 100000
	for i := 0; i < b.N; i++ {
		a := make([]int, size)
		b := make([]int, size)
		Acc += testFor1(true, a, b)
		Acc += testFor2(true, a, b)
		Acc += testFor3(false, a, b)
	}
}

func BenchmarkBBProfInline(b *testing.B) {
	size := 100000
	for i := 0; i < b.N; i++ {
		a := make([]int, size)
		Acc += testInline1(true, a)
		Acc += testInline2(true, a)
		Acc += testInline3(true, a)
	}
}
