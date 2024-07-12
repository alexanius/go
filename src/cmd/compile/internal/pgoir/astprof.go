// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
package pgoir

import (
	"cmd/compile/internal/base"
	"cmd/compile/internal/ir"
	"cmd/compile/internal/ssa"
	"cmd/compile/internal/typecheck"
	"cmd/internal/pgo"

	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
)

var enableDebug = false  // Set true for debug prints on GODDASFUNC
var bbDebugPrint = false // Do not change it manually

// Debug print of an operation
func printOp(n ir.Node) string {
	return n.Op().String() + ":" + strconv.Itoa(int(n.Pos().Line()))
}

// LoadCounters loads counters to the nodes of AST from profile
func LoadCounters(fc *pgo.FunctionsCounters) {

	// Visit all the AST functions and for every node set the counter
	debugFuncName, isDebug := os.LookupEnv("GOSSAFUNC")
	pass := "load_counters"

	ir.VisitFuncsBottomUp(typecheck.Target.Funcs, func(list []*ir.Func, recursive bool) {
		for _, f := range list {
			if f.ProfTable == nil {
				f.ProfTable = make(ir.NodeProfTable)
			}
			name := ir.LinkFuncName(f)

			if isDebug && strings.Contains(name, debugFuncName) && enableDebug {
				fmt.Printf("Start bbpgo setting counters on pass %s to function: %s\n",
					pass,
					ir.LinkFuncName(f))
				bbDebugPrint = true
			}

			lc, isOk := (*fc)[name]

			if !isOk {
				// No samples for given function
				continue
			}

			ir.VisitList(f.Body, func(n ir.Node) {
				if bbDebugPrint {
					fmt.Println("try back_prop init: ", printOp(n))
				}
				counter, ok := lc[int64(n.Pos().Line())]
				if !ok {
					return
				}

				// We should use cumulative counter, as flat may be zero
				ir.SetCounter(f, n, counter)

				if bbDebugPrint {
					fmt.Println("back_prop init: ", printOp(n), " new: ", counter)
				}
			})

			propagateCounters(f, pass)

			if isDebug && strings.Contains(name, debugFuncName) {
				fmt.Printf("Finish bbpgo setting counters on pass %s to function: %s\n",
				pass,
				name)
			}
			bbDebugPrint = false
		}
	})
}

