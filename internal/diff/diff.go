// Package diff computes deltas between two versions of a checkpoint's
// content. One algorithm (Myers O(ND) over a generic sequence) backs every
// view; the alphabet — chunk hashes, text lines, or bytes — is a parameter.
//
// The storage layer already computed the coarse delta: two checkpoints are
// sequences of content-addressed chunk hashes, so the shared chunks are the
// anchors of any finer diff for free. ChunkRegions surfaces that alignment;
// Lines and ByteSpans refine the changed regions for human review.
package diff

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// maxElements bounds the Myers work. Two versions of the same asset normally
// differ far less than this; beyond it callers should fall back to the
// chunk-region view, which is unbounded.
const maxElements = 200_000

var ErrTooLarge = errors.New("content too large to diff")

type Op int

const (
	Equal Op = iota
	Delete
	Insert
)

// Run is one run of identical ops over the two sequences.
//   - Equal:  a[AStart:AStart+Len] == b[BStart:BStart+Len]
//   - Delete: a[AStart:AStart+Len] absent from b
//   - Insert: b[BStart:BStart+Len] absent from a
type Run struct {
	Op                  Op
	AStart, BStart, Len int
}

// Seq diffs two sequences of comparable elements and returns the edit
// script as a list of runs in order. Elements may be chunk hashes, text
// lines, or bytes — the algorithm does not care.
func Seq[T comparable](a, b []T) ([]Run, error) {
	if len(a)+len(b) > maxElements {
		return nil, fmt.Errorf("%w (%d elements)", ErrTooLarge, len(a)+len(b))
	}
	coreA, coreB, pre, suf := stripCommon(a, b)
	runs := make([]Run, 0, 8)
	if pre > 0 {
		runs = append(runs, Run{Op: Equal, AStart: 0, BStart: 0, Len: pre})
	}
	core, err := myers(coreA, coreB)
	if err != nil {
		return nil, err
	}
	for _, r := range core {
		r.AStart += pre
		r.BStart += pre
		runs = append(runs, r)
	}
	if suf > 0 {
		runs = append(runs, Run{Op: Equal, AStart: pre + len(coreA), BStart: pre + len(coreB), Len: suf})
	}
	return runs, nil
}

// stripCommon removes the common prefix and suffix and returns the cores
// plus the prefix and suffix lengths (identical for a and b).
func stripCommon[T comparable](a, b []T) (coreA, coreB []T, pre, suf int) {
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	return a[pre : len(a)-suf], b[pre : len(b)-suf], pre, suf
}

// myers is the classic O(ND) algorithm with a saved V-array trace, run on
// the prefix/suffix-stripped cores.
func myers[T comparable](a, b []T) ([]Run, error) {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil, nil
	}
	max := n + m
	off := max + 1 // index offset so diagonal k maps to v[k+off]
	v := make([]int, 2*max+3)
	var trace [][]int
	found := -1
