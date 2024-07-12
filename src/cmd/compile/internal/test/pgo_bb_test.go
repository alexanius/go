// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package test

import (
	"bufio"
	"bytes"
	"internal/testenv"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

const bbProfFile = "bb_test.pprof"

type checkPair struct {
	r *regexp.Regexp // Pattern to check the dump
	b bool           // Flag that the pattern was found
}

var testIf1DumpPatterns = []*checkPair{
	// AST checks
	// Check, that if has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*IF.*# bb_test.go`), false},
	// Check, that condition has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*EQ .*# bb_test.go`), false},
	// Check, that return has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*RETURN tc\(1\) .*# bb_test.go`), false},
	// Check, that statements after if have zero counter
	{regexp.MustCompile(`0 \..*BLOCK-List`), false},
	// Check, that assign has zero counter
	{regexp.MustCompile(`0 \..*AS tc\(1\) # bb_test.go`), false},
}

var testIf2DumpPatterns = []*checkPair{
	// AST checks
	// Check, that if has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*IF.*# bb_test.go`), false},
	// Check, that condition has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*NE .*# bb_test.go`), false},
	// Check, that return has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*RETURN tc\(1\) .*# bb_test.go`), false},
	// Check, that assign has zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*AS tc\(1\) # bb_test.go`), false},
}

var testFor1DumpPatterns = []*checkPair{
	// AST checks
	// Check, that for has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*FOR-init`), false},
	// Check, that for cond has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*FOR-Cond`), false},
	// Check, that for body has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*FOR-Body`), false},
	// Check, that if body has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*IF-Body`), false},
	// Check, that if else has zero counter
	{regexp.MustCompile(`0 \..*IF-Else`), false},
	// Check, that return has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*RETURN.*# bb_test.go`), false},
}

var testFor2DumpPatterns = []*checkPair{
	// AST checks
	// Check, that for has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*FOR-init`), false},
	// Check, that for cond has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*FOR-Cond`), false},
	// Check, that for body has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*FOR-Body`), false},
	// Check, that if body has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*IF-Body`), false},
	// Check, that if else has zero counter
	{regexp.MustCompile(`0 \..*IF-Else`), false},
	// Check, that return has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*RETURN.*# bb_test.go`), false},
}

var testFor3DumpPatterns = []*checkPair{
	// AST checks
	// Check, that for has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*FOR-init`), false},
	// Check, that for cond has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*FOR-Cond`), false},
	// Check, that for body has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*FOR-Body`), false},
	// Check, that if body has zero counter
	{regexp.MustCompile(`0* \..*IF-Body`), false},
	// Check, that if else has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*IF-Else`), false},
	// Check, that return has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*RETURN.*# bb_test.go`), false},
}

var testInline1DumpPatterns = []*checkPair{
	// AST checks
	// Check, that assign in first branch has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*AS tc\(1\).*bb_test.go:94`), false},
	// Check, that assign in second branch has zero counter
	{regexp.MustCompile(`0* \..*SUB.*bb_test.go:96`), false},
}

func buildBBPGOInliningTest(t *testing.T, dir, pprof, dumpFunc string) []byte {
	// Add a go.mod so we have a consistent symbol names in this temp dir.
	exe := filepath.Join(dir, "test.exe")
	args := []string{"test", "-a", "-c", "-o", exe, "-pgobb", "-pgo="+pprof,"bb_test.go"}
	cmd := testenv.Command(t, testenv.GoToolPath(t), args...)
	cmd.Dir = dir
	cmd = testenv.CleanCmdEnv(cmd)
	cmd.Env = append(cmd.Env, "GOSSAFUNC="+dumpFunc+"+")
	t.Log(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v, output:\n%s", err, out)
	}
	return out
}

func init() {
	// Assembly checks
	testIf1DumpPatterns = append(testIf1DumpPatterns, &checkPair{regexp.MustCompile(`b1 \([1-9][0-9]+\).*PCDATA`), false})
	if runtime.GOARCH == "amd64" {
		testIf1DumpPatterns = append(testIf1DumpPatterns, &checkPair{regexp.MustCompile(`b1 \([1-9][0-9]+\).*JNE`), false})
		testIf1DumpPatterns = append(testIf1DumpPatterns, &checkPair{regexp.MustCompile(`b3 \([1-9][0-9]+\).*RET`), false})
		testIf1DumpPatterns = append(testIf1DumpPatterns, &checkPair{regexp.MustCompile(`b2 \(0\).*RET`), false})
	} else if runtime.GOARCH == "arm64" {
		testIf1DumpPatterns = append(testIf1DumpPatterns, &checkPair{regexp.MustCompile(`b1 \([1-9][0-9]+\).*CBNZ`), false})
		testIf1DumpPatterns = append(testIf1DumpPatterns, &checkPair{regexp.MustCompile(`b3 \([1-9][0-9]+\).*RET`), false})
		testIf1DumpPatterns = append(testIf1DumpPatterns, &checkPair{regexp.MustCompile(`b2 \(0\).*RET`), false})
	}

	testIf2DumpPatterns = append(testIf2DumpPatterns, &checkPair{regexp.MustCompile(`b1 \([1-9][0-9]*\).*PCDATA`), false})
	if runtime.GOARCH == "amd64" {
		testIf2DumpPatterns = append(testIf2DumpPatterns, &checkPair{regexp.MustCompile(`b1 \([1-9][0-9]*\).*(JCS|JCC)`), false})
		testIf2DumpPatterns = append(testIf2DumpPatterns, &checkPair{regexp.MustCompile(`b3 \([1-9][0-9]*\).*RET`), false})
		testIf2DumpPatterns = append(testIf2DumpPatterns, &checkPair{regexp.MustCompile(`b2 \([1-9][0-9]*\).*RET`), false})
	} else if runtime.GOARCH == "arm64" {
		testIf2DumpPatterns = append(testIf2DumpPatterns, &checkPair{regexp.MustCompile(`b1 \([1-9][0-9]*\).*(TBNZ|TBZ)`), false})
		testIf2DumpPatterns = append(testIf2DumpPatterns, &checkPair{regexp.MustCompile(`b3 \([1-9][0-9]*\).*RET`), false})
		testIf2DumpPatterns = append(testIf2DumpPatterns, &checkPair{regexp.MustCompile(`b2 \([1-9][0-9]*\).*RET`), false})
	}

	testFor1DumpPatterns = append(testFor1DumpPatterns, &checkPair{regexp.MustCompile(`b1 \([1-9][0-9]*\).*PCDATA`), false})
	if runtime.GOARCH == "amd64" {
		testFor1DumpPatterns = append(testFor1DumpPatterns, &checkPair{regexp.MustCompile(`b1 \([1-9][0-9]*\).*JLS`), false})
		testFor1DumpPatterns = append(testFor1DumpPatterns, &checkPair{regexp.MustCompile(`b2 \([1-9][0-9]*\).*JLS`), false})
		testFor1DumpPatterns = append(testFor1DumpPatterns, &checkPair{regexp.MustCompile(`b4 \([1-9][0-9]*\).*JMP`), false})
		testFor1DumpPatterns = append(testFor1DumpPatterns, &checkPair{regexp.MustCompile(`b12 \(0\).*JMP`), false})
		testFor1DumpPatterns = append(testFor1DumpPatterns, &checkPair{regexp.MustCompile(`b9 \([1-9][0-9]*\).*RET`), false})
	} else if runtime.GOARCH == "arm64" {
		testFor1DumpPatterns = append(testFor1DumpPatterns, &checkPair{regexp.MustCompile(`b1 \([1-9][0-9]*\).*BLS`), false})
		testFor1DumpPatterns = append(testFor1DumpPatterns, &checkPair{regexp.MustCompile(`b2 \([1-9][0-9]*\).*BLS`), false})
		testFor1DumpPatterns = append(testFor1DumpPatterns, &checkPair{regexp.MustCompile(`b4 \([1-9][0-9]*\).*JMP`), false})
		testFor1DumpPatterns = append(testFor1DumpPatterns, &checkPair{regexp.MustCompile(`b12 \(0\).*JMP`), false})
		testFor1DumpPatterns = append(testFor1DumpPatterns, &checkPair{regexp.MustCompile(`b9 \([1-9][0-9]*\).*RET`), false})
	}

	testFor2DumpPatterns = append(testFor2DumpPatterns, &checkPair{regexp.MustCompile(`b1 \([1-9][0-9]*\).*PCDATA`), false})
	if runtime.GOARCH == "amd64" {
		testFor2DumpPatterns = append(testFor2DumpPatterns, &checkPair{regexp.MustCompile(`b1 \([1-9][0-9]*\).*JLS`), false})
		testFor2DumpPatterns = append(testFor2DumpPatterns, &checkPair{regexp.MustCompile(`b4 \([1-9][0-9]*\).*JMP`), false})
		testFor2DumpPatterns = append(testFor2DumpPatterns, &checkPair{regexp.MustCompile(`b6 \([1-9][0-9]*\).*JLE`), false})
		testFor2DumpPatterns = append(testFor2DumpPatterns, &checkPair{regexp.MustCompile(`b12 \(0\).*JMP`), false})
		testFor2DumpPatterns = append(testFor2DumpPatterns, &checkPair{regexp.MustCompile(`b16 \([1-9][0-9]*\).*JMP`), false})
		testFor2DumpPatterns = append(testFor2DumpPatterns, &checkPair{regexp.MustCompile(`b9 \([1-9][0-9]*\).*RET`), false})
	} else if runtime.GOARCH == "arm64" {
		testFor2DumpPatterns = append(testFor2DumpPatterns, &checkPair{regexp.MustCompile(`b1 \([1-9][0-9]*\).*BLS`), false})
		testFor2DumpPatterns = append(testFor2DumpPatterns, &checkPair{regexp.MustCompile(`b4 \([1-9][0-9]*\).*JMP`), false})
		testFor2DumpPatterns = append(testFor2DumpPatterns, &checkPair{regexp.MustCompile(`b6 \([1-9][0-9]*\).*BLE`), false})
		testFor2DumpPatterns = append(testFor2DumpPatterns, &checkPair{regexp.MustCompile(`b12 \(0\).*JMP`), false})
		testFor2DumpPatterns = append(testFor2DumpPatterns, &checkPair{regexp.MustCompile(`b16 \([1-9][0-9]*\).*JMP`), false})
		testFor2DumpPatterns = append(testFor2DumpPatterns, &checkPair{regexp.MustCompile(`b9 \([1-9][0-9]*\).*RET`), false})
	}

	testFor3DumpPatterns = append(testFor3DumpPatterns, &checkPair{regexp.MustCompile(`b1 \([1-9][0-9]*\).*PCDATA`), false})
	if runtime.GOARCH == "amd64" {
		testFor3DumpPatterns = append(testFor3DumpPatterns, &checkPair{regexp.MustCompile(`b1 \([1-9][0-9]*\).*JLS`), false})
		testFor3DumpPatterns = append(testFor3DumpPatterns, &checkPair{regexp.MustCompile(`b4 \([1-9][0-9]*\).*JMP`), false})
		testFor3DumpPatterns = append(testFor3DumpPatterns, &checkPair{regexp.MustCompile(`b6 \([1-9][0-9]*\).*JLE`), false})
		testFor3DumpPatterns = append(testFor3DumpPatterns, &checkPair{regexp.MustCompile(`b12 \([1-9][0-9]*\).*JMP`), false})
		testFor3DumpPatterns = append(testFor3DumpPatterns, &checkPair{regexp.MustCompile(`b16 \(0\).*JMP`), false})
		testFor3DumpPatterns = append(testFor3DumpPatterns, &checkPair{regexp.MustCompile(`b9 \([1-9][0-9]*\).*RET`), false})
	} else if runtime.GOARCH == "arm64" {
		testFor3DumpPatterns = append(testFor3DumpPatterns, &checkPair{regexp.MustCompile(`b1 \([1-9][0-9]*\).*BLS`), false})
		testFor3DumpPatterns = append(testFor3DumpPatterns, &checkPair{regexp.MustCompile(`b4 \([1-9][0-9]*\).*JMP`), false})
		testFor3DumpPatterns = append(testFor3DumpPatterns, &checkPair{regexp.MustCompile(`b6 \([1-9][0-9]*\).*BLE`), false})
		testFor3DumpPatterns = append(testFor3DumpPatterns, &checkPair{regexp.MustCompile(`b12 \([1-9][0-9]*\).*JMP`), false})
		testFor3DumpPatterns = append(testFor3DumpPatterns, &checkPair{regexp.MustCompile(`b16 \(0\).*JMP`), false})
		testFor3DumpPatterns = append(testFor3DumpPatterns, &checkPair{regexp.MustCompile(`b9 \([1-9][0-9]*\).*RET`), false})
	}

	if runtime.GOARCH == "amd64" {
		testInline1DumpPatterns = append(testInline1DumpPatterns, &checkPair{regexp.MustCompile(`b12 \([1-9][0-9]*\).*JMP`), false})
		testInline1DumpPatterns = append(testInline1DumpPatterns, &checkPair{regexp.MustCompile(`b10 \(0\).*JMP`), false})
	} else if runtime.GOARCH == "arm64" {
		testInline1DumpPatterns = append(testInline1DumpPatterns, &checkPair{regexp.MustCompile(`b12 \([1-9][0-9]*\).*JMP`), false})
		testInline1DumpPatterns = append(testInline1DumpPatterns, &checkPair{regexp.MustCompile(`b10 \(0\).*JMP`), false})
	}
}

// checkBBPGODumps checks that function dump all the patterns from regExprs
func checkBBPGODumps(t *testing.T, out []byte, regExprs []*checkPair, funcName string) {
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		for _, rr := range regExprs {
			if m := rr.r.FindStringSubmatch(line); m != nil {
				rr.b = true
			}
		}
	}

	res := true
	for _, rr := range regExprs {
		if !rr.b {
			t.Logf("Failed check for function '%s' with regexp: '%s'\n", funcName, rr.r.String())
			res = false
		}
	}

	if !res {
		t.Logf(string(out))
		t.Fatalf("Error loading counters to basic blocks for function '%s'\n", funcName)
	}
}

