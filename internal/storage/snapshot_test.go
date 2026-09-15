package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
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
	seedWebSearchSnapshotRows(t, ctx, store)
	seedMessageBatchSnapshotRows(t, ctx, store)
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
	var maxCalls, callCount, dailyCalls int64
	if err := restoredStore.SystemDB().QueryRowContext(ctx, `SELECT web_search_max_calls,web_search_call_count FROM attempts WHERE id='att_web'`).Scan(&maxCalls, &callCount); err != nil || maxCalls != 2 || callCount != 1 {
		t.Fatalf("restored attempt web search=%d/%d error=%v", maxCalls, callCount, err)
	}
	if err := restoredStore.DataDB().QueryRowContext(ctx, `SELECT web_search_calls FROM usage_daily WHERE owner_user_id='usr_web'`).Scan(&dailyCalls); err != nil || dailyCalls != 1 {
		t.Fatalf("restored daily web search=%d error=%v", dailyCalls, err)
	}
	var batchStatus, customID, itemState string
	if err := restoredStore.SystemDB().QueryRowContext(ctx, `SELECT message_batches.processing_status,message_batch_items.custom_id,message_batch_items.state FROM message_batches JOIN message_batch_items ON message_batch_items.batch_id=message_batches.id WHERE message_batches.id='msgbatch_snapshot'`).Scan(&batchStatus, &customID, &itemState); err != nil || batchStatus != "ended" || customID != "snapshot_item" || itemState != "succeeded" {
		t.Fatalf("restored message batch=%q/%q/%q error=%v", batchStatus, customID, itemState, err)
	}
}

func TestSnapshotPreservesProviderMasterKey(t *testing.T) {
	ctx := context.Background()
	root, source := t.TempDir(), ""
	source = filepath.Join(root, "source")
	store, err := Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	key := []byte("01234567890123456789012345678901")
	if err := os.WriteFile(filepath.Join(source, "master.key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(root, "snapshot")
	if _, err := CreateSnapshot(ctx, source, snapshot, "test"); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored")
	if err := RestoreSnapshot(ctx, snapshot, restored); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(restored, "master.key"))
	if err != nil || string(got) != string(key) {
		t.Fatalf("restored master key mismatch: %v", err)
	}
}

func TestSnapshotRequiresMasterKeyForEncryptedFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	store, err := Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES('usr_file','file@example.test','File','hash','owner','active',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO api_keys(id,owner_user_id,label,state,scopes_json,created_at,updated_at) VALUES('key_file','usr_file','File','active','[]',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO openai_files(id,owner_user_id,key_id,filename,purpose,bytes,ciphertext,nonce,created_at,expires_at) VALUES('file_snapshot','usr_file','key_file','batch.jsonl','batch',1,?,?,1,3600001)`, make([]byte, 17), make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,metadata_json,output_expiry_seconds,request_total,created_at,in_progress_at,expires_at,retention_expires_at) VALUES('batch_snapshot','usr_file','key_file','file_snapshot','/v1/responses','24h','model','{}',3600,1,1,1,86400001,2592000001)`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO openai_batch_items(batch_id,ordinal,custom_id,result_id,request_bytes,request_ciphertext,request_nonce,reserved_result_bytes) VALUES('batch_snapshot',1,'snapshot','batch_req_1234567890123456',2,?,?,1024)`, make([]byte, 18), make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `DELETE FROM openai_files WHERE id='file_snapshot'`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = CreateSnapshot(ctx, source, filepath.Join(root, "missing-key"), "test"); err == nil || !strings.Contains(err.Error(), "without master.key") {
		t.Fatalf("missing-key snapshot error = %v", err)
	}
	if err = os.WriteFile(filepath.Join(source, "master.key"), make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(root, "snapshot")
	if _, err = CreateSnapshot(ctx, source, snapshot, "test"); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored")
	if err = RestoreSnapshot(ctx, snapshot, restored); err != nil {
		t.Fatal(err)
	}
	restoredStore, err := Open(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredStore.Close()
	var count int
	if err = restoredStore.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_batch_items WHERE batch_id='batch_snapshot'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("restored encrypted batch items=%d error=%v", count, err)
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

func TestEncryptedLiveSnapshotRoundTripAndAuthentication(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "source"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := systemdb.New(store.SystemDB()).SetGatewayMetadata(ctx, systemdb.SetGatewayMetadataParams{Key: "before", Value: "included"}); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "backups", "snapshot.pagbak")
	manifest, checksum, size, err := CreateEncryptedSnapshot(ctx, store, archive, "test", key)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Generation == "" || len(checksum) != 64 || size <= 0 {
		t.Fatalf("invalid backup result: generation=%q checksum=%q size=%d", manifest.Generation, checksum, size)
	}
	if err := systemdb.New(store.SystemDB()).SetGatewayMetadata(ctx, systemdb.SetGatewayMetadataParams{Key: "after", Value: "excluded"}); err != nil {
		t.Fatalf("source is unavailable after live snapshot: %v", err)
	}
	restored := filepath.Join(root, "nested", "restored")
	if err := RestoreEncryptedSnapshot(ctx, archive, restored, key); err != nil {
		t.Fatal(err)
	}
	restoredStore, err := Open(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredStore.Close()
	if value, err := systemdb.New(restoredStore.SystemDB()).GetGatewayMetadata(ctx, "before"); err != nil || value != "included" {
		t.Fatalf("restored value = %q, error = %v", value, err)
	}
	if _, err := systemdb.New(restoredStore.SystemDB()).GetGatewayMetadata(ctx, "after"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("post-snapshot write was restored: %v", err)
	}

	wrongKey := append([]byte(nil), key...)
	wrongKey[0] ^= 1
	if err := RestoreEncryptedSnapshot(ctx, archive, filepath.Join(root, "wrong-key"), wrongKey); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("wrong-key restore error = %v", err)
	}
	contents, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "truncated.pagbak"), contents[:len(contents)-1], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreEncryptedSnapshot(ctx, filepath.Join(root, "truncated.pagbak"), filepath.Join(root, "truncated"), key); err == nil {
		t.Fatal("truncated archive was accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "trailing.pagbak"), append(contents, 0), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreEncryptedSnapshot(ctx, filepath.Join(root, "trailing.pagbak"), filepath.Join(root, "trailing"), key); err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("trailing archive error = %v", err)
	}
}

func TestEncryptedSnapshotHonorsCancellation(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "source"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output := filepath.Join(t.TempDir(), "cancelled.pagbak")
	if _, _, _, err := CreateEncryptedSnapshot(ctx, store, output, "test", make([]byte, 32)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled archive exists: %v", err)
	}
}

func TestSnapshotHashHonorsCancellation(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "snapshot")
	if err := os.WriteFile(filename, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := hashFileContext(ctx, filename); !errors.Is(err, context.Canceled) {
		t.Fatalf("hash error = %v", err)
	}
}
