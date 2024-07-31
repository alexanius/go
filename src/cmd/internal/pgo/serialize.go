// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pgo

import (
	"bufio"
	"fmt"
	"io"
	"sort"
)

// Serialization of a Profile allows go tool preprofile to construct the edge
// map only once (rather than once per compile process). The compiler processes
// then parse the pre-processed data directly from the serialized format.
//
// The format of the serialized output is as follows.
//
//      GO PREPROFILE V1
//      caller_name
//      callee_name
//      "call site offset" "call edge weight"
//      ...
//      caller_name
//      callee_name
//      "call site offset" "call edge weight"
//
// Entries are sorted by "call edge weight", from highest to lowest.

const serializationHeader = "GO PREPROFILE V1\n"

// WriteTo writes a serialized representation of Profile to w.
//
// FromSerialized can parse the format back to Profile.
//
// WriteTo implements io.WriterTo.Write.
func (d *Profile) WriteTo(w io.Writer) (int64, error) {
	bw := bufio.NewWriter(w)

	var written int64

	// Header
	n, err := bw.WriteString(serializationHeader)
	written += int64(n)
	if err != nil {
		return written, err
	}

	for _, edge := range d.NamedEdgeMap.ByWeight {
		weight := d.NamedEdgeMap.Weight[edge]

		n, err = fmt.Fprintln(bw, edge.CallerName)
		written += int64(n)
		if err != nil {
			return written, err
		}

		n, err = fmt.Fprintln(bw, edge.CalleeName)
		written += int64(n)
		if err != nil {
			return written, err
		}

		n, err = fmt.Fprintf(bw, "%d %d\n", edge.CallSiteOffset, weight)
		written += int64(n)
		if err != nil {
			return written, err
		}
	}

	if err := bw.Flush(); err != nil {
		return written, err
	}

	// No need to serialize TotalWeight, it can be trivially recomputed
	// during parsing.

	return written, nil
}

// WriteBbTo writes a serialized representation of basic block profile to w.
//
// FromSerializedBb can parse the format back to Profile.
//
// WriteBbTo implements io.WriterTo.Write.
func (d *Profile) WriteBbTo(w io.Writer) (int64, error) {
	bw := bufio.NewWriter(w)

	var written int64

	n, err := bw.WriteString(serializationHeader)
	written += int64(n)
	if err != nil {
		return written, err
	}

	if d.FunctionsCounters != nil {
		fnNames := make([]string, 0, len(*d.FunctionsCounters))
		for key := range *d.FunctionsCounters {
			fnNames = append(fnNames, key)
		}
		sort.Strings(fnNames)

		for _, fn := range fnNames {
			fcc := (*d.FunctionsCounters)[fn]
			n, err = fmt.Fprintf(bw, "func: %s\n", fn)
			written += int64(n)

			lines := make([]int64, 0, len(fcc))
			for line := range fcc {
				lines = append(lines, line)
			}
			sort.Slice(lines, func(i, j int) bool {
				return lines[i] < lines[j]
			})

			for _, line := range lines {
				counter := fcc[line]
				n, err = fmt.Fprintf(bw, "%d %d\n", line, counter)
				written += int64(n)
			}
		}
	}

	if err := bw.Flush(); err != nil {
		return written, err
	}

	return written, nil
}
