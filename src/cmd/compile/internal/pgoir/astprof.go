// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pgoir

import (
	"cmd/compile/internal/base"
	"cmd/compile/internal/ir"
//	"cmd/internal/src"
	"cmd/compile/internal/typecheck"
	"internal/profile"
	"os"
	"reflect"
	"strconv"
	"strings"
)

var bbDebugPrint = false

type Counter = int64

func printOp(n ir.Node) string {
//	return n.Op().String() + ":" + strconv.Itoa(int(n.Pos().Line()))
	return n.Op().String() + ":" + strconv.Itoa(int(base.Ctxt.InnermostPos(n.Pos()).Line()))
}

type FuncSamples struct {
	Func *ir.Func
	// This is the map line <-> Sample for quick search
	Sample map[int64][]*profile.Sample
}

type FuncSampleTable map[string]*FuncSamples

// LoadCounters loads counters to the nodes of AST from profile
func LoadCounters(p *profile.Profile, pf *ir.Func) *FuncSampleTable {
	// Build a table functionName <-> ir.Func to get quick search
	// between profile.Function and ir.Func
	funcTable := make(FuncSampleTable)
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
		for _, loc := range s.Location {
			for _, l := range loc.Line {
				fs, ok := funcTable[l.Function.SystemName]
				if !ok {
					// This function is not seen inside this package
					continue
				}
				fs.Sample[l.Line] = append(fs.Sample[l.Line], s)
			}
		}
	}

	// Assign counters to the nodes and propagate it
	SetCounters(&funcTable, pf, nil)

	return &funcTable
}

// SetCounters sets the counters loaded from the pprof file to the function
// If pf is nil, than to all the functions from the funcTable will be loaded counters
// If pf and inlName are not nil, than the counters will be set only into the pf function,
// but the counters will be loaded from the function inlName. This mode is needed to set
// counters to the inlined part of function
func SetCounters(funcTable *FuncSampleTable, pf *ir.Func, /*inlName *string*/callee *ir.Func) {
	// Visit all the AST functions and for every node set the counter
	debugFuncName, isDebug := os.LookupEnv("GOSSAFUNC")

	setCounters := func(fs *FuncSamples, callerFn *ir.Func, calleeFn *ir.Func) {
		if isDebug && strings.Contains(ir.LinkFuncName(callerFn), debugFuncName) {
			println("(SetCounters) Start bbpgo debug for: ", ir.LinkFuncName(/*fs.Func*/callerFn))
			bbDebugPrint = true
		}

		ir.VisitList(callerFn.Body, func(n ir.Node) {
//			sample, ok := fs.Sample[int64(n.Pos().Line())]
			sample, ok := fs.Sample[int64(base.Ctxt.InnermostPos(n.Pos()).Line())]

if bbDebugPrint {
println("back_prop init(try): ", printOp(n), base.Ctxt.InnermostPos(n.Pos()).Line(), n.Pos().Line(), ok)
}
			if !ok {
				return
			}
			if !shouldSetCounter(n.Op()) {
				return
			}
			// We should use cumulative counter, as flat may be zero
			callerFn.SetCounter2(n, sample[0].Value[1])

			if bbDebugPrint {
				println("back_prop init: ", printOp(n), " new: ", sample[0].Value[1])
			}
		})

		bbDebugPrint = false
	}

	if pf != nil {
		// Counters for only one function should be set
		calleeName := ir.LinkFuncName(callee)
		fs := (*funcTable)[calleeName]
		if fs != nil {
			if isDebug && strings.Contains(ir.LinkFuncName(/*fs.Func*/pf), debugFuncName) {
				println("Start bbpgo setting counters to particular function: ",
				     ir.LinkFuncName(pf),
				     "with corrections for inlined function: ",
				     calleeName)
				bbDebugPrint = true
			}
			setCounters(fs, pf, callee)
			PropagateCounters(/*fs.Func*/pf)

			bbDebugPrint = false
		} else {
		}
	} else {
		// Set counters to all the functions
		for _, fs := range *funcTable {
			if isDebug && strings.Contains(ir.LinkFuncName(fs.Func), debugFuncName) {
				println("Start bbpgo setting counters to function: ", ir.LinkFuncName(fs.Func))
				bbDebugPrint = true
			}

			setCounters(fs, fs.Func, fs.Func)
			PropagateCounters(fs.Func)

			bbDebugPrint = false
		}
	}
}

// PropagateCounters this function starts back and forward counter propagation
func PropagateCounters(f *ir.Func) {
	debugFuncName, isDebug := os.LookupEnv("GOSSAFUNC")
	if isDebug && strings.Contains(ir.LinkFuncName(f), debugFuncName) {
		println("(PropagateCounters) Start bbpgo debug for: ", ir.LinkFuncName(f))
		bbDebugPrint = true
	}

	watched := map[ir.Node]bool{}
	backPropNodeListCounterRec(f, f.Body, 0, watched)
	watched = map[ir.Node]bool{}
	forwardPropNodeListCounterRec(f, f.Body, 0, watched)

	bbDebugPrint = false
}

// shouldSetCounter returns true if this node type should have a counter
func shouldSetCounter(op ir.Op) bool {
	return op != ir.ONAME && op != ir.OLITERAL
}

