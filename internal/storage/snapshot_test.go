package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/storage/sqlc/datadb"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage/sqlc/systemdb"
)

func TestPairedSnapshotRestoresBothStores(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	store, err := Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := systemdb.New(store.SystemDB()).SetGatewayMetadata(ctx, systemdb.SetGatewayMetadataParams{Key: "instance", Value: "system-value"}); err != nil {
		t.Fatal(err)
	}
	if err := datadb.New(store.DataDB()).SetProjectionMetadata(ctx, datadb.SetProjectionMetadataParams{Key: "projection", Value: "data-value"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	snapshot := filepath.Join(root, "snapshot")
	manifest, err := CreateSnapshot(ctx, source, snapshot, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Generation == "" || len(manifest.Files) != 2 {
		t.Fatalf("invalid manifest: %+v", manifest)
	}
	if manifest.FormatVersion != 2 {
		t.Fatalf("snapshot format = %d", manifest.FormatVersion)
	}

	restored := filepath.Join(root, "restored")
	if err := RestoreSnapshot(ctx, snapshot, restored); err != nil {
		t.Fatal(err)
	}
	restoredStore, err := Open(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredStore.Close()
	systemValue, err := systemdb.New(restoredStore.SystemDB()).GetGatewayMetadata(ctx, "instance")
	if err != nil || systemValue != "system-value" {
		t.Fatalf("restored system value = %q, error = %v", systemValue, err)
	}
	dataValue, err := datadb.New(restoredStore.DataDB()).GetProjectionMetadata(ctx, "projection")
	if err != nil || dataValue != "data-value" {
		t.Fatalf("restored data value = %q, error = %v", dataValue, err)
	}
}

func TestRestoreFailureLeavesSourceUntouchedAndRejectsFutureSchema(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	store, err := Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := systemdb.New(store.SystemDB()).SetGatewayMetadata(ctx, systemdb.SetGatewayMetadataParams{Key: "sentinel", Value: "preserved"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	snapshot := filepath.Join(root, "snapshot")
	if _, err := CreateSnapshot(ctx, source, snapshot, "test-version"); err != nil {
		t.Fatal(err)
	}

	database, err := sql.Open("sqlite", filepath.Join(snapshot, "system.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO schema_migrations (version, name, checksum) VALUES (999, 'future.sql', 'future')"); err != nil {
		t.Fatal(err)
	}
	database.Close()
	manifest, err := readManifest(filepath.Join(snapshot, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for index := range manifest.Files {
		if manifest.Files[index].Name == "system.db" {
			manifest.Files[index].SHA256, manifest.Files[index].Size, err = hashFile(filepath.Join(snapshot, "system.db"))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	manifest.SchemaVersions["system"] = 999
	if err := writeManifest(filepath.Join(snapshot, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}

	err = RestoreSnapshot(ctx, snapshot, filepath.Join(root, "restored"))
	if err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("restore error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "restored")); !os.IsNotExist(err) {
		t.Fatalf("failed restore left a target: %v", err)
	}

	sourceStore, err := Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer sourceStore.Close()
	value, err := systemdb.New(sourceStore.SystemDB()).GetGatewayMetadata(ctx, "sentinel")
	if err != nil || value != "preserved" {
		t.Fatalf("source sentinel = %q, error = %v", value, err)
	}
}

func TestCreateSnapshotRequiresAnExistingDatabasePair(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if _, err := CreateSnapshot(ctx, missing, filepath.Join(root, "snapshot"), "test"); err == nil {
		t.Fatal("snapshot unexpectedly created a missing source")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing source was changed: %v", err)
	}

	partial := filepath.Join(root, "partial")
	if err := os.Mkdir(partial, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, "system.db"), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateSnapshot(ctx, partial, filepath.Join(root, "partial-snapshot"), "test"); err == nil || !strings.Contains(err.Error(), "missing data.db") {
		t.Fatalf("partial source error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(partial, "data.db")); !os.IsNotExist(err) {
		t.Fatalf("missing data database was created: %v", err)
	}
}

func TestRestoreRejectsDatabasesFromDifferentGenerations(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	store, err := Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	snapshot := filepath.Join(root, "snapshot")
	if _, err := CreateSnapshot(ctx, source, snapshot, "test"); err != nil {
		t.Fatal(err)
	}

	database, err := sql.Open("sqlite", filepath.Join(snapshot, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE projection_metadata SET value = 'different' WHERE key = 'snapshot_generation'"); err != nil {
		t.Fatal(err)
	}
	database.Close()
	manifest, err := readManifest(filepath.Join(snapshot, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for index := range manifest.Files {
		if manifest.Files[index].Name == "data.db" {
			manifest.Files[index].SHA256, manifest.Files[index].Size, err = hashFile(filepath.Join(snapshot, "data.db"))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writeManifest(filepath.Join(snapshot, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}

	err = RestoreSnapshot(ctx, snapshot, filepath.Join(root, "restored"))
	if err == nil || !strings.Contains(err.Error(), "generation does not match") {
		t.Fatalf("restore error = %v", err)
	}
}
