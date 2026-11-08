package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	kzstd "github.com/klauspost/compress/zstd"
	_ "github.com/mattn/go-sqlite3"
)

// openDiffTestDB creates a sqlite db with just the chunk table — enough for
// the checkpoint-diff read path.
func openDiffTestDB(t *testing.T) *sqlx.Tx {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "diff.clst")
	db, err := sqlx.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE chunk (hash TEXT PRIMARY KEY, data BLOB, size INTEGER); CREATE TABLE config (name TEXT PRIMARY KEY NOT NULL COLLATE NOCASE, value CLOB, mtime INTEGER NOT NULL, synced BOOLEAN DEFAULT 0 NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

// writeTestChunk stores data the same way StoreFileChunks does: zstd blob
// keyed by sha256 of the raw bytes.
func writeTestChunk(t *testing.T, tx *sqlx.Tx, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	enc, err := kzstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := enc.EncodeAll(data, nil)
	enc.Close()
	if _, err := tx.Exec("INSERT INTO chunk (hash, data, size) VALUES (?, ?, ?)", hash, compressed, len(compressed)); err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestReadChunksDetailedRoundTrip(t *testing.T) {
	tx := openDiffTestDB(t)
	h1 := writeTestChunk(t, tx, []byte("first chunk bytes"))
	h2 := writeTestChunk(t, tx, []byte("SECOND"))
	seq := strings.Join([]string{h1, h2}, ",")

	content, spans, err := ReadChunksDetailed(tx, seq)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "first chunk bytesSECOND" {
		t.Fatalf("bad content: %q", content)
	}
	if len(spans) != 2 || spans[0].Offset != 0 || spans[0].Len != 17 || spans[1].Offset != 17 || spans[1].Len != 6 {
		t.Fatalf("bad spans: %+v", spans)
	}
}

func TestReadChunksDetailedMissing(t *testing.T) {
	tx := openDiffTestDB(t)
	h1 := writeTestChunk(t, tx, []byte("present"))
	missing := strings.Repeat("ab", 32)
	seq := strings.Join([]string{h1, missing}, ",")

	_, _, err := ReadChunksDetailed(tx, seq)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing-chunks refusal, got %v", err)
	}
}

func TestSplitChunkSequence(t *testing.T) {
	if got := SplitChunkSequence(""); got != nil {
		t.Fatalf("empty seq should be nil, got %v", got)
	}
	if got := SplitChunkSequence("a,b,c"); len(got) != 3 {
		t.Fatalf("bad split: %v", got)
	}
}
