// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa

import (
	"cmd/compile/internal/base"
	"fmt"
	"math"
	"sort"
)

// layout orders basic blocks in f with the goal of minimizing control flow instructions.
// After this phase returns, the order of f.Blocks matters and is the order
// in which those blocks will appear in the assembly output.
func layout(f *Func) {
	if base.Flag.PGOBbExttsp /*&& profile != nil*/ {

		// Sometimes the first block occurs not entry block. Fixing it
		entryIndex := 0
		for i, b := range f.Blocks {
			if b == f.Entry {
				entryIndex = i
			}
		}
		if entryIndex != 0 {
			f.Blocks[entryIndex] = f.Blocks[0]
			f.Blocks[0] = f.Entry
		}

			if len(f.Blocks) < 10 {
				f.Blocks = layoutTsp(f)
			} else {
//				f.Blocks = layoutOrder(f)
				f.Blocks = layoutExttsp(f)
			}
		} else {
			f.Blocks = layoutOrder(f)
		}
}

func layoutTsp(f *Func) []*Block {
	N := len(f.Blocks)
	Weight := make([][]uint64, N)
	for i := 0; i < N; i++ {
		Weight[i] = make([]uint64, N)
	}
	IndexToBB := make([]*Block, N)

	// Populating weight map and index map
	for i, b := range f.Blocks {
		b.LayoutIndex = i
		IndexToBB[i] = b
	}

	for _, b := range f.Blocks {
		for _, e := range b.Succs {
			counter := GetCounter(f, e.b)
			if counter == 0 {
				continue
			}
			Weight[b.LayoutIndex][e.b.LayoutIndex] = (uint64)(counter)

		}
	}
	DP := make([][]int64, 1<<N)
	for i := 0; i < (1 << N); i++ {
		DP[i] = make([]int64, N)
		for j := 0; j < N; j++ {
			DP[i][j] = -1
		}
	}
	// Start with the entry basic block being allocated with cost zero
	DP[1][0] = 0
	var BestSet uint64 = 1
	var BestLast uint64 = 0
	var BestWeight int64 = 0
	var Set uint64
	var Last uint64
	var New uint64
	for Set = 1; Set < (1 << N); Set++ {
		// Traverse each possibility of Last BB visited in this layout
		for Last = 0; Last < uint64(N); Last++ {
			// Case 1: There is no possible layout with this BB as Last
			if DP[Set][Last] == -1 {
				continue
			}
			// Case 2: There is a layout with this Set and this Last, and we try
			// to expand this set with New
			for New = 1; New < uint64(N); New++ {
				if Set&(1<<New) != 0 {
					continue
				}
				// Case 2b: BB "New" is not in this set and we add it to this Set and
				// record total weight of this layout with "New" as the last BB.
				var NewSet uint64 = Set | (1 << New)
				if DP[NewSet][New] == -1 {
					DP[NewSet][New] = DP[Set][Last] + (int64)(Weight[Last][New])
				}
				DP[NewSet][New] = int64(math.Max(float64(DP[NewSet][New]), float64(DP[Set][Last]+(int64)(Weight[Last][New]))))
				if DP[NewSet][New] > BestWeight {
					BestWeight = DP[NewSet][New]
					BestSet = NewSet
					BestLast = New
				}
			}
		}
	}
	// Define final function layout based on layout that maximizes weight
	Last = BestLast
	Set = BestSet
	Visited := map[uint64]bool{}
	Visited[Last] = true
	Order := make([]*Block, 0, f.NumBlocks())
	Order = append(Order, IndexToBB[Last])
	Set = Set & ^(1 << Last)
	var I uint64
	for Set != 0 {
		var Best int64 = -1
		var NewLast uint64
		for I = 0; I < uint64(N); I++ {
			if DP[Set][I] == -1 {
				continue
			}
			var AdjWeight int64 = 0
			if Weight[I][Last] > 0 {
				AdjWeight = int64(Weight[I][Last])
			}
			if DP[Set][I]+AdjWeight > Best {
				NewLast = I
				Best = DP[Set][I] + AdjWeight
			}
		}
		Last = NewLast
		Visited[Last] = true
		Order = append(Order, IndexToBB[Last])
		Set = Set & ^(1 << Last)
	}
	for i, j := 0, len(Order)-1; i < j; i, j = i+1, j-1 {
		Order[i], Order[j] = Order[j], Order[i]
	}
	// Finalize layout with BBs that weren't assigned to the layout using the
	// input layout.
	for _, b := range f.Blocks {
		if _, ok := Visited[uint64(b.LayoutIndex)]; !ok {
			Order = append(Order, b)
		}
	}
	return Order
}