outer:
	for d := 0; d <= max; d++ {
		vc := make([]int, len(v))
		copy(vc, v)
		trace = append(trace, vc)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[k-1+off] < v[k+1+off]) {
				x = v[k+1+off]
			} else {
				x = v[k-1+off] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[k+off] = x
			if x >= n && y >= m {
				found = d
				break outer
			}
		}
	}
	if found < 0 {
		return nil, errors.New("diff: no edit path found")
	}
	// Backtrack through the saved frontiers to recover the edit script.
	type elemOp struct {
		op     Op
		aIndex int // index into a for Equal/Delete, -1 for Insert
		bIndex int // index into b for Equal/Insert, -1 for Delete
	}
	ops := make([]elemOp, 0, n+m)
	x, y := n, m
	for d := found; d > 0; d-- {
		vprev := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && vprev[k-1+off] < vprev[k+1+off]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := vprev[prevK+off]
		prevY := prevX - prevK
		// snake: equal elements walked diagonally inside level d
		for x > prevX && y > prevY {
			ops = append(ops, elemOp{Equal, x - 1, y - 1})
			x--
			y--
		}
		if x == prevX {
			ops = append(ops, elemOp{Insert, -1, y - 1})
			y--
		} else {
			ops = append(ops, elemOp{Delete, x - 1, -1})
			x--
		}
	}
	for x > 0 && y > 0 {
		ops = append(ops, elemOp{Equal, x - 1, y - 1})
		x--
		y--
	}
	for x > 0 {
		ops = append(ops, elemOp{Delete, x - 1, -1})
		x--
	}
	for y > 0 {
		ops = append(ops, elemOp{Insert, -1, y - 1})
		y--
	}
	// ops is reversed; condense into runs
	runs := make([]Run, 0, len(ops)/4+2)
	for i := len(ops) - 1; i >= 0; i-- {
		o := ops[i]
		if len(runs) > 0 {
			last := &runs[len(runs)-1]
			advancing := (o.op == Insert && o.bIndex == last.BStart+last.Len) ||
				(o.op != Insert && o.aIndex == last.AStart+last.Len)
			if last.Op == o.op && advancing {
				last.Len++
				continue
			}
		}
		runs = append(runs, Run{Op: o.op, Len: 1})
	}
	// Positions are recomputed in a single forward pass: each run starts
	// where the previous one left off on both sequences.
	posA, posB := 0, 0
	for i := range runs {
		r := &runs[i]
		switch r.Op {
		case Equal:
			r.AStart, r.BStart = posA, posB
			posA += r.Len
			posB += r.Len
		case Delete:
			r.AStart, r.BStart = posA, posB
			posA += r.Len
		case Insert:
			r.AStart, r.BStart = posA, posB
			posB += r.Len
		}
	}
	return runs, nil
}

// Region is one span of the chunk-sequence diff. Shared regions are anchors
// present in both versions; changed regions pair the removed chunk hashes
// (A) with the added ones (B).
type Region struct {
	Shared bool     `json:"shared"`
	A      []string `json:"a"`
	B      []string `json:"b"`
}

// ChunkRegions diffs two chunk-hash sequences into alternating
// shared/changed regions. This is the storage delta: shared regions cost
// nothing and need no reconstruction.
func ChunkRegions(a, b []string) ([]Region, error) {
	runs, err := Seq(a, b)
	if err != nil {
		return nil, err
	}
	regions := make([]Region, 0, len(runs)/2+1)
	i := 0
	for i < len(runs) {
		r := runs[i]
		if r.Op == Equal {
			regions = append(regions, Region{Shared: true, A: copySlice(a[r.AStart : r.AStart+r.Len]), B: copySlice(b[r.BStart : r.BStart+r.Len])})
			i++
			continue
		}
		reg := Region{Shared: false}
		for i < len(runs) && runs[i].Op != Equal {
			switch runs[i].Op {
			case Delete:
				reg.A = append(reg.A, a[runs[i].AStart:runs[i].AStart+runs[i].Len]...)
			case Insert:
				reg.B = append(reg.B, b[runs[i].BStart:runs[i].BStart+runs[i].Len]...)
			}
			i++
		}
		regions = append(regions, reg)
	}
	return regions, nil
}

func copySlice(s []string) []string {
	out := make([]string, len(s))
	copy(out, s)
	return out
}

// ---- text view ----

// LineEntry is one line of a line-diff hunk. ALine/BLine are 1-based line
// numbers in the two versions; -1 means the line does not exist there.
type LineEntry struct {
	Kind  string `json:"kind"` // "context" | "add" | "del"
	Text  string `json:"text"`
	ALine int    `json:"a_line"`
	BLine int    `json:"b_line"`
}

// LineHunk is a group of changed lines plus surrounding context.
type LineHunk struct {
	AStart  int         `json:"a_start"` // 1-based first line of the hunk in a
	BStart  int         `json:"b_start"`
	Entries []LineEntry `json:"entries"`
}

const lineContext = 3

type lineEnt struct {
	op    Op
	aLine int
	bLine int
	text  string
}

