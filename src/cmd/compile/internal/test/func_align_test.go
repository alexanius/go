// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Test GOARM64 func_align_32 functions alignment option

package test

import (
	"internal/testenv"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func buildFuncAlignmentTest(t *testing.T, dir string, env string) {
	oldEnv := os.Getenv("GOARM64")
	os.Setenv("GOARM64", env)
	defer os.Setenv("GOARM64", oldEnv)

	oldModEnv := os.Getenv("GO111MODULE")
	os.Setenv("GO111MODULE", "off")
	defer os.Setenv("GO111MODULE", oldModEnv)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Error getting wd: %v", err)
	}
	srcDir := filepath.Join(wd, "testdata/func_align")
	for _, file := range []string{"main.go", "main.s"} {
		if err := copyFile(filepath.Join(dir, file), filepath.Join(srcDir, file)); err != nil {
			t.Fatalf("Error copying %s: %v", file, err)
		}
	}
	cmd := testenv.Command(t, testenv.GoToolPath(t), "build", "-a", "-o", "m")
	cmd.Dir = dir
	cmd = testenv.CleanCmdEnv(cmd)
	t.Log(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Build test failed: %v, output:\n%s", err, out)
	}
}

func testFuncAlignment(t *testing.T, dir string, fname string, func_align int64) {
	cmd := testenv.Command(t, testenv.GoToolPath(t), "tool", "objdump", "-gnu", "-s", fname, "./m")
	cmd.Dir = dir
	cmd = testenv.CleanCmdEnv(cmd)
	startStr := "TEXT " + fname
	t.Log(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Build test failed: %v, output:\n%s", err, out)
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("Too short objdump output: %v", lines)
	}
	if !strings.HasPrefix(lines[0], startStr) {
		t.Fatalf("Wrong function name in output: %v", lines[0])
	}
	cols := strings.Fields(lines[1])
	if len(cols) < 4 {
		t.Fatalf("Wrong number of columns in output: %d", len(cols))
	}
	address, err := strconv.ParseInt(strings.TrimPrefix(cols[1], "0x"), 16, 0)
	if err != nil {
		t.Fatalf("Failed to parse address:%v, output:\n%s", err, cols[1])
	}
	if address%func_align != 0 {
		t.Fatalf("Unaligned function: %s %x\n", fname, address)
	}
	t.Log("Function " + fname + " alignment check: PASSED")
}

func TestFuncAlign(t *testing.T) {
	if runtime.GOARCH != "arm64" {
		t.Skip("Skipping test: not implemented for current arch.")
	}

	testenv.MustHaveGoRun(t)

	dir := t.TempDir()
	buildFuncAlignmentTest(t, dir, "v8.0")
	testFuncAlignment(t, dir, "main.asm_foo", 16)
	testFuncAlignment(t, dir, "main.asm_bar", 16)
	testFuncAlignment(t, dir, "main.asm_baz", 16)
	testFuncAlignment(t, dir, "main.foo", 16)
	testFuncAlignment(t, dir, "main.bar", 16)
	testFuncAlignment(t, dir, "main.baz", 16)

	dir = t.TempDir()
	buildFuncAlignmentTest(t, dir, "v8.0")
	testFuncAlignment(t, dir, "main.asm_foo", 32)
	testFuncAlignment(t, dir, "main.asm_bar", 32)
	testFuncAlignment(t, dir, "main.asm_baz", 32)
	testFuncAlignment(t, dir, "main.foo", 32)
	testFuncAlignment(t, dir, "main.bar", 32)
	testFuncAlignment(t, dir, "main.baz", 32)
}
