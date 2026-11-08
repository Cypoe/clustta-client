package diff

import (
	"math/rand"
	"reflect"
	"testing"
)

// applyRuns replays an edit script: equal and insert elements come from b,
// deletes are skipped. The result must equal b — this is the ground truth
// for the Myers implementation.
func applyRuns[T comparable](a, b []T, runs []Run) []T {
	out := make([]T, 0, len(b))
	for _, r := range runs {
		switch r.Op {
		case Equal:
			for i := 0; i < r.Len; i++ {
				if a[r.AStart+i] != b[r.BStart+i] {
					panic("equal run elements differ")
				}
			}
			out = append(out, b[r.BStart:r.BStart+r.Len]...)
		case Insert:
			out = append(out, b[r.BStart:r.BStart+r.Len]...)
		case Delete:
		}
	}
	return out
}

func TestSeqProperty(t *testing.T) {
	cases := [][2][]int{
		{{}, {}},
		{{1, 2, 3}, {1, 2, 3}},
		{{1, 2, 3}, {1, 9, 3}},
		{{1, 2, 3}, {9, 1, 2, 3, 9}},
		{{1, 2, 3}, {}},
		{{}, {1, 2, 3}},
		{{1, 2, 3, 4, 5}, {1, 5}},
		{{5, 4, 3, 2, 1}, {1, 2, 3, 4, 5}},
	}
	for _, c := range cases {
		runs, err := Seq(c[0], c[1])
		if err != nil {
			t.Fatalf("Seq(%v,%v): %v", c[0], c[1], err)
		}
		got := applyRuns(c[0], c[1], runs)
		if !reflect.DeepEqual(got, c[1]) && !(len(got) == 0 && len(c[1]) == 0) {
			t.Fatalf("Seq(%v,%v) replayed to %v", c[0], c[1], got)
		}
	}
}

func TestSeqPropertyRandom(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for trial := 0; trial < 200; trial++ {
		n := r.Intn(60)
		a := make([]int, n)
		for i := range a {
			a[i] = r.Intn(15)
		}
		// b is a with random edits
		b := make([]int, 0, n+10)
		for _, v := range a {
			switch r.Intn(10) {
			case 0:
			case 1:
				b = append(b, r.Intn(15), v)
			default:
				b = append(b, v)
			}
		}
		runs, err := Seq(a, b)
		if err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
		got := applyRuns(a, b, runs)
		if !reflect.DeepEqual(got, b) {
			t.Fatalf("trial %d: replayed %v want %v (a=%v)", trial, got, b, a)
		}
	}
}

