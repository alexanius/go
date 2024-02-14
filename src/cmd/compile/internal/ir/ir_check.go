// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// “Abstract” syntax representation.

package ir

import (
	"cmd/compile/internal/base"
)

// CheckIR checks function IR concistency
func CheckIR(fn *Func) {
	if !base.Flag.BbPgoProfile {
		return
	}
	return
	VisitList(fn.Body, func(n Node) {
		switch n.Op() {
		case OIF:
			n := n.(*IfStmt)
			var c, bC, eC int64
			c = n.Counter()
			if n.Body != nil && len(n.Body) > 0 {
				bC = n.Body[0].Counter()
			}
			if n.Body != nil && len(n.Else) > 0 {
				eC = n.Else[0].Counter()
			}
			if c < bC+eC {
				base.FatalfAt(n.Pos(), "Incorrect edges counter for IF node %d + %d > %d", bC, eC, c)
			}
		case OCHECKNIL:
			if n.Counter() != 0 {
				base.FatalfAt(n.Pos(), "Non-zero NilCheck counter")
			}
		}
	})
}
