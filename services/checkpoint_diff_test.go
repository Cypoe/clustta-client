package services

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

// openDiffProject creates a minimal project db — the three tables the
// checkpoint-diff path touches.
func openDiffProject(t *testing.T) (string, *sqlx.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "proj.clst")
	db, err := sqlx.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`
		CREATE TABLE chunk (hash TEXT PRIMARY KEY, data BLOB, size INTEGER);
		CREATE TABLE preview (hash TEXT PRIMARY KEY, extension TEXT, preview BLOB);
		CREATE TABLE asset_checkpoint (
			id TEXT PRIMARY KEY,
			created_at DATETIME NOT NULL,
			mtime INTEGER NOT NULL,
			asset_id TEXT NOT NULL,
			xxhash_checksum TEXT NOT NULL,
			time_modified INTEGER NOT NULL,
			file_size INTEGER NOT NULL,
			chunks TEXT NOT NULL,
			source_checkpoint_id TEXT NULL,
			comment TEXT DEFAULT '' NOT NULL,
			author_id TEXT NOT NULL,
			group_id TEXT DEFAULT '' NOT NULL,
			preview_id TEXT DEFAULT '' NOT NULL,
			trashed BOOLEAN DEFAULT 0 NOT NULL,
			synced BOOLEAN DEFAULT 0 NOT NULL
		);
	`)
	if err != nil {
		t.Fatal(err)
	}
	return dbPath, db
}

func storeDiffChunk(t *testing.T, db *sqlx.DB, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	enc, err := kzstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := enc.EncodeAll(data, nil)
	enc.Close()
	if _, err := db.Exec("INSERT INTO chunk (hash, data, size) VALUES (?, ?, ?)", hash, compressed, len(compressed)); err != nil {
		t.Fatal(err)
	}
	return hash
}

func storeDiffCheckpoint(t *testing.T, db *sqlx.DB, id, assetId, chunks string, fileSize int) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO asset_checkpoint
		(id, created_at, mtime, asset_id, xxhash_checksum, time_modified, file_size, chunks, author_id)
		VALUES (?, '2026-11-08 00:00:00', 0, ?, 'xx', 0, ?, ?, 'tester')`, id, assetId, fileSize, chunks)
	if err != nil {
		t.Fatal(err)
	}
}

func TestDiffCheckpointsText(t *testing.T) {
	dbPath, db := openDiffProject(t)
	// three logical chunks per version; the middle one differs — this is
	// what CDC produces when an edit lands inside one region of a file.
	head := "line 1\nline 2\n"
	aMid := "gamma = 1.0\n"
	bMid := "gamma = 2.5\n"
	tail := "line 4\nline 5\n"
	hHead := storeDiffChunk(t, db, []byte(head))
	hAMid := storeDiffChunk(t, db, []byte(aMid))
	hBMid := storeDiffChunk(t, db, []byte(bMid))
	hTail := storeDiffChunk(t, db, []byte(tail))
	seqA := strings.Join([]string{hHead, hAMid, hTail}, ",")
	seqB := strings.Join([]string{hHead, hBMid, hTail}, ",")
	storeDiffCheckpoint(t, db, "cpA", "asset-1", seqA, len(head)+len(aMid)+len(tail))
	storeDiffCheckpoint(t, db, "cpB", "asset-1", seqB, len(head)+len(bMid)+len(tail))

	svc := &CheckpointService{}
	res, err := svc.DiffCheckpoints(dbPath, "cpA", "cpB")
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != "text" {
		t.Fatalf("expected text diff, got %q", res.Kind)
	}
	if res.ChunksShared != 2 {
		t.Fatalf("expected 2 shared chunks, got %d", res.ChunksShared)
	}
	if len(res.Regions) != 3 || res.Regions[1].Shared {
		t.Fatalf("expected shared+changed+shared regions, got %+v", res.Regions)
	}
	if len(res.LineHunks) != 1 {
		t.Fatalf("expected 1 line hunk, got %+v", res.LineHunks)
	}
	var sawDel, sawAdd bool
	for _, e := range res.LineHunks[0].Entries {
		if e.Kind == "del" && e.Text == "gamma = 1.0" {
			sawDel = true
		}
		if e.Kind == "add" && e.Text == "gamma = 2.5" {
			sawAdd = true
		}
	}
	if !sawDel || !sawAdd {
		t.Fatalf("hunk missing expected lines: %+v", res.LineHunks[0].Entries)
	}
}

func TestDiffCheckpointsBinary(t *testing.T) {
	dbPath, db := openDiffProject(t)
	hA1 := storeDiffChunk(t, db, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01})
	hB1 := storeDiffChunk(t, db, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0xFF, 0x02})
	hTail := storeDiffChunk(t, db, []byte{0xDE, 0xAD, 0xBE, 0xEF})
	seqA := strings.Join([]string{hA1, hTail}, ",")
	seqB := strings.Join([]string{hB1, hTail}, ",")
	storeDiffCheckpoint(t, db, "cpA", "asset-2", seqA, 14)
	storeDiffCheckpoint(t, db, "cpB", "asset-2", seqB, 14)

	svc := &CheckpointService{}
	res, err := svc.DiffCheckpoints(dbPath, "cpA", "cpB")
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != "binary" {
		t.Fatalf("expected binary diff, got %q", res.Kind)
	}
	if len(res.ByteSpans) != 1 {
		t.Fatalf("expected 1 byte span, got %+v", res.ByteSpans)
	}
	span := res.ByteSpans[0]
	// the first 8 bytes are identical; the changed span trims to the tail
	if span.AOff != 8 || span.ALen != 2 || span.BOff != 8 || span.BLen != 2 {
		t.Fatalf("bad byte span: %+v", span)
	}
	if res.LineHunks != nil {
		t.Fatal("binary diff must not emit line hunks")
	}
}

func TestDiffCheckpointsIdentical(t *testing.T) {
	dbPath, db := openDiffProject(t)
	h := storeDiffChunk(t, db, []byte("identical content"))
	storeDiffCheckpoint(t, db, "cpA", "asset-3", h, 17)
	storeDiffCheckpoint(t, db, "cpB", "asset-3", h, 17)

	svc := &CheckpointService{}
	res, err := svc.DiffCheckpoints(dbPath, "cpA", "cpB")
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != "identical" {
		t.Fatalf("expected identical, got %q", res.Kind)
	}
	if res.BytesShared != 17 {
		t.Fatalf("expected 17 bytes shared, got %d", res.BytesShared)
	}
	if res.A.EOL != "none" || res.B.EOL != "none" {
		t.Fatalf("expected eol=none on both sides, got %q/%q", res.A.EOL, res.B.EOL)
	}
}

func TestDiffCheckpointsMissingChunks(t *testing.T) {
	dbPath, db := openDiffProject(t)
	h := storeDiffChunk(t, db, []byte("there"))
	missing := strings.Repeat("cd", 32)
	storeDiffCheckpoint(t, db, "cpA", "asset-4", h, 5)
	storeDiffCheckpoint(t, db, "cpB", "asset-4", strings.Join([]string{h, missing}, ","), 5)

	svc := &CheckpointService{}
	_, err := svc.DiffCheckpoints(dbPath, "cpA", "cpB")
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing-chunks refusal, got %v", err)
	}
}
