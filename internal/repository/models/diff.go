package models

import "clustta/internal/diff"

// CheckpointDiffSide describes one endpoint of a checkpoint diff.
type CheckpointDiffSide struct {
	CheckpointId string `json:"checkpoint_id"`
	CreatedAt    string `json:"created_at"`
	Comment      string `json:"comment"`
	FileSize     int    `json:"file_size"`
	EOL          string `json:"eol"`
}

// CheckpointDiff is the result of diffing two checkpoints of one asset.
// Kind picks the render view: "identical", "text" (line hunks), or
// "binary" (byte spans). ChunkRegions is always populated for non-identical
// diffs — it is the storage delta the chunk store already knows.
type CheckpointDiff struct {
	Kind         string             `json:"kind"`
	AssetId      string             `json:"asset_id"`
	A            CheckpointDiffSide `json:"a"`
	B            CheckpointDiffSide `json:"b"`
	ChunksTotal  int                `json:"chunks_total"`
	ChunksShared int                `json:"chunks_shared"`
	BytesShared  int                `json:"bytes_shared"`
	Regions      []diff.Region      `json:"regions"`
	LineHunks    []diff.LineHunk    `json:"line_hunks,omitempty"`
	LinesA       int                `json:"lines_a,omitempty"`
	LinesB       int                `json:"lines_b,omitempty"`
	ByteSpans    []diff.ByteSpan    `json:"byte_spans,omitempty"`
	Truncated    bool               `json:"truncated"`
}