// TestPGOBasicBlocks tests that compiler loads profile-guided information
// into basic blocks correctly
func TestPGOBasicBlocks(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64"{
		// Not implemented for other arches
		return
	}

	testenv.MustHaveGoRun(t)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("error getting wd: %v", err)
	}
	srcDir := filepath.Join(wd, "testdata/pgo/basic_blocks")

	// Copy the module to a scratch location so we can add a go.mod.
	dir := t.TempDir()

	for _, file := range []string{"bb_test.go"} {
		if err := copyFile(filepath.Join(dir, file), filepath.Join(srcDir, file)); err != nil {
			t.Fatalf("error copying %s: %v", file, err)
		}
	}

	pprof := filepath.Join(dir, bbProfFile)
	args := []string{"test", "-count=5", "-cpuprofile=" + pprof, "-bench=.", "bb_test.go"}
	cmd := testenv.Command(t, testenv.GoToolPath(t), args...)
	cmd.Dir = dir
	cmd = testenv.CleanCmdEnv(cmd)
	t.Log(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("profile build failed: %v, output:\n%s", err, out)
	}

	out = buildBBPGOInliningTest(t, dir, pprof, "testIf1")
	checkBBPGODumps(t, out, testIf1DumpPatterns, "testIf1")

	out = buildBBPGOInliningTest(t, dir, pprof, "testIf2")
	checkBBPGODumps(t, out, testIf2DumpPatterns, "testIf2")

	out = buildBBPGOInliningTest(t, dir, pprof, "testFor1")
	checkBBPGODumps(t, out, testFor1DumpPatterns, "testFor1")

	out = buildBBPGOInliningTest(t, dir, pprof, "testFor2")
	checkBBPGODumps(t, out, testFor2DumpPatterns, "testFor2")

	out = buildBBPGOInliningTest(t, dir, pprof, "testFor3")
	checkBBPGODumps(t, out, testFor3DumpPatterns, "testFor3")

	out = buildBBPGOInliningTest(t, dir, pprof, "testInline1")
	checkBBPGODumps(t, out, testInline1DumpPatterns, "testInline1")
}
