// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package test

import (
	"bufio"
	"bytes"
	"fmt"
	"internal/testenv"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

const bbProfFile = "bb_test.pprof"
const bbProfPkg = "example.com/pgo/basic_blocks"

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

	// Assembly checks
	{regexp.MustCompile(`b1 \([1-9][0-9]+\).*PCDATA`), false},
	{regexp.MustCompile(`b1 \([1-9][0-9]+\).*JNE`), false},
	{regexp.MustCompile(`b3 \([1-9][0-9]+\).*RET`), false},
	{regexp.MustCompile(`b2 \(0\).*RET`), false},
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

	// Assembly checks
	{regexp.MustCompile(`b1 \([1-9][0-9]*\).*PCDATA`), false},
	{regexp.MustCompile(`b1 \([1-9][0-9]*\).*(JCS|JCC)`), false},
	{regexp.MustCompile(`b3 \([1-9][0-9]*\).*RET`), false},
	{regexp.MustCompile(`b2 \([1-9][0-9]*\).*RET`), false},
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

	// Assembly checks
	{regexp.MustCompile(`b1 \([1-9][0-9]*\).*PCDATA`), false},
	{regexp.MustCompile(`b1 \([1-9][0-9]*\).*JLS`), false},
	{regexp.MustCompile(`b2 \([1-9][0-9]*\).*JLS`), false},
	{regexp.MustCompile(`b4 \([1-9][0-9]*\).*JMP`), false},
	{regexp.MustCompile(`b12 \(0\).*JMP`), false},
	{regexp.MustCompile(`b9 \([1-9][0-9]*\).*RET`), false},
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

	// Assembly checks
	{regexp.MustCompile(`b1 \([1-9][0-9]*\).*PCDATA`), false},
	{regexp.MustCompile(`b1 \([1-9][0-9]*\).*JLS`), false},
	{regexp.MustCompile(`b4 \([1-9][0-9]*\).*JMP`), false},
	{regexp.MustCompile(`b6 \([1-9][0-9]*\).*JLE`), false},
	{regexp.MustCompile(`b12 \(0\).*JMP`), false},
	{regexp.MustCompile(`b16 \([1-9][0-9]*\).*JMP`), false},
	{regexp.MustCompile(`b9 \([1-9][0-9]*\).*RET`), false},
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

	// Assembly checks
	{regexp.MustCompile(`b1 \([1-9][0-9]*\).*PCDATA`), false},
	{regexp.MustCompile(`b1 \([1-9][0-9]*\).*JLS`), false},
	{regexp.MustCompile(`b4 \([1-9][0-9]*\).*JMP`), false},
	{regexp.MustCompile(`b6 \([1-9][0-9]*\).*JLE`), false},
	{regexp.MustCompile(`b12 \([1-9][0-9]*\).*JMP`), false},
	{regexp.MustCompile(`b16 \(0\).*JMP`), false},
	{regexp.MustCompile(`b9 \([1-9][0-9]*\).*RET`), false},
}

var testInline1DumpPatterns = []*checkPair{
	// AST checks
	// Check, that assign in first branch has non-zero counter
	{regexp.MustCompile(`[1-9][0-9]* \..*AS tc\(1\).*bb_test.go:94`), false},
	// Check, that assign in second branch has zero counter
	{regexp.MustCompile(`0* \..*SUB.*bb_test.go:96`), false},

	// Assembly checks
	{regexp.MustCompile(`b12 \([1-9][0-9]*\).*JMP`), false},
	{regexp.MustCompile(`b10 \(0\).*JMP`), false},
}

func buildBBPGOInliningTest(t *testing.T, dir, gcflag, dumpFunc string) []byte {
	// Add a go.mod so we have a consistent symbol names in this temp dir.
	/*	goMod := fmt.Sprintf(`module %s
		go 1.19
		`, bbProfPkg)

			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
				t.Fatalf("error writing go.mod: %v", err)
			}*/

	exe := filepath.Join(dir, "test.exe")
	args := []string{"test", "-c", "-o", exe, "-gcflags=" + gcflag, "bb_test.go"}
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

	// build with -trimpath so the source location (thus the hash)
	// does not depend on the temporary directory path.
	gcflag := fmt.Sprintf("-pgobbprofile -pgoprofile=%s -trimpath %s=>%s", pprof, dir, bbProfPkg)

	out = buildBBPGOInliningTest(t, dir, gcflag, "testIf1")
	checkBBPGODumps(t, out, testIf1DumpPatterns, "testIf1")

	out = buildBBPGOInliningTest(t, dir, gcflag, "testIf2")
	checkBBPGODumps(t, out, testIf2DumpPatterns, "testIf2")

	out = buildBBPGOInliningTest(t, dir, gcflag, "testFor1")
	checkBBPGODumps(t, out, testFor1DumpPatterns, "testFor1")

	out = buildBBPGOInliningTest(t, dir, gcflag, "testFor2")
	checkBBPGODumps(t, out, testFor2DumpPatterns, "testFor2")

	out = buildBBPGOInliningTest(t, dir, gcflag, "testFor3")
	checkBBPGODumps(t, out, testFor3DumpPatterns, "testFor3")

	out = buildBBPGOInliningTest(t, dir, gcflag, "testInline1")
	checkBBPGODumps(t, out, testInline1DumpPatterns, "testInline1")
}
