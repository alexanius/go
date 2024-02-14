// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pgo

import (
	"cmd/compile/internal/ir"
	"cmd/compile/internal/typecheck"
	"internal/profile"
	"os"
	"reflect"
	"strings"
)

var bbDebugPrint = false

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

// PropagateCounters this function starts back and forward counter propagation
func PropagateCounters(f *ir.Func) {
	debugFuncName, isDebug := os.LookupEnv("GOSSAFUNC")
	if isDebug && strings.Contains(ir.LinkFuncName(f), debugFuncName) {
		println("Start bbpgo debug for: ", ir.LinkFuncName(f))
	}

	watched := map[ir.Node]bool{}
	backPropNodeListCounterRec(f.Body, 0, watched)
	watched = map[ir.Node]bool{}
	forwardPropNodeListCounterRec(f.Body, 0, watched)

	bbDebugPrint = false
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

	setCounter := func(nds ir.Nodes, s, e int, c int64) {
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
	setCounter(nodes, rangeStart, len(nodes)-1, count)

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