// propagateCounters this function starts back and forward counter propagation
func propagateCounters(f *ir.Func, pass string) {
	debugFuncName, isDebug := os.LookupEnv("GOSSAFUNC")
	if isDebug && strings.Contains(ir.LinkFuncName(f), debugFuncName) && enableDebug {
		fmt.Printf("Start bbpgo debug on pass '%s' for func '%s'\n",
			pass, ir.LinkFuncName(f))
		bbDebugPrint = true
	}

	if f.ProfTable == nil {
		println("Nil prof table: ", ir.LinkFuncName(f))
		return
	}

	watched := map[ir.Node]bool{}
	backPropNodeListCounterRec(f, f.Body, 0, watched)
	watched = map[ir.Node]bool{}
	forwardPropNodeListCounterRec(f, f.Body, 0, watched)

	bbDebugPrint = false
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

	setCounter := func(nds ir.Nodes, s, e int, c ir.Counter) {
		for i := s; i <= e; i++ {
			n := nds[i]
			if ir.ShouldSetCounter(n) {
				if bbDebugPrint {
					fmt.Println("back_prop (list): ", printOp(n), " old: ", ir.GetCounter(f, n), " new: ", c)
				}
				ir.SetCounter(f, n, c)
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
func backPropNodeCounterRec(f *ir.Func, n ir.Node, depth int, watched map[ir.Node]bool) (ir.Counter, bool) {
	if n == nil {
		return 0, false
	}
	if watched[n] {
		return ir.GetCounter(f, n), false
	}
	watched[n] = true

	max := func(x, y ir.Counter) ir.Counter {
		if x > y {
			return x
		}
		return y
	}
	var mayReturn bool
	var count ir.Counter

	if n.Op() == ir.OIF {
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
	} else if ir.ShouldSetCounter(n) {
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

	count = max(count, ir.GetCounter(f, n))
	if bbDebugPrint {
		fmt.Println("back_prop: ", printOp(n), " old: ", ir.GetCounter(f, n), " new: ", count)
	}
	ir.SetCounter(f, n, count)

	return count, mayReturn
}

// forwardPropNodeListCounterRec for all nodes in the list launch forward propagation
func forwardPropNodeListCounterRec(f *ir.Func, nodes ir.Nodes, depth int, watched map[ir.Node]bool) {
	for _, n := range nodes {
		forwardPropNodeCounterRec(f, n, ir.GetCounter(f, n), depth, watched)
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

	if ir.ShouldSetCounter(n) {
		if bbDebugPrint {
			fmt.Println("forward_prop: ", printOp(n), " old: ", ir.GetCounter(f, n), " new: ", c)
		}
		ir.SetCounter(f, n, c)
	}

	if n.Op() == ir.OIF {
		n := n.(*ir.IfStmt)

		bodyCount := int64(0)
		bodyLen := len(n.Body)
		if bodyLen != 0 {
			// The first node has the maximal counter
			bodyCount = ir.GetCounter(f, n.Body[0])
		}
		elseCount := int64(0)
		elseLen := len(n.Else)
		if elseLen != 0 {
			// The first node has the maximal counter
			elseCount = ir.GetCounter(f, n.Else[0])
		}

		condCount := ir.GetCounter(f, n.Cond)

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
			ir.SetCounter(f, n.Body[0], bodyCount)
			forwardPropNodeListCounterRec(f, n.Body, depth+1, watched)
		}

		if elseLen != 0 {
			ir.SetCounter(f, n.Else[0], elseCount)
			forwardPropNodeListCounterRec(f, n.Else, depth+1, watched)
		}
		forwardPropNodeCounterRec(f, n.Cond, c, depth+1, watched)
	} else if n.Op() == ir.OFOR {
		n := n.(*ir.ForStmt)
		var bC, cC, pC int64
		if n.Body != nil {
			bC = c
			if len(n.Body) != 0 {
				bC = ir.GetCounter(f, n.Body[0])
			}
		}
		if n.Cond != nil {
			cC = ir.GetCounter(f, n.Cond)
		}
		if n.Post != nil {
			pC = ir.GetCounter(f, n.Post)
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
	} else if ir.ShouldSetCounter(n) {
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

//----------------------------- Inline correction functions

func setCounterToNodeRec(fc *pgo.FunctionsCounters, lc *pgo.LinesCounters, f *ir.Func, n ir.Node, depth int, watched map[ir.Node]bool, inlCount ir.Counter) {
	if watched[n] {
		return
	}
	watched[n] = true

	if lc != nil && inlCount != 0 {
		counter, ok := (*lc)[int64(n.Pos().Line())]
		if ok {
			ir.SetCounter(f, n, counter)

			if bbDebugPrint {
				fmt.Println("inline_correction init: ", printOp(n), " new: ", counter)
			}
		}
	}

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
			setCounterToNodeRec(fc, lc, f, val, depth+1, watched, inlCount)
		case ir.Nodes:
			inlineCorrectionNodeListCounterRec(fc, lc, f, val, depth+1, watched, inlCount)
		}
	}
}

func inlineCorrectionNodeListCounterRec(fc *pgo.FunctionsCounters, lc *pgo.LinesCounters, f *ir.Func, nodes ir.Nodes, depth int, watched map[ir.Node]bool, inlCount ir.Counter) {
	if nodes == nil {
		return
	}

	startFuncTable := lc
	curFuncTable := lc
	hadInl := false
	oldCounter := inlCount

	for _, n := range nodes {
		if n.Op() == ir.OINLMARK {
			n := n.(*ir.InlineMarkStmt)
			fSym := base.Ctxt.InlTree.InlinedFunction(int(n.Index))
			name := fSym.String()
			tmp := (*fc)[name]
			curFuncTable = &tmp
			inlCount = ir.GetCounter(f, n)
			hadInl = true

			if bbDebugPrint {
				fmt.Println("inline_correction: found INLMARK ", printOp(n), " for function: ", name, "with counter: ", inlCount)
			}
		}

		setCounterToNodeRec(fc, curFuncTable, f, n, depth+1, watched, inlCount)

		if n.Op() == ir.OLABEL && hadInl == true {
			curFuncTable = startFuncTable
			hadInl = false
			inlCount = oldCounter
		}
	}
}

// CorrectProfileAfterInline parses function, set counters only to inlined nodes
// and launches propagation of counters
func CorrectProfileAfterInline(fc *pgo.FunctionsCounters, f *ir.Func) {
	if fc == nil {
		return
	}

	debugFuncName, isDebug := os.LookupEnv("GOSSAFUNC")
	if isDebug && strings.Contains(ir.LinkFuncName(f), debugFuncName) && enableDebug {
		fmt.Printf("Start bbpgo debug  on pass 'after_inline' for: '%s'\n", ir.LinkFuncName(f))
		bbDebugPrint = true
	}

	watched := map[ir.Node]bool{}
	inlineCorrectionNodeListCounterRec(fc, nil, f, f.Body, 0, watched, 0)

	if bbDebugPrint {
		fmt.Printf("Finish bbpgo debug  on pass 'after_inline' for: '%s'\n", ir.LinkFuncName(f))
	}

	bbDebugPrint = false
}

//----------------------------- SSA correction functions

// For the given counters on the AST we translate counters to the SSA
func SetBBCounters(irFn *ir.Func, ssaFn *ssa.Func) {

	debugFuncName, isDebug := os.LookupEnv("GOSSAFUNC")
	if isDebug && strings.Contains(ir.LinkFuncName(irFn), debugFuncName) && enableDebug {
		fmt.Printf("Start bbpgo debug  on pass 'buildssa' for: '%s'\n", ir.LinkFuncName(irFn))
		bbDebugPrint = true
	}

	if irFn.ProfTable == nil {
		return
	}

	ssaFn.ProfTable = make(ssa.NodeProfTable)
	getMaxCounter := func(bb *ssa.Block) ir.Counter {
		maxC := ir.Counter(0)
		for _, v := range bb.Values {
			if v.Op == ssa.OpPanicBounds {
				return 0
			}
			if v.Op == ssa.OpStaticCall || v.Op == ssa.OpStaticLECall {
				s := v.Aux.(*ssa.AuxCall).Fn.String()
				switch s {
				case "runtime.racefuncenter", "runtime.racefuncexit",
					"runtime.panicdivide", "runtime.panicwrap",
					"runtime.panicshift":
					return 0
				}
			}
			c := ir.GetCounterByPos(irFn, v.Pos)
			if maxC < c {
				maxC = c
			}
		}
		return maxC
	}

	for _, b := range ssaFn.Blocks {
		c := getMaxCounter(b)
		ssa.SetCounter(ssaFn, b, ssa.Counter(c))
		if bbDebugPrint {
			fmt.Printf("Set counter %d to b%d on line %d\n", c, b.ID, b.Pos.Line())
		}
	}

	bbDebugPrint = false
}