func TestChunkRegionsSharedAnchors(t *testing.T) {
	a := []string{"c1", "c2", "c3", "c4", "c5"}
	b := []string{"c1", "c2", "n1", "c4", "c5"}
	regions, err := ChunkRegions(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 3 {
		t.Fatalf("expected 3 regions, got %v", regions)
	}
	if !regions[0].Shared || !regions[2].Shared {
		t.Fatalf("anchors not shared: %v", regions)
	}
	mid := regions[1]
	if mid.Shared || !reflect.DeepEqual(mid.A, []string{"c3"}) || !reflect.DeepEqual(mid.B, []string{"n1"}) {
		t.Fatalf("bad changed region: %+v", mid)
	}
}

func TestChunkRegionsInsertion(t *testing.T) {
	// CDC behaviour: an inserted byte run near the start produces one new
	// chunk, the rest of the file re-hashes to the same chunks.
	a := []string{"c1", "c2", "c3"}
	b := []string{"c1", "n1", "c2", "c3"}
	regions, err := ChunkRegions(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 3 || regions[1].Shared {
		t.Fatalf("expected shared+changed+shared, got %v", regions)
	}
	if len(regions[1].A) != 0 || !reflect.DeepEqual(regions[1].B, []string{"n1"}) {
		t.Fatalf("bad insertion region: %+v", regions[1])
	}
}

func TestLinesBasic(t *testing.T) {
	a := []byte("alpha\nbeta\ngamma\ndelta\n")
	b := []byte("alpha\nbeta\nGAMMA\ndelta\nepsilon\n")
	hunks, err := Lines(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %v", hunks)
	}
	var del, add int
	for _, e := range hunks[0].Entries {
		switch e.Kind {
		case "del":
			del++
			if e.Text != "gamma" {
				t.Fatalf("wrong del line: %q", e.Text)
			}
		case "add":
			add++
		}
	}
	if del != 1 || add != 2 {
		t.Fatalf("expected 1 del + 2 add, got %d/%d: %+v", del, add, hunks[0].Entries)
	}
}

func TestLinesIdentical(t *testing.T) {
	hunks, err := Lines([]byte("same\nfile\n"), []byte("same\nfile\n"))
	if err != nil {
		t.Fatal(err)
	}
	if hunks != nil {
		t.Fatalf("expected no hunks, got %v", hunks)
	}
}

func TestByteSpans(t *testing.T) {
	// file: c1 = "AAAA" (off 0,len 4), n1 = "XXYY" (off 4,len 4), c3 = "ZZZZ" (off 8)
	aBytes := []byte("AAAAccccZZZZ")
	bBytes := []byte("AAAAXXYYZZZZ")
	aSpans := []ChunkSpan{{"c1", 0, 4}, {"c2", 4, 4}, {"c3", 8, 4}}
	bSpans := []ChunkSpan{{"c1", 0, 4}, {"n1", 4, 4}, {"c3", 8, 4}}
	regions := []Region{
		{Shared: true, A: []string{"c1"}, B: []string{"c1"}},
		{Shared: false, A: []string{"c2"}, B: []string{"n1"}},
		{Shared: true, A: []string{"c3"}, B: []string{"c3"}},
	}
	spans := ByteSpans(regions, aSpans, bSpans, aBytes, bBytes)
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %v", spans)
	}
	s := spans[0]
	if s.AOff != 4 || s.ALen != 4 || s.BOff != 4 || s.BLen != 4 {
		t.Fatalf("bad span: %+v", s)
	}
}

func TestByteSpansTrimsSharedEdges(t *testing.T) {
	// changed chunks share edge bytes with each other; the span should
	// shrink to the truly differing interior.
	aBytes := []byte("QabcQ")
	bBytes := []byte("QxyzQ")
	aSpans := []ChunkSpan{{"c1", 0, 5}}
	bSpans := []ChunkSpan{{"n1", 0, 5}}
	regions := []Region{{Shared: false, A: []string{"c1"}, B: []string{"n1"}}}
	spans := ByteSpans(regions, aSpans, bSpans, aBytes, bBytes)
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %v", spans)
	}
	s := spans[0]
	if s.AOff != 1 || s.ALen != 3 || s.BOff != 1 || s.BLen != 3 {
		t.Fatalf("edges not trimmed: %+v", s)
	}
}

func TestIsText(t *testing.T) {
	if !IsText([]byte("plain ascii\nwith\ttabs\r\n")) {
		t.Fatal("text sniffed as binary")
	}
	if IsText([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00}) {
		t.Fatal("png header sniffed as text")
	}
	if !IsText(nil) {
		t.Fatal("empty content should be text")
	}
}

func TestLinesEOLInsensitive(t *testing.T) {
	a := []byte("alpha" + "\r\n" + "beta" + "\r\n" + "gamma" + "\r\n")
	b := []byte("alpha\nbeta\ngamma\n")
	hunks, err := Lines(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if hunks != nil {
		t.Fatalf("pure EOL flip should produce no hunks, got %+v", hunks)
	}
}

func TestLinesEOLPlusContent(t *testing.T) {
	a := []byte("alpha" + "\r\n" + "beta" + "\r\n")
	b := []byte("alpha\nBETA\n")
	hunks, err := Lines(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %+v", hunks)
	}
}

func TestDetectEOL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"a\nb\n", "lf"},
		{"a\r\nb\r\n", "crlf"},
		{"a\rb\r", "cr"},
		{"a\nb\r\n", "mixed"},
		{"no endings", "none"},
	}
	for _, c := range cases {
		if got := DetectEOL([]byte(c.in)); got != c.want {
			t.Fatalf("DetectEOL(%q) = %s want %s", c.in, got, c.want)
		}
	}
}