var ForwardDistance uint64 = 1024
var BackwardDistance uint64 = 640
var ForwardWeight float64 = 0.1
var BackwardWeight float64 = 0.1
var EPS = 1e-8
var ChainSplitThreshold = 128
var TSPThreshold = 10
var ColdThreshold uint64 = 10
// A wrapper around three chains of basic blocks; it is used to avoid extra
// instantiation of the vectors.
type MergedChain struct {
	Ptr1   []*CBlock
	Begin1 int
	End1   int
	Ptr2   []*CBlock
	Begin2 int
	End2   int
	Ptr3   []*CBlock
	Begin3 int
	End3   int
	Result []*CBlock
}
func (M *MergedChain) initialize(Ptr1 []*CBlock, Begin1 int, End1 int, Ptr2 []*CBlock, Begin2 int, End2 int, Ptr3 []*CBlock, Begin3 int, End3 int) {
	M.Ptr1 = Ptr1
	M.Begin1 = Begin1
	M.End1 = End1
	M.Ptr2 = Ptr2
	M.Begin2 = Begin2
	M.End2 = End2
	M.Ptr3 = Ptr3
	M.Begin3 = Begin3
	M.End3 = End3
	M.Result = make([]*CBlock, 0)
	for I := M.Begin1; I < M.End1; I++ {
		M.Result = append(M.Result, M.Ptr1[I])
	}
	for I := M.Begin2; I < M.End2; I++ {
		M.Result = append(M.Result, M.Ptr2[I])
	}
	for I := M.Begin3; I < M.End3; I++ {
		M.Result = append(M.Result, M.Ptr3[I])
	}
}
func (M *MergedChain) getBlocks() []*CBlock {
	return M.Result
}
type Jump struct {
	B *CBlock
	V uint64
}
// A node in CFG corresponding to a BasicBlock.
// The class wraps several mutable fields utilized in the ExtTSP algorithm
type CBlock struct {
	// Corresponding basic block
	BB *Block
	// Current chain of the basic block
	CurChain *Chain
	// (Estimated) size of the block
	Size uint64
	// Execution count of the block
	ExecutionCount uint64
	// An original index of the node in CFG
	Index int
	// The index of the block in the current chain
	CurIndex int
	// An offset of the block in the current chain
	EstimatedAddr uint64
	// Fallthrough successor of the node in CFG
	FallthroughSucc *CBlock
	// Fallthrough predecessor of the node in CFG
	FallthroughPred *CBlock
	// Outgoing jumps from the block
	OutJumps []Jump
	// Incoming jumps to the block
	InJumps []Jump
	// Total execution count of incoming jumps
	InWeight uint64
	// Total execution count of outgoing jumps
	OutWeight uint64
}
type ChainEdge struct {
	C *Chain
	E *CEdge
}
// A chain (ordered sequence) of CFG nodes (basic blocks)
type Chain struct {
	Id             int
	IsEntry        bool
	ExecutionCount uint64
	Size           uint64
	Score          float64
	CBlocks        []*CBlock
	CEdges         []ChainEdge
}
func (C *Chain) density() float64 {
	return float64(C.ExecutionCount) / float64(C.Size)
}
func (C *Chain) removCEdge(Other *Chain) {
	ret := make([]ChainEdge, 0)
	index := 0
	for ; index < len(C.CEdges); index++ {
		if C.CEdges[index].C == Other {
			break
		}
	}
	if index == len(C.CEdges) {
		panic("func removCEdge: out of bound")
	}
	if index != 0 {
		ret = append(ret, C.CEdges[:index]...)
	}
	if index != len(C.CEdges)-1 {
		ret = append(ret, C.CEdges[index+1:]...)
	}
	C.CEdges = ret
}
func (C *Chain) clear() {
	C.CBlocks = make([]*CBlock, 0)
	C.CEdges = make([]ChainEdge, 0)
}
func (C *Chain) mergCEdges(Other *Chain) {
	for Idx := range Other.CEdges {
		EdgeIt := &Other.CEdges[Idx]
		DstChain := EdgeIt.C
		DstEdge := EdgeIt.E
		var TargetChain *Chain
		if DstChain == Other {
			TargetChain = C
		} else {
			TargetChain = DstChain
		}
		curEdge := C.getEdge(TargetChain)
		if curEdge == nil {
			DstEdge.changeEndPoint(Other, C)
			C.addEdge(TargetChain, DstEdge)
			if DstChain != C && DstChain != Other {
				DstChain.addEdge(C, DstEdge)
			}
		} else {
			curEdge.moveJumps(DstEdge)
		}
		if DstChain != Other {
			DstChain.removCEdge(Other)
		}
	}
}
func (C *Chain) merge(Other *Chain, MergedBlocks []*CBlock) {
	C.CBlocks = MergedBlocks
	C.IsEntry = C.IsEntry || Other.IsEntry
	C.ExecutionCount += Other.ExecutionCount
	C.Size += Other.Size
	for Idx := range C.CBlocks {
		Bb := &C.CBlocks[Idx]
		(*Bb).CurChain = C
		(*Bb).CurIndex = Idx
	}
}
func (C *Chain) initialize(Id int, Bb *CBlock) {
	C.Id = Id
	if Bb.Index == 0 {
		C.IsEntry = true
	} else {
		C.IsEntry = false
	}
	C.ExecutionCount = Bb.ExecutionCount
	C.Size = Bb.Size
	C.Score = 0.0
	C.CBlocks = make([]*CBlock, 0)
	C.CBlocks = append(C.CBlocks, Bb)
}
func (C *Chain) getEdge(Other *Chain) *CEdge {
	for Idx := range C.CEdges {
		E := &C.CEdges[Idx]
		if E.C == Other {
			return E.E
		}
	}
	return nil
}
func (C *Chain) addEdge(Other *Chain, E *CEdge) {
	C.CEdges = append(C.CEdges, ChainEdge{C: Other, E: E})
}
type JumpList struct {
	B1 *CBlock
	B2 *CBlock
	V  uint64
}
func (J *JumpList) initialize(SrcBlock *CBlock, DstBlock *CBlock, EC uint64) {
	J.B1 = SrcBlock
	J.B2 = DstBlock
	J.V = EC
}
// An edge in CFG reprsenting jumps between chains of BasicBlocks.
// When blocks are merged into chains, the edges are combined too so that
// there is always at most one edge between a pair of chains
type CEdge struct {
	SrcChain           *Chain
	DstChain           *Chain
	Jumps              []JumpList
	CachedGainForward  MergeGainTy
	CachedGainBackward MergeGainTy
	CacheValidForward  bool
	CacheValidBackward bool
}
// The compiler adds the input J into Jumps if J does not appear in Jumps.
func appendUnique(Jumps []JumpList, J JumpList) []JumpList {
	duplicate := false
	for _, I := range Jumps {
		if J.B1 == I.B1 && J.B2 == I.B2 {
			duplicate = true
			break
		}
	}
	if duplicate == false {
		Jumps = append(Jumps, J)
	}
	return Jumps
}
func (E *CEdge) moveJumps(Other *CEdge) {
	for Idx := range Other.Jumps {
		J := &Other.Jumps[Idx]
		E.Jumps = appendUnique(E.Jumps, *J)
	}
	Other.Jumps = make([]JumpList, 0)
}
func (E *CEdge) changeEndPoint(From *Chain, To *Chain) {
	if From == E.SrcChain {
		E.SrcChain = To
	}
	if From == E.DstChain {
		E.DstChain = To
	}
}
func (E *CEdge) initialize(SrcBlock *CBlock, DstBlock *CBlock, EC uint64) {
	E.SrcChain = SrcBlock.CurChain
	E.DstChain = DstBlock.CurChain
	E.Jumps = make([]JumpList, 0)
	E.appendJump(SrcBlock, DstBlock, EC)
}
func (E *CEdge) hasCachedMergeGain(Src *Chain, Dst *Chain) bool {
	if Src == E.SrcChain {
		return E.CacheValidForward
	} else {
		return E.CacheValidBackward
	}
}
func (E *CEdge) getCachedMergeGain(Src *Chain, Dst *Chain) MergeGainTy {
	if Src == E.SrcChain {
		return E.CachedGainForward
	} else {
		return E.CachedGainBackward
	}
}
func (E *CEdge) setCachedMergeGain(Src *Chain, Dst *Chain, MergeGain MergeGainTy) {
	if Src == E.SrcChain {
		E.CachedGainForward = MergeGain
		E.CacheValidForward = true
	} else {
		E.CachedGainBackward = MergeGain
		E.CacheValidBackward = true
	}
}
func (E *CEdge) appendJump(SrcBlock *CBlock, DstBlock *CBlock, EC uint64) {
	for _, Jump := range E.Jumps {
		if SrcBlock == Jump.B1 && DstBlock == Jump.B2 {
			return
		}
	}
	E.Jumps = append(E.Jumps, JumpList{})
	E.Jumps[len(E.Jumps)-1].initialize(SrcBlock, DstBlock, EC)
}
type MergeTypeTy int64
const (
	X_Y     MergeTypeTy = 0
	X1_Y_X2             = 1
	Y_X2_X1             = 2
	X2_X1_Y             = 3
)
type MergeGainTy struct {
	Score       float64
	MergeOffset int
	MergeType   MergeTypeTy
}
func (M *MergeGainTy) isLessThan(Other MergeGainTy) bool {
	if Other.Score > EPS && Other.Score > M.Score+EPS {
		return true
	} else {
		return false
	}
}
type ExtTSP struct {
	// The function
	f *Func
	// All CFG nodes (basic blocks)
	AllBlocks []CBlock
	// All chains of blocks
	AllChains []*Chain
	// Active chains. The vector gets updated at runtime when chains are merged
	HotChains []*Chain
	// All edges between chains
	AllEdges []CEdge
}
func NewExtTSP(f *Func) *ExtTSP {
	E := new(ExtTSP)
	E.f = f
	return E
}
func computeCodeSize(b *Block) int {
	count := 0
	for _, v := range b.Values {
		if v.Op != OpPhi {
			count = count + 1
		}
	}
	if count == 0 {
		count = 1
	}
	return count
}
// Initialize algorithm's data structures
func (E *ExtTSP) initialize() {
	// Initialize CFG nodes
	E.AllBlocks = make([]CBlock, 0)
	for Idx := range E.f.Blocks {
		b := &E.f.Blocks[Idx]
		(*b).LayoutIndex = Idx
		size := computeCodeSize(*b)
		E.AllBlocks = append(E.AllBlocks, CBlock{
			BB:              *b,
			CurChain:        nil,
			Size:            uint64(size),
			ExecutionCount:  uint64(GetCounter(E.f, *b)),
			Index:           int((*b).LayoutIndex),
			CurIndex:        0,
			EstimatedAddr:   0,
			FallthroughSucc: nil,
			FallthroughPred: nil,
			OutJumps:        make([]Jump, 0),
			InJumps:         make([]Jump, 0),
			InWeight:        0,
			OutWeight:       0})
	}
	if E.f.pass.debug > 2 {
		fmt.Printf("All Blocks:\n")
		for _, Bb := range E.AllBlocks {
			fmt.Printf("b%d %d\n", Bb.BB.ID, Bb.ExecutionCount)
		}
	}
	if E.f.pass.debug > 2 {
		fmt.Printf("All out jumps:\n")
	}
	// Initialize edges for the blocks and compute their total in/out weights
	NumEdges := 0
	for Idx := range E.AllBlocks {
		Bb := &E.AllBlocks[Idx]
		for _, e := range Bb.BB.Succs {
			if Bb.BB != e.b {
				if GetCounter(E.f, Bb.BB) != 0 &&  GetCounter(E.f, e.b) != 0 /*e.EdgeFreq.RawCount == 0*/ {
					Count := GetCounter(E.f, Bb.BB)//uint64(e.EdgeFreq.RawCount) // TODO
					if E.f.pass.debug > 2 {
						fmt.Printf("double check b%d b%d %d\n", Bb.BB.ID, e.b.ID, Count)
					}
				}
				if /*e.EdgeFreq.RawCount*/ GetCounter(E.f, Bb.BB) != 0 {
					Count := uint64(GetCounter(E.f, Bb.BB)) // uint64(e.EdgeFreq.RawCount) // TODO
					E.AllBlocks[e.b.LayoutIndex].InWeight = E.AllBlocks[e.b.LayoutIndex].InWeight + Count
					E.AllBlocks[e.b.LayoutIndex].InJumps = append(E.AllBlocks[e.b.LayoutIndex].InJumps, Jump{B: Bb, V: Count})
					Bb.OutWeight = Bb.OutWeight + Count
					Bb.OutJumps = append(Bb.OutJumps, Jump{B: &E.AllBlocks[e.b.LayoutIndex], V: Count})
					if E.f.pass.debug > 2 {
						fmt.Printf("b%d b%d %d\n", Bb.BB.ID, e.b.ID, Count)
					}
					NumEdges = NumEdges + 1
				}
			}
		}
	}
	// Initialize execution count for every basic block, which is the
	// maximum over the sums of all in and out edge weights.
	for Idx := range E.AllBlocks {
		Bb := &E.AllBlocks[Idx]
		Bb.ExecutionCount = uint64(math.Max(float64(Bb.ExecutionCount), float64(Bb.InWeight)))
		Bb.ExecutionCount = uint64(math.Max(float64(Bb.ExecutionCount), float64(Bb.OutWeight)))
	}
	// Initialize chains
	E.AllChains = make([]*Chain, 0)
	E.HotChains = make([]*Chain, 0)
	for Idx := range E.AllBlocks {
		Bb := &E.AllBlocks[Idx]
		C := new(Chain)
		E.AllChains = append(E.AllChains, C)
		C.initialize(Bb.Index, Bb)
		Bb.CurChain = C
		// The value 10 is hard coded here for performance tuning.
		// TODO. The value should be calculate based on the profiling
		// information of all basic blocks.
		if Bb.ExecutionCount > ColdThreshold {
			E.HotChains = append(E.HotChains, C)
		}
	}
	// Initialize edges
	if E.f.pass.debug > 2 {
		fmt.Printf("All edges:\n")
	}
	E.AllEdges = make([]CEdge, 0)
	for Idx := range E.AllBlocks {
		Bb := &E.AllBlocks[Idx]
		for I := range Bb.OutJumps {
			J := &Bb.OutJumps[I]
			SuccBlock := J.B
			CurEdge := Bb.CurChain.getEdge(SuccBlock.CurChain)
			if CurEdge != nil {
				CurEdge.appendJump(Bb, SuccBlock, J.V)
				continue
			}
			E.AllEdges = append(E.AllEdges, CEdge{})
			E.AllEdges[len(E.AllEdges)-1].initialize(Bb, SuccBlock, J.V)
			if E.f.pass.debug > 2 {
				fmt.Printf("b%d b%d %d\n", Bb.BB.ID, SuccBlock.BB.ID, J.V)
			}
			Bb.CurChain.addEdge(SuccBlock.CurChain, &E.AllEdges[len(E.AllEdges)-1])
			SuccBlock.CurChain.addEdge(Bb.CurChain, &E.AllEdges[len(E.AllEdges)-1])
		}
	}
}
// For a pair of blocks, A and B, block B is the fallthrough successor of A,
// if (i) all jumps (based on profile) from A goes to B and (ii) all jumps
// to B are from A. Such blocks should be adjacent in an optimal ordering;
// the method finds and merges such pairs of blocks
func (E *ExtTSP) mergeFallthroughs() {
	for Idx := range E.AllBlocks {
		Bb := &E.AllBlocks[Idx]
		if len(Bb.BB.Succs) == 1 &&
			len(Bb.BB.Succs[0].b.Preds) == 1 &&
			Bb.BB.Succs[0].b.LayoutIndex != 0 {
			SuccIndex := Bb.BB.Succs[0].b.LayoutIndex
			Bb.FallthroughSucc = &E.AllBlocks[SuccIndex]
			E.AllBlocks[SuccIndex].FallthroughPred = Bb
			continue
		}
		if Bb.OutWeight == 0 {
			continue
		}
		for _, Ee := range Bb.OutJumps {
			SuccBlock := Ee.B
			if Bb.OutWeight == Ee.V &&
				SuccBlock.InWeight == Ee.V &&
				SuccBlock.Index != 0 {
				Bb.FallthroughSucc = SuccBlock
				SuccBlock.FallthroughPred = Bb
				break
			}
		}
	}
	// There might be 'cycles' in the fallthrough dependencies (since profile
	// data isn't 100% accurate).
	// Break the cycles by choosing the block with smallest index as the tail
	for Idx := range E.AllBlocks {
		Bb := &E.AllBlocks[Idx]
		if Bb.FallthroughSucc == nil ||
			Bb.FallthroughPred == nil {
			continue
		}
		SuccBlock := Bb.FallthroughSucc
		for SuccBlock != nil && SuccBlock != Bb {
			SuccBlock = SuccBlock.FallthroughSucc
		}
		if SuccBlock == nil {
			continue
		}
		// break the cycle
		E.AllBlocks[Bb.FallthroughPred.Index].FallthroughSucc = nil
		Bb.FallthroughPred = nil
	}
	// Merge blocks with their fallthrough successors
	for Idx := range E.AllBlocks {
		Bb := &E.AllBlocks[Idx]
		if Bb.FallthroughPred == nil &&
			Bb.FallthroughSucc == nil {
			CurBlock := Bb
			for CurBlock.FallthroughSucc != nil {
				NextBlock := CurBlock.FallthroughSucc
				E.mergeChains(Bb.CurChain, NextBlock.CurChain, 0, X_Y)
				CurBlock = NextBlock
			}
		}
	}
}
// Merge two chains and update the best Gain
func (E *ExtTSP) computeMergeGain(CurGain MergeGainTy, ChainPred *Chain, ChainSucc *Chain, Jumps []JumpList, MergeOffset int, MergeType MergeTypeTy) MergeGainTy {
	MergedBlocks := E.mergeBlocks(ChainPred.CBlocks, ChainSucc.CBlocks, MergeOffset, MergeType)
	// Do not allow a merge that does not preserve the original entry block
	if (ChainPred.IsEntry || ChainSucc.IsEntry) &&
		MergedBlocks.getBlocks()[0].Index != 0 {
		return CurGain
	}
	// The gain for the new chain
	NewScore := E.score(MergedBlocks, Jumps) - ChainPred.Score
	NewGain := MergeGainTy{
		Score:       NewScore,
		MergeOffset: MergeOffset,
		MergeType:   MergeType}
	if CurGain.isLessThan(NewGain) {
		return NewGain
	} else {
		return CurGain
	}
}
// Merge two chains of blocks respecting a given merge 'type' and 'offset'
//
// If MergeType == 0, then the result is a concatentation of two chains.
// Otherwise, the first chain is cut into two sub-chains at the offset,
// and merged using all possible ways of concatenating three chains.
func (E *ExtTSP) mergeBlocks(X []*CBlock, Y []*CBlock, MergeOffset int, MergeType MergeTypeTy) MergedChain {
	BeginX1 := 0
	EndX1 := MergeOffset
	BeginX2 := MergeOffset
	EndX2 := len(X)
	BeginY := 0
	EndY := len(Y)
	// Construct a new chain from the three existing ones
	switch MergeType {
	case X_Y:
		var M MergedChain
		M.initialize(X, BeginX1, EndX2, Y, BeginY, EndY, Y, 0, 0)
		return M
	case X1_Y_X2:
		var M MergedChain
		M.initialize(X, BeginX1, EndX1, Y, BeginY, EndY, X, BeginX2, EndX2)
		return M
	case Y_X2_X1:
		var M MergedChain
		M.initialize(Y, BeginY, EndY, X, BeginX2, EndX2, X, BeginX1, EndX1)
		return M
	default: // X2_X1_Y
		var M MergedChain
		M.initialize(X, BeginX2, EndX2, X, BeginX1, EndX1, Y, BeginY, EndY)
		return M
	}
}
// Calculate Ext-TSP value, which quantifies the expected number of i-cache
// misses for a given ordering of basic blocks
func extTSPScore(SrcAddr uint64, SrcSize uint64, DstAddr uint64, Count uint64) float64 {
	// Fallthrough
	if SrcAddr+SrcSize == DstAddr {
		return float64(Count)
	}
	// Forward
	if SrcAddr+SrcSize < DstAddr {
		Dist := DstAddr - (SrcAddr + SrcSize)
		if Dist < ForwardDistance {
			Prob := 1.0 - float64(Dist)/float64(ForwardDistance)
			return ForwardWeight * Prob * float64(Count)
		}
		return 0.0
	}
	// Backward
	Dist := SrcAddr + SrcSize - DstAddr
	if Dist <= BackwardDistance {
		Prob := 1.0 - float64(Dist)/float64(BackwardDistance)
		return BackwardWeight * Prob * float64(Count)
	}
	return 0.0
}
// Compute ExtTSP score for a given order of basic blocks
func (E *ExtTSP) score(MergedBlocks MergedChain, Jumps []JumpList) float64 {
	if len(Jumps) == 0 {
		return 0.0
	}
	var CurAddr uint64 = 0.0
	for _, Bb := range MergedBlocks.getBlocks() {
		Bb.EstimatedAddr = CurAddr
		CurAddr += Bb.Size
	}
	var Score float64 = 0
	for _, Jump := range Jumps {
		SrcBlock := Jump.B1
		DstBlock := Jump.B2
		Score = Score + extTSPScore(SrcBlock.EstimatedAddr, SrcBlock.Size, DstBlock.EstimatedAddr, Jump.V)
	}
	return Score
}
func (E *ExtTSP) removeHotChainElement(From *Chain) {
	ret := make([]*Chain, 0)
	index := 0
	for ; index < len(E.HotChains); index++ {
		if E.HotChains[index] == From {
			break
		}
	}
	if index == len(E.HotChains) {
		return
	}
	if index != 0 {
		ret = append(ret, E.HotChains[:index]...)
	}
	if index != len(E.HotChains)-1 {
		ret = append(ret, E.HotChains[index+1:]...)
	}
	E.HotChains = ret
}
// Merge chain From into chain Into, update the list of active chains,
// adjacency information, and the corresponding cached values
func (E *ExtTSP) mergeChains(Into *Chain, From *Chain, MergeOffset int, MergeType MergeTypeTy) {
	// Merge the blocks
	MergedBlocks := E.mergeBlocks(Into.CBlocks, From.CBlocks, MergeOffset, MergeType)
	Into.merge(From, MergedBlocks.getBlocks())
	Into.mergCEdges(From)
	From.clear()
	// Update cached ext-tsp score for the new chain
	SelfEdge := Into.getEdge(Into)
	if SelfEdge != nil {
		MergedBlocks.initialize(Into.CBlocks, 0, len(Into.CBlocks), Into.CBlocks, 0, 0, Into.CBlocks, 0, 0)
		Into.Score = E.score(MergedBlocks, SelfEdge.Jumps)
	}
	// Remove chain From from the list of active chains
	E.removeHotChainElement(From)
	for Idx := range Into.CEdges {
		EdgeIter := &Into.CEdges[Idx]
		EdgeIter.E.CacheValidForward = false
		EdgeIter.E.CacheValidBackward = false
	}
}
// Compute the gain of merging two chains
//
// The function considers all possible ways of merging two chains and
// computes the one having the largest increase in ExtTSP objective. The
// result is a pair with the first element being the gain and the second
// element being the corresponding merging type.
func (E *ExtTSP) mergeGain(ChainPred *Chain, ChainSucc *Chain, Edge *CEdge) MergeGainTy {
	if Edge.hasCachedMergeGain(ChainPred, ChainSucc) {
		return Edge.getCachedMergeGain(ChainPred, ChainSucc)
	}
	// Precompute jumps between ChainPred and ChainSucc
	EdgePP := ChainPred.getEdge(ChainPred)
	if EdgePP != nil {
		for _, J := range EdgePP.Jumps {
			Edge.Jumps = appendUnique(Edge.Jumps, J)
		}
	}
	Gain := MergeGainTy{
		Score:       -1.0,
		MergeOffset: 0,
		MergeType:   X_Y}
	// Try to concatenate two chains w/o splitting
	Gain = E.computeMergeGain(Gain, ChainPred, ChainSucc, Edge.Jumps, 0, X_Y)
	// Try to break ChainPred in various ways and concatenate with ChainSucc
	if len(ChainPred.CBlocks) < ChainSplitThreshold {
		for Offset := 1; Offset < len(ChainPred.CBlocks); Offset++ {
			BB1 := ChainPred.CBlocks[Offset-1]
			if BB1.FallthroughSucc != nil {
				continue
			}
			Gain = E.computeMergeGain(Gain, ChainPred, ChainSucc, Edge.Jumps, Offset, X1_Y_X2)
			Gain = E.computeMergeGain(Gain, ChainPred, ChainSucc, Edge.Jumps, Offset, Y_X2_X1)
			Gain = E.computeMergeGain(Gain, ChainPred, ChainSucc, Edge.Jumps, Offset, X2_X1_Y)
		}
	}
	Edge.setCachedMergeGain(ChainPred, ChainSucc, Gain)
	return Gain
}
// Deterministically compare pairs of chains
func compareChainPairs(A1 *Chain, B1 *Chain, A2 *Chain, B2 *Chain) bool {
	Samples1 := A1.ExecutionCount + B1.ExecutionCount
	Samples2 := A2.ExecutionCount + B2.ExecutionCount
	if Samples1 != Samples2 {
		return Samples1 < Samples2
	}
	if A1 != A2 {
		return A1.Id < A2.Id
	}
	return B1.Id < B2.Id
}
// Merge pairs of chains while improving the ExtTSP objective
func (E *ExtTSP) mergeChainPairs() {
	i := 0
	for len(E.HotChains) > 1 {
		// The following code is under debug control.
		if E.f.pass.debug > 2 {
			fmt.Printf("iteration ---%v\n", i)
			for Idx := range E.HotChains {
				ChainTmp := &E.HotChains[Idx]
				fmt.Printf("%v %v %v %v %v %v\n", (*ChainTmp).Id, (*ChainTmp).ExecutionCount, (*ChainTmp).Size, (*ChainTmp).Score, len((*ChainTmp).CBlocks), len((*ChainTmp).CEdges))
				for _, Bb := range (*ChainTmp).CBlocks {
					fmt.Printf("b%d(%d) ", Bb.BB.ID, Bb.ExecutionCount)
				}
				fmt.Printf("\n")
				for _, Ce := range (*ChainTmp).CEdges {
					fmt.Printf("c%d c%d\n", Ce.E.SrcChain.Id, Ce.E.DstChain.Id)
				}
			}
			i = i + 1
		}
		var BestChainPred *Chain = nil
		var BestChainSucc *Chain = nil
		BestGain := MergeGainTy{
			Score:       -1.0,
			MergeOffset: 0,
			MergeType:   X_Y}
		for Idx := range E.HotChains {
			ChainPred := &E.HotChains[Idx]
			for Jdx := range (*ChainPred).CEdges {
				EdgeIter := &(*ChainPred).CEdges[Jdx]
				ChainSucc := EdgeIter.C
				ChainEdge := EdgeIter.E
				if *ChainPred == ChainSucc {
					continue
				}
				// The source and destination of the chain edge should match with
				// the incoming *ChainPred and ChainSucc.
				if ChainEdge.SrcChain != *ChainPred || ChainEdge.DstChain != ChainSucc {
					continue
				}
				// Only the hotchains are allowed to be merged.
				if (*ChainPred).ExecutionCount < ColdThreshold || ChainSucc.ExecutionCount < ColdThreshold {
					continue
				}
				// Compute the gain of merging the two chains
				CurGain := E.mergeGain(*ChainPred, ChainSucc, ChainEdge)
				if BestGain.isLessThan(CurGain) ||
					(math.Abs(CurGain.Score-BestGain.Score) < EPS &&
						BestChainPred != nil &&
						BestChainSucc != nil &&
						compareChainPairs(*ChainPred, ChainSucc, BestChainPred, BestChainSucc)) {
					BestGain = CurGain
					BestChainPred = *ChainPred
					BestChainSucc = ChainSucc
				}
			}
		}
		// Stop merging when there is no improvement
		if BestGain.Score <= EPS {
			break
		}
		// Merge the best pair of chains
		E.mergeChains(BestChainPred, BestChainSucc, BestGain.MergeOffset, BestGain.MergeType)
	}
}
// Merge cold blocks to reduce code size
func (E *ExtTSP) mergeColdChains() {
	for _, SrcBB := range E.f.Blocks {
		// Iterating in reverse order to make sure original fallthrough jumps are
		// merged first
		for Idx := len(SrcBB.Succs) - 1; Idx >= 0; Idx-- {
			Itr := &SrcBB.Succs[Idx]
			DstBB := Itr.b
			SrcIndex := SrcBB.LayoutIndex
			DstIndex := DstBB.LayoutIndex
			SrcChain := E.AllBlocks[SrcIndex].CurChain
			DstChain := E.AllBlocks[DstIndex].CurChain
			// The compiler should avoid merging the hot chain with the cold chain.
			// Ideally, chain A should be placed next to chain B if the tail of A reaches
			// the head of B. Here more conditions are added so that hot chain is expected to
			// connects hot chain while the cold chain is connected with cold chain.
			if SrcChain != DstChain && !DstChain.IsEntry && SrcChain.CBlocks[len(SrcChain.CBlocks)-1].Index == SrcIndex && DstChain.CBlocks[0].Index == DstIndex && ((SrcChain.ExecutionCount <= ColdThreshold && DstChain.ExecutionCount <= ColdThreshold) || (SrcChain.ExecutionCount > ColdThreshold && DstChain.ExecutionCount > ColdThreshold)) {
				E.mergeChains(SrcChain, DstChain, 0, X_Y)
			}
		}
	}
}
// Concatenate all chains into a final order
func (E *ExtTSP) concatChains() []*Block {
	var SortedChains []*Chain
	SortedChains = make([]*Chain, 0)
	if E.f.pass.debug > 2 {
		fmt.Printf("All chains\n")
	}
	if E.f.pass.debug > 2 {
		fmt.Printf("Before Sorting\n")
	}
	for Idx := range E.AllChains {
		C := &E.AllChains[Idx]
		if len((*C).CBlocks) > 0 {
			if E.f.pass.debug > 2 {
				fmt.Printf("Chain:\n")
				for _, Bb := range (*C).CBlocks {
					fmt.Printf("b%d %d\n", Bb.BB.ID, Bb.ExecutionCount)
				}
			}
			SortedChains = append(SortedChains, *C)
		}
	}
	// Sorting chains by density in decreasing order
	sort.Slice(SortedChains, func(i, j int) bool {
		if SortedChains[i].IsEntry != SortedChains[j].IsEntry {
			if SortedChains[i].IsEntry {
				return true
			}
			if SortedChains[j].IsEntry {
				return false
			}
		}
		D1 := SortedChains[i].density()
		D2 := SortedChains[j].density()
		if D1 != D2 {
			return D1 > D2
		}
		return SortedChains[i].Id < SortedChains[j].Id
	})
	if E.f.pass.debug > 2 {
		fmt.Printf("After Sorting\n")
	}
	// Collect the basic blocks in the order specified by their chains
	Order := make([]*Block, 0, E.f.NumBlocks())
	for _, C := range SortedChains {
		if E.f.pass.debug > 2 {
			fmt.Printf("Chain:\n")
			for _, Bb := range (*C).CBlocks {
				fmt.Printf("b%d %d\n", Bb.BB.ID, Bb.ExecutionCount)
			}
		}
		for _, B := range C.CBlocks {
			Order = append(Order, B.BB)
		}
	}
	return Order
}

