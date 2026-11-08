package repository

import (
	"clustta/internal/diff"
	"errors"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
	kzstd "github.com/klauspost/compress/zstd"
)

// ReadChunksDetailed reconstructs a checkpoint's content from its
// comma-separated chunk sequence and returns the concatenated bytes plus
// the byte span each chunk covers. Missing chunks fail loudly — the same
// contract RestoreFileFromChunks keeps.
func ReadChunksDetailed(tx *sqlx.Tx, chunks string) ([]byte, []diff.ChunkSpan, error) {
	chunkHashes := strings.Split(chunks, ",")
	if len(chunkHashes) == 1 && chunkHashes[0] == "" {
		return nil, nil, nil
	}
	missing, err := CheckMissingChunks(tx, chunkHashes)
	if err != nil {
		return nil, nil, err
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("missing %d chunks; sync before diffing", len(missing))
	}
	decoder, err := kzstd.NewReader(nil)
	if err != nil {
		return nil, nil, err
	}
	defer decoder.Close()
	content := make([]byte, 0, len(chunkHashes)*1024*1024)
	spans := make([]diff.ChunkSpan, 0, len(chunkHashes))
	for _, hash := range chunkHashes {
		var compressed []byte
		if err := tx.Get(&compressed, "SELECT data FROM chunk WHERE hash = ?", hash); err != nil {
			return nil, nil, err
		}
		data, err := decoder.DecodeAll(compressed, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("corrupt chunk %s: %w", hash[:8], err)
		}
		spans = append(spans, diff.ChunkSpan{Hash: hash, Offset: len(content), Len: len(data)})
		content = append(content, data...)
	}
	return content, spans, nil
}

// SplitChunkSequence parses a checkpoint's chunk column into hashes.
func SplitChunkSequence(chunks string) []string {
	if chunks == "" {
		return nil
	}
	return strings.Split(chunks, ",")
}

var ErrDifferentAssets = errors.New("checkpoints belong to different assets")
