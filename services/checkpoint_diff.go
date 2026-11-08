package services

import (
	"clustta/internal/diff"
	"clustta/internal/repository"
	"clustta/internal/repository/models"
	"clustta/internal/utils"
	"errors"
)

// maxDiffContentSize caps how much reconstructed content the fine-grained
// views may materialize. Beyond it the caller still gets the chunk-region
// delta — the storage layer computed that for free.
const maxDiffContentSize = 48 * 1024 * 1024

// DiffCheckpoints diffs two checkpoints of the same asset. The chunk
// sequences alone determine the storage delta (shared vs changed regions);
// reconstructed content refines it into line hunks for text or byte spans
// for binary. Missing chunks fail loudly — sync first.
func (c *CheckpointService) DiffCheckpoints(projectPath, checkpointIdA, checkpointIdB string) (models.CheckpointDiff, error) {
	result := models.CheckpointDiff{}
	if checkpointIdA == checkpointIdB {
		return result, errors.New("cannot diff a checkpoint against itself")
	}
	dbConn, err := utils.OpenDb(projectPath)
	if err != nil {
		return result, err
	}
	defer dbConn.Close()
	tx, err := dbConn.Beginx()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	cpA, err := repository.GetCheckpoint(tx, checkpointIdA)
	if err != nil {
		return result, err
	}
	cpB, err := repository.GetCheckpoint(tx, checkpointIdB)
	if err != nil {
		return result, err
	}
	if cpA.AssetId != cpB.AssetId {
		return result, repository.ErrDifferentAssets
	}
	result.AssetId = cpA.AssetId
	result.A = models.CheckpointDiffSide{CheckpointId: cpA.Id, CreatedAt: cpA.CreatedAt, Comment: cpA.Comment, FileSize: cpA.FileSize}
	result.B = models.CheckpointDiffSide{CheckpointId: cpB.Id, CreatedAt: cpB.CreatedAt, Comment: cpB.Comment, FileSize: cpB.FileSize}

	hashesA := repository.SplitChunkSequence(cpA.Chunks)
	hashesB := repository.SplitChunkSequence(cpB.Chunks)
	regions, err := diff.ChunkRegions(hashesA, hashesB)
	if err != nil {
		return result, err
	}
	result.Regions = regions
	result.ChunksTotal = len(hashesA) + len(hashesB)
	changed := false
	for _, r := range regions {
		if r.Shared {
			result.ChunksShared += len(r.A)
		} else {
			changed = true
		}
	}
	if !changed {
		result.Kind = "identical"
		content, _, err := repository.ReadChunksDetailed(tx, cpA.Chunks)
		if err != nil {
			return result, err
		}
		result.BytesShared = len(content)
		result.A.EOL = diff.DetectEOL(content)
		result.B.EOL = result.A.EOL
		return result, nil
	}

	contentA, spansA, err := repository.ReadChunksDetailed(tx, cpA.Chunks)
	if err != nil {
		return result, err
	}
	contentB, spansB, err := repository.ReadChunksDetailed(tx, cpB.Chunks)
	if err != nil {
		return result, err
	}
	for _, s := range spansA {
		if regionShared(regions, s.Hash) {
			result.BytesShared += s.Len
		}
	}
	result.A.EOL = diff.DetectEOL(contentA)
	result.B.EOL = diff.DetectEOL(contentB)

	if len(contentA)+len(contentB) > maxDiffContentSize {
		result.Kind = pickDiffKind(contentA, contentB)
		result.Truncated = true
		return result, nil
	}

	if diff.IsText(contentA) && diff.IsText(contentB) {
		result.Kind = "text"
		result.LinesA = diff.CountLines(contentA)
		result.LinesB = diff.CountLines(contentB)
		result.LineHunks, err = diff.Lines(contentA, contentB)
		if errors.Is(err, diff.ErrTooLarge) {
			result.LineHunks = nil
			result.Truncated = true
			return result, nil
		}
		if err != nil {
			return result, err
		}
	} else {
		result.Kind = "binary"
		result.ByteSpans = diff.ByteSpans(regions, spansA, spansB, contentA, contentB)
	}
	return result, nil
}

func regionShared(regions []diff.Region, hash string) bool {
	for _, r := range regions {
		if !r.Shared {
			continue
		}
		for _, h := range r.A {
			if h == hash {
				return true
			}
		}
	}
	return false
}

func pickDiffKind(contentA, contentB []byte) string {
	if diff.IsText(contentA) && diff.IsText(contentB) {
		return "text"
	}
	return "binary"
}