// Lines computes a unified-diff-style view over the two contents split into
// lines. Returns nil hunks when contents are line-identical.
func Lines(a, b []byte) ([]LineHunk, error) {
	la, lb := splitLines(NormalizeEOL(a)), splitLines(NormalizeEOL(b))
	runs, err := Seq(la, lb)
	if err != nil {
		return nil, err
	}
	ents := make([]lineEnt, 0, len(la)+len(lb))
	for _, r := range runs {
		switch r.Op {
		case Equal:
			for i := 0; i < r.Len; i++ {
				ents = append(ents, lineEnt{Equal, r.AStart + i + 1, r.BStart + i + 1, la[r.AStart+i]})
			}
		case Delete:
			for i := 0; i < r.Len; i++ {
				ents = append(ents, lineEnt{Delete, r.AStart + i + 1, -1, la[r.AStart+i]})
			}
		case Insert:
			for i := 0; i < r.Len; i++ {
				ents = append(ents, lineEnt{Insert, -1, r.BStart + i + 1, lb[r.BStart+i]})
			}
		}
	}
	changed := make([]int, 0, 16)
	for i, e := range ents {
		if e.op != Equal {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return nil, nil
	}
	hunks := []LineHunk{}
	start, end := changed[0], changed[0]
	for _, c := range changed[1:] {
		if c-end <= 2*lineContext {
			end = c
			continue
		}
		hunks = append(hunks, makeHunk(ents, start, end))
		start, end = c, c
	}
	hunks = append(hunks, makeHunk(ents, start, end))
	return hunks, nil
}

func makeHunk(ents []lineEnt, firstChange, lastChange int) LineHunk {
	from := firstChange - lineContext
	if from < 0 {
		from = 0
	}
	to := lastChange + lineContext + 1
	if to > len(ents) {
		to = len(ents)
	}
	h := LineHunk{Entries: make([]LineEntry, 0, to-from)}
	for _, e := range ents[from:to] {
		le := LineEntry{Text: e.text, ALine: e.aLine, BLine: e.bLine}
		switch e.op {
		case Equal:
			le.Kind = "context"
			if h.AStart == 0 {
				h.AStart = e.aLine
			}
			if h.BStart == 0 {
				h.BStart = e.bLine
			}
		case Delete:
			le.Kind = "del"
			if h.AStart == 0 {
				h.AStart = e.aLine
			}
			if h.BStart == 0 {
				h.BStart = e.bLine + 1
			}
		case Insert:
			le.Kind = "add"
			if h.AStart == 0 {
				h.AStart = e.aLine + 1
			}
			if h.BStart == 0 {
				h.BStart = e.bLine
			}
		}
		h.Entries = append(h.Entries, le)
	}
	if h.AStart == 0 {
		h.AStart = 1
	}
	if h.BStart == 0 {
		h.BStart = 1
	}
	return h
}

// NormalizeEOL converts CRLF and lone CR line endings to LF. The diff view
// normalizes so a pure line-ending flip produces no hunks — storage keeps
// the original bytes untouched.
func NormalizeEOL(content []byte) []byte {
	out := bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(out, []byte("\r"), []byte("\n"))
}

// DetectEOL reports the dominant line ending of the content:
// "lf" | "crlf" | "cr" | "mixed" | "none" (no line endings found).
func DetectEOL(content []byte) string {
	crlf, cr, lf := 0, 0, 0
	for i := 0; i < len(content); i++ {
		switch content[i] {
		case '\r':
			if i+1 < len(content) && content[i+1] == '\n' {
				crlf++
				i++
			} else {
				cr++
			}
		case '\n':
			lf++
		}
	}
	total := crlf + cr + lf
	if total == 0 {
		return "none"
	}
	switch {
	case lf == total:
		return "lf"
	case crlf == total:
		return "crlf"
	case cr == total:
		return "cr"
	default:
		return "mixed"
	}
}

// CountLines reports the line count of content after EOL normalization.
func CountLines(content []byte) int {
	return len(splitLines(NormalizeEOL(content)))
}

// splitLines splits on '\n'; a trailing newline does not produce a phantom
// empty line. Line terminators are stripped for comparison.
func splitLines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	lines := strings.Split(string(content), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// ---- binary view ----

// ChunkSpan maps one chunk to its byte range inside the reconstructed file.
type ChunkSpan struct {
	Hash   string `json:"hash"`
	Offset int    `json:"offset"`
	Len    int    `json:"len"`
}

// ByteSpan is one changed byte range: a[AOff:AOff+ALen] became
// b[BOff:BOff+BLen]. Hex holds a short preview of each side.
type ByteSpan struct {
	AOff int    `json:"a_off"`
	ALen int    `json:"a_len"`
	BOff int    `json:"b_off"`
	BLen int    `json:"b_len"`
	AHex string `json:"a_hex"`
	BHex string `json:"b_hex"`
}

const bytePreview = 64

// ByteSpans refines changed chunk regions into byte ranges, using the
// per-chunk spans of each version. Shared regions carry no entries — they
// are identical by hash. Previews are capped at bytePreview bytes.
func ByteSpans(regions []Region, aSpans, bSpans []ChunkSpan, aBytes, bBytes []byte) []ByteSpan {
	aOff := map[string]ChunkSpan{}
	for _, s := range aSpans {
		aOff[s.Hash] = s
	}
	bOff := map[string]ChunkSpan{}
	for _, s := range bSpans {
		bOff[s.Hash] = s
	}
	out := []ByteSpan{}
	for _, r := range regions {
		if r.Shared {
			continue
		}
		span := ByteSpan{}
		if len(r.A) > 0 {
			first, last := aOff[r.A[0]], aOff[r.A[len(r.A)-1]]
			span.AOff = first.Offset
			span.ALen = last.Offset + last.Len - first.Offset
		}
		if len(r.B) > 0 {
			first, last := bOff[r.B[0]], bOff[r.B[len(r.B)-1]]
			span.BOff = first.Offset
			span.BLen = last.Offset + last.Len - first.Offset
		}
		// trim common prefix/suffix inside the changed span
		aSeg := safeSlice(aBytes, span.AOff, span.ALen)
		bSeg := safeSlice(bBytes, span.BOff, span.BLen)
		pre := 0
		for pre < len(aSeg) && pre < len(bSeg) && aSeg[pre] == bSeg[pre] {
			pre++
		}
		suf := 0
		for suf < len(aSeg)-pre && suf < len(bSeg)-pre && aSeg[len(aSeg)-1-suf] == bSeg[len(bSeg)-1-suf] {
			suf++
		}
		span.AOff += pre
		span.ALen -= pre + suf
		span.BOff += pre
		span.BLen -= pre + suf
		span.AHex = hexPreview(safeSlice(aBytes, span.AOff, span.ALen))
		span.BHex = hexPreview(safeSlice(bBytes, span.BOff, span.BLen))
		out = append(out, span)
	}
	return out
}

func safeSlice(b []byte, off, length int) []byte {
	if off < 0 {
		off = 0
	}
	if off > len(b) {
		off = len(b)
	}
	end := off + length
	if end > len(b) {
		end = len(b)
	}
	return b[off:end]
}

func hexPreview(b []byte) string {
	truncated := len(b) > bytePreview
	if truncated {
		b = b[:bytePreview]
	}
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*3+4)
	for i, c := range b {
		if i > 0 {
			out = append(out, ' ')
		}
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	if truncated {
		out = append(out, ' ', '.', '.', '.')
	}
	return string(out)
}

// IsText sniffs content: a NUL byte in the first 8 KiB, or more than 30%
// non-printable non-whitespace bytes, marks it binary. This only picks the
// render alphabet — both views run on the same sequence machinery.
func IsText(content []byte) bool {
	sample := content
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	if len(sample) == 0 {
		return true
	}
	bad := 0
	for _, c := range sample {
		if c == 0 {
			return false
		}
		if c < 0x20 && c != '\n' && c != '\r' && c != '\t' && c != '\f' && c != '\v' {
			bad++
		}
	}
	return float64(bad)/float64(len(sample)) <= 0.30
}
