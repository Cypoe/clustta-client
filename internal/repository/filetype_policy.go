package repository

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"clustta/internal/utils"

	"github.com/jmoiron/sqlx"
)

// Filetype policy: how a file is chunked and diffed, resolved from its
// extension. Chunks are the storage lane — "text" files get a smaller CDC
// window so frame-sequence/text exports that would otherwise fit in a
// single default-sized chunk still produce sub-file deltas.
//
// Projects override the standard set through the syncable `filetype_policy`
// config row: {"version":1,"extensions":{".abc":"text",".cube":"default"}}.
const (
	FiletypePolicyConfig  = "filetype_policy"
	FiletypePolicyVersion = 1
)

// ChunkLane is a named CDC parameter set.
type ChunkLane struct {
	MinSize     int `json:"min_size"`
	AverageSize int `json:"avg_size"`
	MaxSize     int `json:"max_size"`
	Text        bool `json:"text"`
}

const (
	LaneDefault = "default"
	LaneText    = "text"
)

var chunkLanes = map[string]ChunkLane{
	LaneDefault: {MinSize: 512 * 1024, AverageSize: 1024 * 1024, MaxSize: 8 * 1024 * 1024},
	LaneText:    {MinSize: 4 * 1024, AverageSize: 16 * 1024, MaxSize: 64 * 1024, Text: true},
}

// textExtensions is the standard set: files that read as text and benefit
// from the small-chunk lane — source, config, and the text serialization
// formats animation/DCC tools emit (USD ASCII, Maya ASCII, Nuke scripts,
// OTIO timelines, EDLs, OBJ/MTL, glTF JSON, subtitles).
var textExtensions = map[string]bool{
	// generic text + code
	".py": true, ".mel": true, ".lua": true, ".js": true, ".ts": true,
	".json": true, ".xml": true, ".yaml": true, ".yml": true, ".toml": true,
	".ini": true, ".cfg": true, ".conf": true, ".txt": true, ".md": true,
	".csv": true, ".tsv": true, ".sh": true, ".bat": true, ".ps1": true,
	".log": true, ".diff": true, ".patch": true,
	// studio text formats
	".ma": true, ".nk": true, ".gizmo": true, ".usda": true,
	".otio": true, ".edl": true, ".ale": true, ".mtl": true, ".obj": true,
	".gltf": true, ".svg": true, ".ocio": true, ".srt": true, ".vtt": true,
}

// FiletypePolicy is the project's extension→lane assignment, stored as one
// JSON config row. Extensions are lowercase, leading-dot.
type FiletypePolicy struct {
	Version    int               `json:"version"`
	Extensions map[string]string `json:"extensions"`
}

func GetFiletypePolicy(tx *sqlx.Tx) (FiletypePolicy, error) {
	policy := FiletypePolicy{Version: FiletypePolicyVersion, Extensions: map[string]string{}}
	var raw string
	if err := tx.Get(&raw, "SELECT value FROM config WHERE name = ?", FiletypePolicyConfig); err != nil {
		if err == sql.ErrNoRows {
			return policy, nil
		}
		return FiletypePolicy{}, err
	}
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return FiletypePolicy{}, fmt.Errorf("invalid filetype policy: %w", err)
	}
	return NormalizeFiletypePolicy(policy)
}

func SetFiletypePolicy(tx *sqlx.Tx, policy FiletypePolicy) error {
	normalized, err := NormalizeFiletypePolicy(policy)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`
		INSERT INTO config (name, value, mtime, synced)
		VALUES (?, ?, ?, 0)
		ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value, mtime = EXCLUDED.mtime, synced = 0
	`, FiletypePolicyConfig, string(raw), utils.GetEpochTime())
	return err
}

func NormalizeFiletypePolicy(policy FiletypePolicy) (FiletypePolicy, error) {
	policy.Version = FiletypePolicyVersion
	extensions := map[string]string{}
	for ext, lane := range policy.Extensions {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		if strings.ContainsAny(ext, `/\\`) {
			return FiletypePolicy{}, fmt.Errorf("invalid file extension %q", ext)
		}
		if _, ok := chunkLanes[lane]; !ok {
			return FiletypePolicy{}, fmt.Errorf("unknown chunk lane %q for %s", lane, ext)
		}
		extensions[ext] = lane
	}
	policy.Extensions = extensions
	return policy, nil
}

// LaneForExtension resolves the chunk lane: project policy first, then the
// standard text set, then default.
func LaneForExtension(policy FiletypePolicy, ext string) (name string, lane ChunkLane) {
	ext = strings.ToLower(ext)
	if laneName, ok := policy.Extensions[ext]; ok {
		return laneName, chunkLanes[laneName]
	}
	if textExtensions[ext] {
		return LaneText, chunkLanes[LaneText]
	}
	return LaneDefault, chunkLanes[LaneDefault]
}

// LaneForFile resolves the lane for a file path inside the project's
// configured policy.
func LaneForFile(tx *sqlx.Tx, filePath string) (string, ChunkLane, error) {
	policy, err := GetFiletypePolicy(tx)
	if err != nil {
		return "", ChunkLane{}, err
	}
	name, lane := LaneForExtension(policy, filepath.Ext(filePath))
	return name, lane, nil
}

// LaneNames lists the defined lanes, sorted — for settings surfaces.
func LaneNames() []string {
	names := make([]string, 0, len(chunkLanes))
	for name := range chunkLanes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
