// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "textflag.h"

// func Syscall6(a1, a2, a3, a4, a5, a6, num uintptr) (r1, r2, errno uintptr)
TEXT ·Syscall6<ABIInternal>(SB),NOSPLIT,$0-0
        MOVD    R0, R8
        MOVD    R1, R0
        MOVD    R2, R1
        MOVD    R3, R2
        MOVD    R4, R3
        MOVD    R5, R4
        MOVD    R6, R5
        SVC
        CMN     $4095, R0
        CSNEG   CC, ZR, R0, R2 // if CC then R2 = ZR else R2 = -R0
        CSINV   CC, R0, ZR, R0 // if CC then R0 = R0 else R0 = ZR
        CSET    CC, R1         // if CC then R1 = 1 else R1 = 0
        RET