// backPropNodeListCounterRec for all nodes in the list launch back propagation
// returns the maximum value of the counter and true if this block may return
func backPropNodeListCounterRec(f *ir.Func, nodes ir.Nodes, depth int, watched map[ir.Node]bool) (int64, bool) {
	if nodes == nil {
		return 0, false
	}

	var maxCount int64 // Maximal counter of the whole block
	var count int64    // Counter for the noreturn subset of the block
	var mR bool

	setCounter := func(nds ir.Nodes, s, e int, c int64) {
		for i := s; i <= e; i++ {
			n := nds[i]
			if shouldSetCounter(n.Op()) {
				if bbDebugPrint {
					println("back_prop (list): ", printOp(n), " old: ", f.GetCounter2(n), " new: ", c)
				}
				f.SetCounter2(n, c)
			}
		}
	}

	// Propagate counters and find maximum for this tree level
	// TODO we should take in account that a subtree in a list may contain
	//      return, and after it the counter may be lower
	rangeStart := 0
	for curNode, n := range nodes {
		c, mayReturn := backPropNodeCounterRec(f, n, depth, watched)
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
	setCounter(nodes, rangeStart, len(nodes)-1, count)

	return maxCount, mR
}

// backPropNodeCounterRec implements the propagation of profile counters from
// bottom to top. The main goal of this step is to get the maximal counter
// value to each level of a tree and to make possible the top to down pass
// returns the counter of the node and true if sub-tree have a return statement
// NOTE keep it symmetrically to forwardPropNodeCounterRec
func backPropNodeCounterRec(f *ir.Func, n ir.Node, depth int, watched map[ir.Node]bool) (int64, bool) {
	if n == nil {
		return 0, false
	}
	if watched[n] {
		return f.GetCounter2(n), false
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

	if n.Op() == ir.OINLMARK {
		n := n.(*ir.InlineMarkStmt)
		inlFunc := base.Ctxt.InlTree.InlinedFunction(n.Index)
	} else if n.Op() == ir.OIF {
		n := n.(*ir.IfStmt)
		count, mayReturn = backPropNodeCounterRec(f, n.Cond, depth+1, watched)
		bC, bR := backPropNodeListCounterRec(f, n.Body, depth+1, watched)
		eC, eR := backPropNodeListCounterRec(f, n.Else, depth+1, watched)

		sum := bC + eC
		count = max(count, sum)
		mayReturn = mayReturn || bR || eR
	} else if n.Op() == ir.OFOR {
		n := n.(*ir.ForStmt)
		count, mayReturn = backPropNodeListCounterRec(f, n.Body, depth+1, watched)
		cC, cR := backPropNodeCounterRec(f, n.Cond, depth+1, watched)
		pC, pR := backPropNodeCounterRec(f, n.Post, depth+1, watched)

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
				fC, mR = backPropNodeCounterRec(f, val, depth+1, watched)
			case ir.Nodes:
				fC, mR = backPropNodeListCounterRec(f, val, depth+1, watched)
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

	count = max(count, f.GetCounter2(n))
	if bbDebugPrint {
		println("back_prop: ", printOp(n), " old: ", f.GetCounter2(n), " new: ", count)
	}
	if shouldSetCounter(n.Op()) {
		f.SetCounter2(n, count)
	}

	return count, mayReturn
}

// forwardPropNodeListCounterRec for all nodes in the list launch forward propagation
func forwardPropNodeListCounterRec(f *ir.Func, nodes ir.Nodes, depth int, watched map[ir.Node]bool) {
	for _, n := range nodes {
		forwardPropNodeCounterRec(f, n, f.GetCounter2(n), depth, watched)
	}
}

// forwardPropNodeCounterRec implements the propagation of profile counters from
// top to bottom. The main goal of this step is to make counters of the tree
// consistent
// NOTE keep it symmetrically to backPropNodeCounterRec
func forwardPropNodeCounterRec(f *ir.Func, n ir.Node, c int64, depth int, watched map[ir.Node]bool) {
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
			println("forward_prop: ", printOp(n), " old: ", f.GetCounter2(n), " new: ", c)
		}
		f.SetCounter2(n, c)
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

		if bodyCount+elseCount > c {
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

		// NOTE: we could correct both branches to make true the equation bodyCount + elseCount == ifCount
		//       but currently we do not need it.

		if bodyLen != 0 {
			if shouldSetCounter(n.Body[0].Op()) {
				f.SetCounter2(n.Body[0], bodyCount)
			}
			forwardPropNodeListCounterRec(f, n.Body, depth+1, watched)
		}

		if elseLen != 0 {
			if shouldSetCounter(n.Else[0].Op()) {
				f.SetCounter2(n.Else[0], elseCount)
			}
			forwardPropNodeListCounterRec(f, n.Else, depth+1, watched)
		}
		forwardPropNodeCounterRec(f, n.Cond, c, depth+1, watched)
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
			forwardPropNodeListCounterRec(f, n.Body, depth+1, watched)
		}
		if n.Cond != nil {
			forwardPropNodeCounterRec(f, n.Cond, c, depth+1, watched)
		}
		if n.Post != nil {
			forwardPropNodeCounterRec(f, n.Post, c, depth+1, watched)
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
				forwardPropNodeCounterRec(f, val, c, depth+1, watched)
			case ir.Nodes:
				forwardPropNodeListCounterRec(f, val, depth+1, watched)
			}
		}
	}

	return
}
