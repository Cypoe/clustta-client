package repository

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaneForExtensionResolution(t *testing.T) {
	policy := FiletypePolicy{Version: FiletypePolicyVersion, Extensions: map[string]string{".json": "default"}}

	name, lane := LaneForExtension(policy, ".json")
	if name != LaneDefault || lane.Text {
		t.Fatalf("project override should win: got %s", name)
	}
	name, lane = LaneForExtension(policy, ".usda")
	if name != LaneText || !lane.Text {
		t.Fatalf("standard text set should hit text lane: got %s", name)
	}
	name, lane = LaneForExtension(policy, ".blend")
	if name != LaneDefault || lane.Text {
		t.Fatalf("unknown/binary ext should stay default: got %s", name)
	}
}

func TestNormalizeFiletypePolicy(t *testing.T) {
	p, err := NormalizeFiletypePolicy(FiletypePolicy{Extensions: map[string]string{
		"ABC":   "text",
		".cube": "default",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Extensions[".abc"] != "text" || p.Extensions[".cube"] != "default" {
		t.Fatalf("bad normalization: %+v", p.Extensions)
	}
	if _, err := NormalizeFiletypePolicy(FiletypePolicy{Extensions: map[string]string{".x": "huge"}}); err == nil {
		t.Fatal("unknown lane should refuse")
	}
}

func TestFiletypePolicyConfigRoundTrip(t *testing.T) {
	tx := openDiffTestDB(t)
	if err := SetFiletypePolicy(tx, FiletypePolicy{Extensions: map[string]string{".exr": "text"}}); err != nil {
		t.Fatal(err)
	}
	policy, err := GetFiletypePolicy(tx)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Extensions[".exr"] != "text" {
		t.Fatalf("policy not persisted: %+v", policy)
	}
	name, _ := LaneForExtension(policy, ".exr")
	if name != LaneText {
		t.Fatalf("override should resolve to text lane, got %s", name)
	}
}

func TestStoreFileChunksLaneSelection(t *testing.T) {
	tx := openDiffTestDB(t)
	dir := t.TempDir()

	// a 200 KiB file: one chunk in the default lane (512 KiB min), many in
	// the text lane (64 KiB max). Pseudo-random content so CDC boundaries
	// actually fall inside the file.
	content := make([]byte, 200*1024)
	state := uint32(42)
	for i := range content {
		state = state*1664525 + 1013904223
		content[i] = byte(state >> 24)
	}

	binPath := filepath.Join(dir, "asset.bin")
	if err := os.WriteFile(binPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	seq, err := StoreFileChunks(tx, binPath, func(int, int, string, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Split(seq, ",")); n != 1 {
		t.Fatalf("binary lane should yield 1 chunk, got %d", n)
	}

	jsonPath := filepath.Join(dir, "asset.json")
	if err := os.WriteFile(jsonPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	seq, err = StoreFileChunks(tx, jsonPath, func(int, int, string, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Split(seq, ",")); n < 2 {
		t.Fatalf("text lane should yield multiple chunks for a 200 KiB file, got %d", n)
	}
}