func (E *ExtTSP) run() []*Block {
	E.initialize()
	// Pass 1: Merge blocks with their fallthrough successors
	E.mergeFallthroughs()
	// Pass 2: Merge pairs of chains while improving the ExtTSP objective
	E.mergeChainPairs()
	// Pass 3: Merge cold blocks to reduce code size
	E.mergeColdChains()
	// Collect blocks from all chains
	return E.concatChains()
}

func layoutExttsp(f *Func) []*Block {
	extTSP := ExtTSP{
		f:         f,
		AllBlocks: make([]CBlock, 0),
		AllChains: make([]*Chain, 0),
		HotChains: make([]*Chain, 0),
		AllEdges:  make([]CEdge, 0)}
	return extTSP.run()
}

// Register allocation may use a different order which has constraints
// imposed by the linear-scan algorithm.
func layoutRegallocOrder(f *Func) []*Block {
	// remnant of an experiment; perhaps there will be another.
	return layoutOrder(f)
}

func layoutOrder(f *Func) []*Block {
	order := make([]*Block, 0, f.NumBlocks())
	scheduled := f.Cache.allocBoolSlice(f.NumBlocks())
	defer f.Cache.freeBoolSlice(scheduled)
	idToBlock := f.Cache.allocBlockSlice(f.NumBlocks())
	defer f.Cache.freeBlockSlice(idToBlock)
	indegree := f.Cache.allocIntSlice(f.NumBlocks())
	defer f.Cache.freeIntSlice(indegree)
	posdegree := f.newSparseSet(f.NumBlocks()) // blocks with positive remaining degree
	defer f.retSparseSet(posdegree)
	// blocks with zero remaining degree. Use slice to simulate a LIFO queue to implement
	// the depth-first topology sorting algorithm.
	var zerodegree []ID
	// LIFO queue. Track the successor blocks of the scheduled block so that when we
	// encounter loops, we choose to schedule the successor block of the most recently
	// scheduled block.
	var succs []ID
	exit := f.newSparseSet(f.NumBlocks()) // exit blocks
	defer f.retSparseSet(exit)

	// Populate idToBlock and find exit blocks.
	for _, b := range f.Blocks {
		idToBlock[b.ID] = b
		if b.Kind == BlockExit {
			exit.add(b.ID)
		}
	}

	// Expand exit to include blocks post-dominated by exit blocks.
	for {
		changed := false
		for _, id := range exit.contents() {
			b := idToBlock[id]
		NextPred:
			for _, pe := range b.Preds {
				p := pe.b
				if exit.contains(p.ID) {
					continue
				}
				for _, s := range p.Succs {
					if !exit.contains(s.b.ID) {
						continue NextPred
					}
				}
				// All Succs are in exit; add p.
				exit.add(p.ID)
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	// Initialize indegree of each block
	for _, b := range f.Blocks {
		if exit.contains(b.ID) {
			// exit blocks are always scheduled last
			continue
		}
		indegree[b.ID] = len(b.Preds)
		if len(b.Preds) == 0 {
			// Push an element to the tail of the queue.
			zerodegree = append(zerodegree, b.ID)
		} else {
			posdegree.add(b.ID)
		}
	}

	bid := f.Entry.ID
blockloop:
	for {
		// add block to schedule
		b := idToBlock[bid]
		order = append(order, b)
		scheduled[bid] = true
		if len(order) == len(f.Blocks) {
			break
		}

		// Here, the order of traversing the b.Succs affects the direction in which the topological
		// sort advances in depth. Take the following cfg as an example, regardless of other factors.
		//           b1
		//         0/ \1
		//        b2   b3
		// Traverse b.Succs in order, the right child node b3 will be scheduled immediately after
		// b1, traverse b.Succs in reverse order, the left child node b2 will be scheduled
		// immediately after b1. The test results show that reverse traversal performs a little
		// better.
		// Note: You need to consider both layout and register allocation when testing performance.
		for i := len(b.Succs) - 1; i >= 0; i-- {
			c := b.Succs[i].b
			indegree[c.ID]--
			if indegree[c.ID] == 0 {
				posdegree.remove(c.ID)
				zerodegree = append(zerodegree, c.ID)
			} else {
				succs = append(succs, c.ID)
			}
		}

		// Pick the next block to schedule
		// Pick among the successor blocks that have not been scheduled yet.

		// Use likely direction if we have it.
		var likely *Block
		switch b.Likely {
		case BranchLikely:
			likely = b.Succs[0].b
		case BranchUnlikely:
			likely = b.Succs[1].b
		}
		if likely != nil && !scheduled[likely.ID] {
			bid = likely.ID
			continue
		}

		// Use degree for now.
		bid = 0
		// TODO: improve this part
		// No successor of the previously scheduled block works.
		// Pick a zero-degree block if we can.
		for len(zerodegree) > 0 {
			// Pop an element from the tail of the queue.
			cid := zerodegree[len(zerodegree)-1]
			zerodegree = zerodegree[:len(zerodegree)-1]
			if !scheduled[cid] {
				bid = cid
				continue blockloop
			}
		}

		// Still nothing, pick the unscheduled successor block encountered most recently.
		for len(succs) > 0 {
			// Pop an element from the tail of the queue.
			cid := succs[len(succs)-1]
			succs = succs[:len(succs)-1]
			if !scheduled[cid] {
				bid = cid
				continue blockloop
			}
		}

		// Still nothing, pick any non-exit block.
		for posdegree.size() > 0 {
			cid := posdegree.pop()
			if !scheduled[cid] {
				bid = cid
				continue blockloop
			}
		}
		// Pick any exit block.
		// TODO: Order these to minimize jump distances?
		for {
			cid := exit.pop()
			if !scheduled[cid] {
				bid = cid
				continue blockloop
			}
		}
	}
	f.laidout = true
	return order
	//f.Blocks = order
}
