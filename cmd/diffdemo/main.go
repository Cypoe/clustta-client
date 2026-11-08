package main

// diffdemo builds a REAL .clst project through the app's own creation path
// (repository.CreateProjectWithStorageMode -> schema + initData + admin user +
// version stamp), then creates assets and checkpoints through the exported
// repository functions (AddCollection/AddAsset/CreateCheckpoint ->
// StoreFileChunks, so filetype chunk lanes actually apply) and runs
// CheckpointService.DiffCheckpoints over them.
//
// It ends by running the same gates the app runs on open --
// VerifyProjectIntegrity and RunMigrations -- so a demo db that the app would
// refuse fails here, at generation time.
//
// Output goes to stdout; the db is left in output/diffdemo/demo.clst and
// can be opened in the app to click through the diff view.
//
//	go run ./cmd/diffdemo

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	auth_service "clustta/internal/auth_service"
	"clustta/internal/repository"
	"clustta/internal/repository/migrations"
	"clustta/internal/utils"
	"clustta/services"
)

// lightrig renders ~350 KiB of one-json-object-per-line shot data -- the
// shape of a DCC tool dumping a scene to text.
func lightrig(frames, seed int, mutate func([]string) []string) []byte {
	var b strings.Builder
	b.WriteString("{\n  \"shot\": \"sh_010\",\n  \"frames\": [\n")
	for i := 0; i < frames; i++ {
		fmt.Fprintf(&b, "    {\"frame\": %d, \"camera\": [%.4f, %.4f, %.4f], \"light_a\": %.3f, \"light_b\": %.3f, \"key_intensity\": %.3f}%s\n",
			i,
			float64(i)*0.111, float64(i)*0.042, float64(seed)*1.0,
			float64(i)*0.007, float64(i)*0.013, float64(i)*0.029,
			map[bool]string{true: ",", false: ""}[i < frames-1])
	}
	b.WriteString("  ]\n}\n")
	lines := strings.Split(b.String(), "\n")
	if mutate != nil {
		lines = mutate(lines[:len(lines)-1])
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func diffAndPrint(svc *services.CheckpointService, dbPath, a, b, label string) {
	res, err := svc.DiffCheckpoints(dbPath, a, b)
	if err != nil {
		log.Fatalf("%s: %v", label, err)
	}
	pretty, _ := json.MarshalIndent(res, "", "  ")
	fmt.Printf("\n================ %s : %s -> %s ================\n%s\n", label, a, b, pretty)
}

func main() {
	dir, err := filepath.Abs(filepath.Join("output", "diffdemo"))
	if err != nil {
		log.Fatal(err)
	}
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}
	dbPath := filepath.Join(dir, "demo.clst")
	workDir := filepath.Join(dir, "work")

	// Create the project exactly like the app does. The creation path calls
	// AddKnownUser(fetchPhoto=true), which hits the auth API -- so this needs
	// the real logged-in user, not a fabricated one.
	user, err := auth_service.GetActiveUser()
	if err != nil {
		log.Fatalf("active user (log into the app account first): %v", err)
	}
	info, err := repository.CreateProjectWithStorageMode(dbPath, "Personal", workDir, "", "", "", user)
	if err != nil {
		log.Fatalf("create project: %v", err)
	}
	fmt.Println("demo project:", info.Uri)

	db, err := utils.OpenDb(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	var readyStatusID, genericTypeID, genericCollTypeID, userID string
	if err := db.Get(&readyStatusID, "SELECT id FROM status WHERE short_name='ready'"); err != nil {
		log.Fatal(err)
	}
	if err := db.Get(&genericTypeID, "SELECT id FROM asset_type WHERE name='generic'"); err != nil {
		log.Fatal(err)
	}
	if err := db.Get(&genericCollTypeID, "SELECT id FROM collection_type WHERE name='generic'"); err != nil {
		log.Fatal(err)
	}
	if err := db.Get(&userID, "SELECT id FROM "+strconv.Quote("user")+" LIMIT 1"); err != nil {
		log.Fatal(err)
	}

	tx, err := db.Beginx()
	if err != nil {
		log.Fatal(err)
	}
	collID := uuid.New().String()
	if err := repository.AddCollection(tx, collID, "demo", "", genericCollTypeID, "", "", false); err != nil {
		log.Fatal(err)
	}
	lightrigID := uuid.New().String()
	if err := repository.AddAsset(tx, lightrigID, time.Now().UTC().Format("2006-01-02 15:04:05"),
		"shot_010_lightrig", genericTypeID, collID, readyStatusID, "json", "", nil, "", false, userID, ""); err != nil {
		log.Fatal(err)
	}
	thumbID := uuid.New().String()
	if err := repository.AddAsset(tx, thumbID, time.Now().UTC().Format("2006-01-02 15:04:05"),
		"render_thumb", genericTypeID, collID, readyStatusID, "bin", "", nil, "", false, userID, ""); err != nil {
		log.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}

	checkpointIDs := map[string]string{}
	checkpointSeqs := map[string]string{}
	checkpoint := func(assetID, fileName, msg string, content []byte) {
		path := filepath.Join(workDir, fileName)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			log.Fatal(err)
		}
		tx, err := db.Beginx()
		if err != nil {
			log.Fatal(err)
		}
		cp, err := repository.CreateCheckpoint(tx, assetID, msg, "", "", 0, 0, path, userID, "", uuid.New().String(), func(int, int, string, string) {})
		if err != nil {
			log.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			log.Fatal(err)
		}
		checkpointIDs[msg] = cp.Id
		checkpointSeqs[msg] = cp.Chunks
		fmt.Printf("  %-36s %5d KiB -> %d chunks\n", msg, len(content)/1024, len(strings.Split(cp.Chunks, ",")))
	}

	// --- asset 1: text lane (.json) -------------------------------------
	v1 := lightrig(3000, 7, nil)
	v2 := lightrig(3000, 7, func(ls []string) []string {
		ls[1200] = "    {\"frame\": 1198, \"camera\": [9.9999, 0.5, 7.0], \"light_a\": 1.0, \"light_b\": 0.25, \"key_intensity\": 2.5},"
		ls[2100] = "    {\"frame\": 2098, \"camera\": [0.1, 0.1, 7.0], \"light_a\": 0.0, \"light_b\": 0.0, \"key_intensity\": 0.1},"
		out := append([]string{}, ls[:1800]...)
		out = append(out, "    {\"frame\": 1798, \"camera\": [0.0, 0.0, 7.0], \"light_a\": 0.5, \"light_b\": 0.5, \"key_intensity\": 1.0},")
		return append(out, ls[1800:]...)
	})
	// v3: byte-for-byte same content as v1 but CRLF line endings.
	v3 := []byte(strings.ReplaceAll(string(v1), "\n", "\r\n"))

	fmt.Println("\n[text lane] shot_010_lightrig.json")
	checkpoint(lightrigID, "shot_010_lightrig.json", "v1: lightrig 3000 frames", v1)
	checkpoint(lightrigID, "shot_010_lightrig.json", "v2: camera tweaks + one insert", v2)
	checkpoint(lightrigID, "shot_010_lightrig.json", "v3: same as v1 but CRLF endings", v3)

	// v1 restore point: same chunk sequence, separate checkpoint row.
	tx, err = db.Beginx()
	if err != nil {
		log.Fatal(err)
	}
	cpCopy, err := repository.CreateCheckpoint(tx, lightrigID, "v1 restore point",
		checkpointSeqs["v1: lightrig 3000 frames"], "same-as-v1", 0, len(v1),
		filepath.Join(workDir, "shot_010_lightrig.json"), userID, "", uuid.New().String(), func(int, int, string, string) {})
	if err != nil {
		log.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
	checkpointIDs["v1 restore point"] = cpCopy.Id
	checkpointSeqs["v1 restore point"] = cpCopy.Chunks

	// --- asset 2: binary lane (.bin) -------------------------------------
	rng := rand.New(rand.NewSource(42))
	binA := make([]byte, 40*1024)
	rng.Read(binA)
	copy(binA, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	binB := append([]byte(nil), binA...)
	copy(binB[20480:], []byte{0xDE, 0xAD, 0xBE, 0xEF})
	fmt.Println("\n[binary lane] render_thumb.bin")
	checkpoint(thumbID, "render_thumb.bin", "thumbnail v1", binA)
	checkpoint(thumbID, "render_thumb.bin", "thumbnail v2: 4 bytes changed", binB)

	// --- project override: .fxpreset -> text lane -------------------------
	fx := lightrig(1200, 3, nil)
	fxPath := filepath.Join(workDir, "look.fxpreset")
	os.WriteFile(fxPath, fx, 0o600)
	tx, _ = db.Beginx()
	seqF, _ := repository.StoreFileChunks(tx, fxPath, func(int, int, string, string) {})
	tx.Commit()
	fmt.Printf("\n[override] look.fxpreset %d KiB -> %d chunks (default lane)\n",
		len(fx)/1024, len(strings.Split(seqF, ",")))

	tx, _ = db.Beginx()
	if err := repository.SetFiletypePolicy(tx, repository.FiletypePolicy{
		Extensions: map[string]string{".fxpreset": "text"},
	}); err != nil {
		log.Fatal(err)
	}
	tx.Commit()

	tx, _ = db.Beginx()
	seqG, _ := repository.StoreFileChunks(tx, fxPath, func(int, int, string, string) {})
	tx.Commit()
	fmt.Printf("[override] look.fxpreset (policy .fxpreset->text) -> %d chunks (text lane)\n",
		len(strings.Split(seqG, ",")))

	// --- diffs ------------------------------------------------------------
	svc := &services.CheckpointService{}
	diffAndPrint(svc, dbPath, checkpointIDs["v1: lightrig 3000 frames"], checkpointIDs["v2: camera tweaks + one insert"], "TEXT edit")
	diffAndPrint(svc, dbPath, checkpointIDs["v1: lightrig 3000 frames"], checkpointIDs["v3: same as v1 but CRLF endings"], "EOL flip only")
	diffAndPrint(svc, dbPath, checkpointIDs["thumbnail v1"], checkpointIDs["thumbnail v2: 4 bytes changed"], "BINARY edit")
	diffAndPrint(svc, dbPath, checkpointIDs["v1: lightrig 3000 frames"], checkpointIDs["v1 restore point"], "IDENTICAL")

	// --- the same gates the app runs on open -------------------------------
	ok, err := repository.VerifyProjectIntegrity(dbPath)
	if err != nil || !ok {
		log.Fatalf("integrity check: ok=%v err=%v", ok, err)
	}
	check, err := utils.OpenDb(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer check.Close()
	var version string
	if err := check.Get(&version, "SELECT value FROM config WHERE name='version'"); err != nil {
		log.Fatal(err)
	}
	if err := migrations.RunMigrations(check, version, repository.ProjectSchema); err != nil {
		log.Fatalf("migration check: %v", err)
	}
	fmt.Printf("\napp-open gates: integrity ok, migrations ok (schema %s)\n", version)
	fmt.Println("db left at", dbPath)
	fmt.Println("open it in the app, open asset shot_010_lightrig, expand a checkpoint, hit the merge icon")
}
