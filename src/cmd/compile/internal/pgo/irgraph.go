// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// A note on line numbers: when working with line numbers, we always use the
// binary-visible relative line number. i.e., the line number as adjusted by
// //line directives (ctxt.InnermostPos(ir.Node.Pos()).RelLine()). Use
// NodeLineOffset to compute line offsets.
//
// If you are thinking, "wait, doesn't that just make things more complex than
// using the real line number?", then you are 100% correct. Unfortunately,
// pprof profiles generated by the runtime always contain line numbers as
// adjusted by //line directives (because that is what we put in pclntab). Thus
// for the best behavior when attempting to match the source with the profile
// it makes sense to use the same line number space.
//
// Some of the effects of this to keep in mind:
//
//  - For files without //line directives there is no impact, as RelLine() ==
//    Line().
//  - For functions entirely covered by the same //line directive (i.e., a
//    directive before the function definition and no directives within the
//    function), there should also be no impact, as line offsets within the
//    function should be the same as the real line offsets.
//  - Functions containing //line directives may be impacted. As fake line
//    numbers need not be monotonic, we may compute negative line offsets. We
//    should accept these and attempt to use them for best-effort matching, as
//    these offsets should still match if the source is unchanged, and may
//    continue to match with changed source depending on the impact of the
//    changes on fake line numbers.
//  - Functions containing //line directives may also contain duplicate lines,
//    making it ambiguous which call the profile is referencing. This is a
//    similar problem to multiple calls on a single real line, as we don't
//    currently track column numbers.
//
// Long term it would be best to extend pprof profiles to include real line
// numbers. Until then, we have to live with these complexities. Luckily,
// //line directives that change line numbers in strange ways should be rare,
// and failing PGO matching on these files is not too big of a loss.

package pgo

import (
	"bufio"
	"cmd/compile/internal/base"
	"cmd/compile/internal/ir"
	"cmd/compile/internal/typecheck"
	"cmd/compile/internal/types"
	"errors"
	"fmt"
	"internal/profile"
	"io"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// IRGraph is a call graph with nodes pointing to IRs of functions and edges
// carrying weights and callsite information.
//
// Nodes for indirect calls may have missing IR (IRNode.AST == nil) if the node
// is not visible from this package (e.g., not in the transitive deps). Keeping
// these nodes allows determining the hottest edge from a call even if that
// callee is not available.
//
// TODO(prattmic): Consider merging this data structure with Graph. This is
// effectively a copy of Graph aggregated to line number and pointing to IR.
type IRGraph struct {
	// Nodes of the graph. Each node represents a function, keyed by linker
	// symbol name.
	IRNodes map[string]*IRNode
}

// IRNode represents a node (function) in the IRGraph.
type IRNode struct {
	// Pointer to the IR of the Function represented by this node.
	AST *ir.Func
	// Linker symbol name of the Function represented by this node.
	// Populated only if AST == nil.
	LinkerSymbolName string

	// Set of out-edges in the callgraph. The map uniquely identifies each
	// edge based on the callsite and callee, for fast lookup.
	OutEdges map[NamedCallEdge]*IREdge
}

// Name returns the symbol name of this function.
func (i *IRNode) Name() string {
	if i.AST != nil {
		return ir.LinkFuncName(i.AST)
	}
	return i.LinkerSymbolName
}

// IREdge represents a call edge in the IRGraph with source, destination,
// weight, callsite, and line number information.
type IREdge struct {
	// Source and destination of the edge in IRNode.
	Src, Dst       *IRNode
	Weight         int64
	CallSiteOffset int // Line offset from function start line.
}

// NamedCallEdge identifies a call edge by linker symbol names and call site
// offset.
type NamedCallEdge struct {
	CallerName     string
	CalleeName     string
	CallSiteOffset int // Line offset from function start line.
}

// NamedEdgeMap contains all unique call edges in the profile and their
// edge weight.
type NamedEdgeMap struct {
	Weight map[NamedCallEdge]int64

	// ByWeight lists all keys in Weight, sorted by edge weight.
	ByWeight []NamedCallEdge
}

// CallSiteInfo captures call-site information and its caller/callee.
type CallSiteInfo struct {
	LineOffset int // Line offset from function start line.
	Caller     *ir.Func
	Callee     *ir.Func
}

// Profile contains the processed PGO profile and weighted call graph used for
// PGO optimizations.
type Profile struct {
	// Aggregated edge weights across the profile. This helps us determine
	// the percentage threshold for hot/cold partitioning.
	TotalWeight int64

	// NamedEdgeMap contains all unique call edges in the profile and their
	// edge weight.
	NamedEdgeMap NamedEdgeMap

	// WeightedCG represents the IRGraph built from profile, which we will
	// update as part of inlining.
	WeightedCG *IRGraph
}

var wantHdr = "GO PREPROFILE V1\n"

var bbDebugPrint = false

func isPreProfileFile(r *bufio.Reader) (bool, error) {
	hdr, err := r.Peek(len(wantHdr))
	if err == io.EOF {
		// Empty file.
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("error reading profile header: %w", err)
	}

	return string(hdr) == wantHdr, nil
}

// New generates a profile-graph from the profile or pre-processed profile.
func New(profileFile string) (*Profile, error) {
	f, err := os.Open(profileFile)
	if err != nil {
		return nil, fmt.Errorf("error opening profile: %w", err)
	}
	defer f.Close()

	r := bufio.NewReader(f)

	isPreProf, err := isPreProfileFile(r)
	if err != nil {
		return nil, fmt.Errorf("error processing profile header: %w", err)
	}

	if isPreProf {
		profile, err := processPreprof(r)
		if err != nil {
			return nil, fmt.Errorf("error processing preprocessed PGO profile: %w", err)
		}
		return profile, nil
	}

	profile, err := processProto(r)
	if err != nil {
		return nil, fmt.Errorf("error processing pprof PGO profile: %w", err)
	}
	return profile, nil

}

// processProto generates a profile-graph from the profile.
func processProto(r io.Reader) (*Profile, error) {
	p, err := profile.Parse(r)
	if errors.Is(err, profile.ErrNoData) {
		// Treat a completely empty file the same as a profile with no
		// samples: nothing to do.
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("error parsing profile: %w", err)
	}

	if len(p.Sample) == 0 {
		// We accept empty profiles, but there is nothing to do.
		return nil, nil
	}

	valueIndex := -1
	for i, s := range p.SampleType {
		// Samples count is the raw data collected, and CPU nanoseconds is just
		// a scaled version of it, so either one we can find is fine.
		if (s.Type == "samples" && s.Unit == "count") ||
			(s.Type == "cpu" && s.Unit == "nanoseconds") {
			valueIndex = i
			break
		}
	}

	if valueIndex == -1 {
		return nil, fmt.Errorf(`profile does not contain a sample index with value/type "samples/count" or cpu/nanoseconds"`)
	}

	g := profile.NewGraph(p, &profile.Options{
		SampleValue: func(v []int64) int64 { return v[valueIndex] },
	})

	namedEdgeMap, totalWeight, err := createNamedEdgeMap(g)
	if err != nil {
		return nil, err
	}

	if totalWeight == 0 {
		return nil, nil // accept but ignore profile with no samples.
	}

	// Create package-level call graph with weights from profile and IR.
	wg := createIRGraph(namedEdgeMap)

	if base.Flag.BbPgoProfile {
		// If option is enabled - load basic block counters from the profile

		// Load counters from file
		loadCounters(p)

		// Propagate counters in AST
		ir.VisitFuncsBottomUp(typecheck.Target.Funcs, func(list []*ir.Func, recursive bool) {
			for _, f := range list {
				PropagateCounters(f)
			}
		})
	}

	return &Profile{
		TotalWeight:  totalWeight,
		NamedEdgeMap: namedEdgeMap,
		WeightedCG:   wg,
	}, nil
}

// shouldSetCounter returns true if this node type should have a counter
func shouldSetCounter(op ir.Op) bool {
	return op != ir.ONAME && op != ir.OLITERAL
}

// backPropNodeListCounterRec for all nodes in the list launch back propagation
// returns the maximum value of the counter and true if this block may return
func backPropNodeListCounterRec(nodes ir.Nodes, depth int, watched map[ir.Node]bool) (int64, bool) {
	if nodes == nil {
		return 0, false
	}

	var maxCount int64 // Maximal counter of the whole block
	var count int64    // Counter for the noreturn subset of the block
	var mR bool

	setCounter := func (nds ir.Nodes, s, e int, c int64) {
		for i := s; i <= e; i++ {
			n := nds[i]
			if shouldSetCounter(n.Op()) {
				if bbDebugPrint {
					println("back_prop (list): ", n.Op().String(), ":", n.Pos().Line(), " old: ", n.Counter(), " new: ", c)
				}
				n.SetCounter(c)
			}
		}
	}

	// Propagate counters and find maximum for this tree level
	// TODO we should take in account that a subtree in a list may contain
	//      return, and after it the counter may be lower
	rangeStart := 0
	for curNode, n := range nodes {
		c, mayReturn := backPropNodeCounterRec(n, depth, watched)
		if c > count {
			count = c
		}
		if c > maxCount {
			maxCount = c
		}

		if mayReturn {
			// If we could return from this sub-tree, we must set
			// the current counter for this range of nodes.
			setCounter(nodes, rangeStart, curNode, count)
			rangeStart = curNode + 1
			count = 0
			mR = true
		}
	}

	// Set counters to the rest of node list (or to the whole list, if
	// no possible returns were found
	setCounter(nodes, rangeStart, len(nodes) - 1, count)

	return maxCount, mR
}

// backPropNodeCounterRec implements the propagation of profile counters from
// bottom to top. The main goal of this step is to get the maximal counter
// value to each level of a tree and to make possible the top to down pass
// returns the counter of the node and true if sub-tree have a return statement
// NOTE keep it symmetrically to forwardPropNodeCounterRec
func backPropNodeCounterRec(n ir.Node, depth int, watched map[ir.Node]bool) (int64, bool) {
	if n == nil {
		return 0, false
	}
	if watched[n] {
		return n.Counter(), false
	}
	watched[n] = true

	max := func(x, y int64) int64 {
		if x > y {
			return x
		}
		return y
	}
	var mayReturn bool
	var count int64

	if n.Op() == ir.OIF {
		n := n.(*ir.IfStmt)
		count, mayReturn = backPropNodeCounterRec(n.Cond, depth+1, watched)
		bC, bR := backPropNodeListCounterRec(n.Body, depth+1, watched)
		eC, eR := backPropNodeListCounterRec(n.Else, depth+1, watched)

		sum := bC + eC
		count = max(count, sum)
		mayReturn = mayReturn || bR || eR
	} else if n.Op() == ir.OFOR {
		n := n.(*ir.ForStmt)
		count, mayReturn = backPropNodeListCounterRec(n.Body, depth+1, watched)
		cC, cR := backPropNodeCounterRec(n.Cond, depth+1, watched)
		pC, pR := backPropNodeCounterRec(n.Post, depth+1, watched)

		// The OFOR node itself represents the acyclic node without real representation in code.
		// Its counter should be the same as the acyclic nodes of the same level
		if count != 0 || cC != 0 || pC != 0 {
			count = 1
		} else {
			count = 0
		}
		mayReturn = mayReturn || cR || pR
	} else if shouldSetCounter(n.Op()) {
		v := reflect.ValueOf(n).Elem()
		t := reflect.TypeOf(n).Elem()
		nf := t.NumField()
		for i := 0; i < nf; i++ {
			var fC int64
			var mR bool
			tf := t.Field(i)
			vf := v.Field(i)

			if tf.PkgPath != "" {
				// skip unexported field - Interface will fail
				continue
			}
			switch tf.Type.Kind() {
			case reflect.Interface, reflect.Ptr, reflect.Slice:
				if vf.IsNil() {
					continue
				}
			}

			switch val := vf.Interface().(type) {
			case ir.Node:
				fC, mR = backPropNodeCounterRec(val, depth+1, watched)
			case ir.Nodes:
				fC, mR = backPropNodeListCounterRec(val, depth+1, watched)
			}

			count = max(count, fC)
			mayReturn = mayReturn || mR
		}
	}

	if n.Op() == ir.ORANGE && count > 0 {
		// Same logic as for OFOR
		count = 1
	} else if n.Op() == ir.ORETURN {
		mayReturn = true
	}

	count = max(count, n.Counter())
	if bbDebugPrint {
		println("back_prop: ", n.Op().String(), ":", n.Pos().Line(), " old: ", n.Counter(), " new: ", count)
	}
	n.SetCounter(count)

	return count, mayReturn
}

// forwardPropNodeListCounterRec for all nodes in the list launch forward propagation
func forwardPropNodeListCounterRec(nodes ir.Nodes, depth int, watched map[ir.Node]bool) {
	for _, n := range nodes {
		forwardPropNodeCounterRec(n, n.Counter(), depth, watched)
	}
}

// forwardPropNodeCounterRec implements the propagation of profile counters from
// top to bottom. The main goal of this step is to make counters of the tree
// consistent
// NOTE keep it symmetrically to backPropNodeCounterRec
func forwardPropNodeCounterRec(n ir.Node, c int64, depth int, watched map[ir.Node]bool) {
	if n == nil {
		return
	}
	if watched[n] {
		return
	}
	watched[n] = true

	max := func(x, y, z int64) int64 {
		res := x
		if y > res {
			res = y
		}
		if z > res {
			return z
		}
		return res
	}

	if shouldSetCounter(n.Op()) {
		if bbDebugPrint {
			println("forward_prop: ", n.Op().String(), ":", n.Pos().Line(), " old: ", n.Counter(), " new: ", c)
		}
		n.SetCounter(c)
	}

	if n.Op() == ir.OIF {
		n := n.(*ir.IfStmt)

		bodyCount := int64(0)
		bodyLen := len(n.Body)
		if bodyLen != 0 {
			// The first node has the maximal counter
			bodyCount = n.Body[0].Counter()
		}
		elseCount := int64(0)
		elseLen := len(n.Else)
		if elseLen != 0 {
			// The first node has the maximal counter
			elseCount = n.Else[0].Counter()
		}

		condCount := n.Cond.Counter()

		if bodyCount + elseCount > c {
			// This is case, when sum of branches counters is larger, than the
			// counter of if condition itself. This happens when one of branch
			// is executed longer, than the if condition. In this case we count
			// correct condition counter as the sum of its branches
			c = bodyCount + elseCount
			if condCount > c {
				// NOTE this is impossible after back propagation
				c = condCount
			}
		}

		if condCount < c {
			// The counter of the condition and the counter of IF node should be equal
			condCount = c
		}

		if elseLen == 0 {
			// If we have no else branch - we always go to the body
			bodyCount = c
		}
		// NOTE: we could correct both branches to make true the equation bodyCount + elseCount == ifCount
		//       but currently we do not need it.

		if bodyLen != 0 {
			n.Body[0].SetCounter(bodyCount)
			forwardPropNodeListCounterRec(n.Body, depth+1, watched)
		}

		if elseLen != 0 {
			n.Else[0].SetCounter(elseCount)
			forwardPropNodeListCounterRec(n.Else, depth+1, watched)
		}
		forwardPropNodeCounterRec(n.Cond, c, depth+1, watched)

/*		if n.Else == nil {
			// Without else we can not correct the body counter
			if bodyLen != 0 {
				if bbDebugPrint {
					println("forward_prop (body correct0): ", n.Op().String(), ":", n.Pos().Line(), " old: ", n.Counter(), " new: ", bodyCount)
				}
				n.Body[0].SetCounter(bodyCount)
				forwardPropNodeListCounterRec(n.Body, depth+1, watched)
			}
		} else if len(n.Else) != 0 {
			elseCount = n.Else[0].Counter() // All counters in list are equal
		}

		if bodyCount+elseCount < c && len(n.Else) != 0 {
			// Sum of two if branches is lesser than if counter
			// We should correct the counters
			if bodyCount+elseCount != 0 {
				// At least one branch edge has non-zero profile
				ratio := float64(c) / float64(bodyCount+elseCount)
				bodyCount = int64(float64(bodyCount) * ratio)
				elseCount = c - bodyCount
			} else {
				// If both branches are zero, we assume that the probability is 0.5
				if n.Else != nil {
					bodyCount = int64(float64(c) * 0.5)
				} else {
					bodyCount = c
				}
				elseCount = c - bodyCount
			}

			if len(n.Body) > 0 {
				if bbDebugPrint {
					println("forward_prop (body correct2): ", n.Op().String(), ":", n.Pos().Line(), " old: ", n.Counter(), " new: ", bodyCount)
				}
				n.Body[0].SetCounter(bodyCount)
			}
			if len(n.Else) > 0 {
				if bbDebugPrint {
					println("forward_prop (else correct): ", n.Op().String(), ":", n.Pos().Line(), " old: ", n.Counter(), " new: ", elseCount)
				}
				n.Else[0].SetCounter(elseCount)
			}
			forwardPropNodeListCounterRec(n.Body, depth+1, watched)
			forwardPropNodeListCounterRec(n.Else, depth+1, watched)
		}
		forwardPropNodeCounterRec(n.Cond, bodyCount+elseCount, depth+1, watched)*/
	} else if n.Op() == ir.OFOR {
		n := n.(*ir.ForStmt)
		var bC, cC, pC int64
		if n.Body != nil {
			bC = c
			if len(n.Body) != 0 {
				bC = n.Body[0].Counter()
			}
		}
		if n.Cond != nil {
			cC = n.Cond.Counter()
		}
		if n.Post != nil {
			pC = n.Post.Counter()
		}

		c = max(bC, cC, pC)
		if n.Body != nil {
			forwardPropNodeListCounterRec(n.Body, depth+1, watched)
		}
		if n.Cond != nil {
			forwardPropNodeCounterRec(n.Cond, c, depth+1, watched)
		}
		if n.Post != nil {
			forwardPropNodeCounterRec(n.Post, c, depth+1, watched)
		}
	} else if shouldSetCounter(n.Op()) {
		v := reflect.ValueOf(n).Elem()
		t := reflect.TypeOf(n).Elem()
		nf := t.NumField()
		for i := 0; i < nf; i++ {
			vf := v.Field(i)
			tf := t.Field(i)

			if tf.PkgPath != "" {
				// skip unexported field - Interface will fail
				continue
			}
			switch tf.Type.Kind() {
			case reflect.Interface, reflect.Ptr, reflect.Slice:
				if vf.IsNil() {
					continue
				}
			}

			switch val := vf.Interface().(type) {
			case ir.Node:
				forwardPropNodeCounterRec(val, c, depth+1, watched)
			case ir.Nodes:
				forwardPropNodeListCounterRec(val, depth+1, watched)
			}
		}
	}

	return
}

// PropagateCounters this function starts back and forward counter propagation
func PropagateCounters(f *ir.Func) {
	debugFuncName, isDebug := os.LookupEnv("GOSSAFUNC")
	if isDebug && strings.Contains(ir.LinkFuncName(f), debugFuncName) {
		bbDebugPrint = true
		println("Start bbpgo debug for: ", ir.LinkFuncName(f))
	}

	watched := map[ir.Node]bool{}
	backPropNodeListCounterRec(f.Body, 0, watched)
	watched = map[ir.Node]bool{}
	forwardPropNodeListCounterRec(f.Body, 0, watched)

	bbDebugPrint = false
}

// loadCounters loads counters to the nodes of AST from profile
func loadCounters(p *profile.Profile) {
	// Build a table functionName <-> ir.Func to get quick search
	// between profile.Function and ir.Func
	type FuncSamples struct {
		Func *ir.Func
		// This is the map line <-> Sample for quick search
		Sample map[int64][]*profile.Sample
	}
	funcTable := make(map[string]*FuncSamples)
	ir.VisitFuncsBottomUp(typecheck.Target.Funcs, func(list []*ir.Func, recursive bool) {
		for _, f := range list {
			fs := &FuncSamples{
				Func:   f,
				Sample: make(map[int64][]*profile.Sample),
			}
			name := ir.LinkFuncName(f)
			funcTable[name] = fs
		}
	})

	// Watch all samples and add the sample to the function
	// table lineNum <-> sample
	for _, s := range p.Sample {
		lastLocIdx := len(s.Location)
		if lastLocIdx == 0 {
			continue
		}
		loc := s.Location[0]
		// One sample may relate to few lines in the code (for example,
		// if the instruction lies on the other function and the
		// function was inlined). So we add the sample to all the
		// entries
		for _, l := range loc.Line {
			fs, ok := funcTable[l.Function.SystemName]
			if !ok {
				// This function is not seen inside this package
				continue
			}

			fs.Sample[l.Line] = append(fs.Sample[l.Line], s)
		}
	}

	// Visit all the AST functions and for every node set the counter
	for _, fs := range funcTable {
		ir.VisitList(fs.Func.Body, func(n ir.Node) {
			sample, ok := fs.Sample[int64(n.Pos().Line())]
			if !ok {
				return
			}
			if !shouldSetCounter(n.Op()) {
				return
			}
			n.SetCounter(sample[0].Value[0])
		})
	}
}

// processPreprof generates a profile-graph from the pre-procesed profile.
func processPreprof(r io.Reader) (*Profile, error) {
	namedEdgeMap, totalWeight, err := createNamedEdgeMapFromPreprocess(r)
	if err != nil {
		return nil, err
	}

	if totalWeight == 0 {
		return nil, nil // accept but ignore profile with no samples.
	}

	// Create package-level call graph with weights from profile and IR.
	wg := createIRGraph(namedEdgeMap)

	return &Profile{
		TotalWeight:  totalWeight,
		NamedEdgeMap: namedEdgeMap,
		WeightedCG:   wg,
	}, nil
}

func postProcessNamedEdgeMap(weight map[NamedCallEdge]int64, weightVal int64) (edgeMap NamedEdgeMap, totalWeight int64, err error) {
	if weightVal == 0 {
		return NamedEdgeMap{}, 0, nil // accept but ignore profile with no samples.
	}
	byWeight := make([]NamedCallEdge, 0, len(weight))
	for namedEdge := range weight {
		byWeight = append(byWeight, namedEdge)
	}
	sort.Slice(byWeight, func(i, j int) bool {
		ei, ej := byWeight[i], byWeight[j]
		if wi, wj := weight[ei], weight[ej]; wi != wj {
			return wi > wj // want larger weight first
		}
		// same weight, order by name/line number
		if ei.CallerName != ej.CallerName {
			return ei.CallerName < ej.CallerName
		}
		if ei.CalleeName != ej.CalleeName {
			return ei.CalleeName < ej.CalleeName
		}
		return ei.CallSiteOffset < ej.CallSiteOffset
	})

	edgeMap = NamedEdgeMap{
		Weight:   weight,
		ByWeight: byWeight,
	}

	totalWeight = weightVal

	return edgeMap, totalWeight, nil
}

// restore NodeMap information from a preprocessed profile.
// The reader can refer to the format of preprocessed profile in cmd/preprofile/main.go.
func createNamedEdgeMapFromPreprocess(r io.Reader) (edgeMap NamedEdgeMap, totalWeight int64, err error) {
	fileScanner := bufio.NewScanner(r)
	fileScanner.Split(bufio.ScanLines)
	weight := make(map[NamedCallEdge]int64)

	if !fileScanner.Scan() {
		if err := fileScanner.Err(); err != nil {
			return NamedEdgeMap{}, 0, fmt.Errorf("error reading preprocessed profile: %w", err)
		}
		return NamedEdgeMap{}, 0, fmt.Errorf("preprocessed profile missing header")
	}
	if gotHdr := fileScanner.Text() + "\n"; gotHdr != wantHdr {
		return NamedEdgeMap{}, 0, fmt.Errorf("preprocessed profile malformed header; got %q want %q", gotHdr, wantHdr)
	}

	for fileScanner.Scan() {
		readStr := fileScanner.Text()

		callerName := readStr

		if !fileScanner.Scan() {
			if err := fileScanner.Err(); err != nil {
				return NamedEdgeMap{}, 0, fmt.Errorf("error reading preprocessed profile: %w", err)
			}
			return NamedEdgeMap{}, 0, fmt.Errorf("preprocessed profile entry missing callee")
		}
		calleeName := fileScanner.Text()

		if !fileScanner.Scan() {
			if err := fileScanner.Err(); err != nil {
				return NamedEdgeMap{}, 0, fmt.Errorf("error reading preprocessed profile: %w", err)
			}
			return NamedEdgeMap{}, 0, fmt.Errorf("preprocessed profile entry missing weight")
		}
		readStr = fileScanner.Text()

		split := strings.Split(readStr, " ")

		if len(split) != 2 {
			return NamedEdgeMap{}, 0, fmt.Errorf("preprocessed profile entry got %v want 2 fields", split)
		}

		co, _ := strconv.Atoi(split[0])

		namedEdge := NamedCallEdge{
			CallerName:     callerName,
			CalleeName:     calleeName,
			CallSiteOffset: co,
		}

		EWeight, _ := strconv.ParseInt(split[1], 10, 64)

		weight[namedEdge] += EWeight
		totalWeight += EWeight
	}

	return postProcessNamedEdgeMap(weight, totalWeight)

}

// createNamedEdgeMap builds a map of callsite-callee edge weights from the
// profile-graph.
//
// Caller should ignore the profile if totalWeight == 0.
func createNamedEdgeMap(g *profile.Graph) (edgeMap NamedEdgeMap, totalWeight int64, err error) {
	seenStartLine := false

	// Process graph and build various node and edge maps which will
	// be consumed by AST walk.
	weight := make(map[NamedCallEdge]int64)
	for _, n := range g.Nodes {
		seenStartLine = seenStartLine || n.Info.StartLine != 0

		canonicalName := n.Info.Name
		// Create the key to the nodeMapKey.
		namedEdge := NamedCallEdge{
			CallerName:     canonicalName,
			CallSiteOffset: n.Info.Lineno - n.Info.StartLine,
		}

		for _, e := range n.Out {
			totalWeight += e.WeightValue()
			namedEdge.CalleeName = e.Dest.Info.Name
			// Create new entry or increment existing entry.
			weight[namedEdge] += e.WeightValue()
		}
	}

	if !seenStartLine {
		// TODO(prattmic): If Function.start_line is missing we could
		// fall back to using absolute line numbers, which is better
		// than nothing.
		return NamedEdgeMap{}, 0, fmt.Errorf("profile missing Function.start_line data (Go version of profiled application too old? Go 1.20+ automatically adds this to profiles)")
	}
	return postProcessNamedEdgeMap(weight, totalWeight)
}

// initializeIRGraph builds the IRGraph by visiting all the ir.Func in decl list
// of a package.
func createIRGraph(namedEdgeMap NamedEdgeMap) *IRGraph {
	g := &IRGraph{
		IRNodes: make(map[string]*IRNode),
	}

	// Bottomup walk over the function to create IRGraph.
	ir.VisitFuncsBottomUp(typecheck.Target.Funcs, func(list []*ir.Func, recursive bool) {
		for _, fn := range list {
			visitIR(fn, namedEdgeMap, g)
		}
	})

	// Add additional edges for indirect calls. This must be done second so
	// that IRNodes is fully populated (see the dummy node TODO in
	// addIndirectEdges).
	//
	// TODO(prattmic): visitIR above populates the graph via direct calls
	// discovered via the IR. addIndirectEdges populates the graph via
	// calls discovered via the profile. This combination of opposite
	// approaches is a bit awkward, particularly because direct calls are
	// discoverable via the profile as well. Unify these into a single
	// approach.
	addIndirectEdges(g, namedEdgeMap)

	return g
}

// visitIR traverses the body of each ir.Func adds edges to g from ir.Func to
// any called function in the body.
func visitIR(fn *ir.Func, namedEdgeMap NamedEdgeMap, g *IRGraph) {
	name := ir.LinkFuncName(fn)
	node, ok := g.IRNodes[name]
	if !ok {
		node = &IRNode{
			AST: fn,
		}
		g.IRNodes[name] = node
	}

	// Recursively walk over the body of the function to create IRGraph edges.
	createIRGraphEdge(fn, node, name, namedEdgeMap, g)
}

// createIRGraphEdge traverses the nodes in the body of ir.Func and adds edges
// between the callernode which points to the ir.Func and the nodes in the
// body.
func createIRGraphEdge(fn *ir.Func, callernode *IRNode, name string, namedEdgeMap NamedEdgeMap, g *IRGraph) {
	ir.VisitList(fn.Body, func(n ir.Node) {
		switch n.Op() {
		case ir.OCALLFUNC:
			call := n.(*ir.CallExpr)
			// Find the callee function from the call site and add the edge.
			callee := DirectCallee(call.Fun)
			if callee != nil {
				addIREdge(callernode, name, n, callee, namedEdgeMap, g)
			}
		case ir.OCALLMETH:
			call := n.(*ir.CallExpr)
			// Find the callee method from the call site and add the edge.
			callee := ir.MethodExprName(call.Fun).Func
			addIREdge(callernode, name, n, callee, namedEdgeMap, g)
		}
	})
}

// NodeLineOffset returns the line offset of n in fn.
func NodeLineOffset(n ir.Node, fn *ir.Func) int {
	// See "A note on line numbers" at the top of the file.
	line := int(base.Ctxt.InnermostPos(n.Pos()).RelLine())
	startLine := int(base.Ctxt.InnermostPos(fn.Pos()).RelLine())
	return line - startLine
}

// addIREdge adds an edge between caller and new node that points to `callee`
// based on the profile-graph and NodeMap.
func addIREdge(callerNode *IRNode, callerName string, call ir.Node, callee *ir.Func, namedEdgeMap NamedEdgeMap, g *IRGraph) {
	calleeName := ir.LinkFuncName(callee)
	calleeNode, ok := g.IRNodes[calleeName]
	if !ok {
		calleeNode = &IRNode{
			AST: callee,
		}
		g.IRNodes[calleeName] = calleeNode
	}

	namedEdge := NamedCallEdge{
		CallerName:     callerName,
		CalleeName:     calleeName,
		CallSiteOffset: NodeLineOffset(call, callerNode.AST),
	}

	// Add edge in the IRGraph from caller to callee.
	edge := &IREdge{
		Src:            callerNode,
		Dst:            calleeNode,
		Weight:         namedEdgeMap.Weight[namedEdge],
		CallSiteOffset: namedEdge.CallSiteOffset,
	}

	if callerNode.OutEdges == nil {
		callerNode.OutEdges = make(map[NamedCallEdge]*IREdge)
	}
	callerNode.OutEdges[namedEdge] = edge
}

// LookupFunc looks up a function or method in export data. It is expected to
// be overridden by package noder, to break a dependency cycle.
var LookupFunc = func(fullName string) (*ir.Func, error) {
	base.Fatalf("pgo.LookupMethodFunc not overridden")
	panic("unreachable")
}

// addIndirectEdges adds indirect call edges found in the profile to the graph,
// to be used for devirtualization.
//
// N.B. despite the name, addIndirectEdges will add any edges discovered via
// the profile. We don't know for sure that they are indirect, but assume they
// are since direct calls would already be added. (e.g., direct calls that have
// been deleted from source since the profile was taken would be added here).
//
// TODO(prattmic): Devirtualization runs before inlining, so we can't devirtualize
// calls inside inlined call bodies. If we did add that, we'd need edges from
// inlined bodies as well.
func addIndirectEdges(g *IRGraph, namedEdgeMap NamedEdgeMap) {
	// g.IRNodes is populated with the set of functions in the local
	// package build by VisitIR. We want to filter for local functions
	// below, but we also add unknown callees to IRNodes as we go. So make
	// an initial copy of IRNodes to recall just the local functions.
	localNodes := make(map[string]*IRNode, len(g.IRNodes))
	for k, v := range g.IRNodes {
		localNodes[k] = v
	}

	// N.B. We must consider edges in a stable order because export data
	// lookup order (LookupMethodFunc, below) can impact the export data of
	// this package, which must be stable across different invocations for
	// reproducibility.
	//
	// The weight ordering of ByWeight is irrelevant, it just happens to be
	// an ordered list of edges that is already available.
	for _, key := range namedEdgeMap.ByWeight {
		weight := namedEdgeMap.Weight[key]
		// All callers in the local package build were added to IRNodes
		// in VisitIR. If a caller isn't in the local package build we
		// can skip adding edges, since we won't be devirtualizing in
		// them anyway. This keeps the graph smaller.
		callerNode, ok := localNodes[key.CallerName]
		if !ok {
			continue
		}

		// Already handled this edge?
		if _, ok := callerNode.OutEdges[key]; ok {
			continue
		}

		calleeNode, ok := g.IRNodes[key.CalleeName]
		if !ok {
			// IR is missing for this callee. VisitIR populates
			// IRNodes with all functions discovered via local
			// package function declarations and calls. This
			// function may still be available from export data of
			// a transitive dependency.
			//
			// TODO(prattmic): Parameterized types/functions are
			// not supported.
			//
			// TODO(prattmic): This eager lookup during graph load
			// is simple, but wasteful. We are likely to load many
			// functions that we never need. We could delay load
			// until we actually need the method in
			// devirtualization. Instantiation of generic functions
			// will likely need to be done at the devirtualization
			// site, if at all.
			fn, err := LookupFunc(key.CalleeName)
			if err == nil {
				if base.Debug.PGODebug >= 3 {
					fmt.Printf("addIndirectEdges: %s found in export data\n", key.CalleeName)
				}
				calleeNode = &IRNode{AST: fn}

				// N.B. we could call createIRGraphEdge to add
				// direct calls in this newly-imported
				// function's body to the graph. Similarly, we
				// could add to this function's queue to add
				// indirect calls. However, those would be
				// useless given the visit order of inlining,
				// and the ordering of PGO devirtualization and
				// inlining. This function can only be used as
				// an inlined body. We will never do PGO
				// devirtualization inside an inlined call. Nor
				// will we perform inlining inside an inlined
				// call.
			} else {
				// Still not found. Most likely this is because
				// the callee isn't in the transitive deps of
				// this package.
				//
				// Record this call anyway. If this is the hottest,
				// then we want to skip devirtualization rather than
				// devirtualizing to the second most common callee.
				if base.Debug.PGODebug >= 3 {
					fmt.Printf("addIndirectEdges: %s not found in export data: %v\n", key.CalleeName, err)
				}
				calleeNode = &IRNode{LinkerSymbolName: key.CalleeName}
			}

			// Add dummy node back to IRNodes. We don't need this
			// directly, but PrintWeightedCallGraphDOT uses these
			// to print nodes.
			g.IRNodes[key.CalleeName] = calleeNode
		}
		edge := &IREdge{
			Src:            callerNode,
			Dst:            calleeNode,
			Weight:         weight,
			CallSiteOffset: key.CallSiteOffset,
		}

		if callerNode.OutEdges == nil {
			callerNode.OutEdges = make(map[NamedCallEdge]*IREdge)
		}
		callerNode.OutEdges[key] = edge
	}
}

// WeightInPercentage converts profile weights to a percentage.
func WeightInPercentage(value int64, total int64) float64 {
	return (float64(value) / float64(total)) * 100
}

// PrintWeightedCallGraphDOT prints IRGraph in DOT format.
func (p *Profile) PrintWeightedCallGraphDOT(edgeThreshold float64) {
	fmt.Printf("\ndigraph G {\n")
	fmt.Printf("forcelabels=true;\n")

	// List of functions in this package.
	funcs := make(map[string]struct{})
	ir.VisitFuncsBottomUp(typecheck.Target.Funcs, func(list []*ir.Func, recursive bool) {
		for _, f := range list {
			name := ir.LinkFuncName(f)
			funcs[name] = struct{}{}
		}
	})

	// Determine nodes of DOT.
	//
	// Note that ir.Func may be nil for functions not visible from this
	// package.
	nodes := make(map[string]*ir.Func)
	for name := range funcs {
		if n, ok := p.WeightedCG.IRNodes[name]; ok {
			for _, e := range n.OutEdges {
				if _, ok := nodes[e.Src.Name()]; !ok {
					nodes[e.Src.Name()] = e.Src.AST
				}
				if _, ok := nodes[e.Dst.Name()]; !ok {
					nodes[e.Dst.Name()] = e.Dst.AST
				}
			}
			if _, ok := nodes[n.Name()]; !ok {
				nodes[n.Name()] = n.AST
			}
		}
	}

	// Print nodes.
	for name, ast := range nodes {
		if _, ok := p.WeightedCG.IRNodes[name]; ok {
			style := "solid"
			if ast == nil {
				style = "dashed"
			}

			if ast != nil && ast.Inl != nil {
				fmt.Printf("\"%v\" [color=black, style=%s, label=\"%v,inl_cost=%d\"];\n", name, style, name, ast.Inl.Cost)
			} else {
				fmt.Printf("\"%v\" [color=black, style=%s, label=\"%v\"];\n", name, style, name)
			}
		}
	}
	// Print edges.
	ir.VisitFuncsBottomUp(typecheck.Target.Funcs, func(list []*ir.Func, recursive bool) {
		for _, f := range list {
			name := ir.LinkFuncName(f)
			if n, ok := p.WeightedCG.IRNodes[name]; ok {
				for _, e := range n.OutEdges {
					style := "solid"
					if e.Dst.AST == nil {
						style = "dashed"
					}
					color := "black"
					edgepercent := WeightInPercentage(e.Weight, p.TotalWeight)
					if edgepercent > edgeThreshold {
						color = "red"
					}

					fmt.Printf("edge [color=%s, style=%s];\n", color, style)
					fmt.Printf("\"%v\" -> \"%v\" [label=\"%.2f\"];\n", n.Name(), e.Dst.Name(), edgepercent)
				}
			}
		}
	})
	fmt.Printf("}\n")
}

// DirectCallee takes a function-typed expression and returns the underlying
// function that it refers to if statically known. Otherwise, it returns nil.
//
// Equivalent to inline.inlCallee without calling CanInline on closures.
func DirectCallee(fn ir.Node) *ir.Func {
	fn = ir.StaticValue(fn)
	switch fn.Op() {
	case ir.OMETHEXPR:
		fn := fn.(*ir.SelectorExpr)
		n := ir.MethodExprName(fn)
		// Check that receiver type matches fn.X.
		// TODO(mdempsky): Handle implicit dereference
		// of pointer receiver argument?
		if n == nil || !types.Identical(n.Type().Recv().Type, fn.X.Type()) {
			return nil
		}
		return n.Func
	case ir.ONAME:
		fn := fn.(*ir.Name)
		if fn.Class == ir.PFUNC {
			return fn.Func
		}
	case ir.OCLOSURE:
		fn := fn.(*ir.ClosureExpr)
		c := fn.Func
		return c
	}
	return nil
}
