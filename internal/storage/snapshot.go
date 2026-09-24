package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const SnapshotFormatVersion = 2

type SnapshotManifest struct {
	FormatVersion  int              `json:"format_version"`
	GatewayVersion string           `json:"gateway_version"`
	SQLiteVersion  string           `json:"sqlite_version"`
	Generation     string           `json:"generation"`
	CreatedAt      string           `json:"created_at"`
	SchemaVersions map[string]int64 `json:"schema_versions"`
	Files          []SnapshotFile   `json:"files"`
}

type SnapshotFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func CreateSnapshot(ctx context.Context, dataDir, outputDir, gatewayVersion string) (SnapshotManifest, error) {
	dataPath, err := filepath.Abs(dataDir)
	if err != nil {
		return SnapshotManifest{}, fmt.Errorf("resolve data directory: %w", err)
	}
	outputPath, err := filepath.Abs(outputDir)
	if err != nil {
		return SnapshotManifest{}, fmt.Errorf("resolve snapshot directory: %w", err)
	}
	if withinPath(outputPath, dataPath) {
		return SnapshotManifest{}, errors.New("snapshot directory must be outside the data directory")
	}
	if err := requireAbsent(outputPath, "snapshot directory"); err != nil {
		return SnapshotManifest{}, err
	}

	store, err := openForSnapshot(ctx, dataPath)
	if err != nil {
		return SnapshotManifest{}, err
	}
	defer store.Close()
	return createSnapshotFromStore(ctx, store, outputPath, gatewayVersion, "")
}

func CreateLiveSnapshot(ctx context.Context, store *Store, outputDir, gatewayVersion string) (SnapshotManifest, error) {
	outputPath, err := filepath.Abs(outputDir)
	if err != nil {
		return SnapshotManifest{}, fmt.Errorf("resolve snapshot directory: %w", err)
	}
	if withinPath(outputPath, store.DataDir()) {
		return SnapshotManifest{}, errors.New("snapshot directory must be outside the data directory")
	}
	return createSnapshotFromStore(ctx, store, outputPath, gatewayVersion, "")
}

func createSnapshotFromStore(ctx context.Context, store *Store, outputPath, gatewayVersion, generation string) (SnapshotManifest, error) {
	unlockProjection := store.LockProjection()
	defer unlockProjection()
	if err := requireAbsent(outputPath, "snapshot directory"); err != nil {
		return SnapshotManifest{}, err
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return SnapshotManifest{}, fmt.Errorf("create snapshot parent: %w", err)
	}
	if generation == "" {
		var err error
		generation, err = randomID()
		if err != nil {
			return SnapshotManifest{}, err
		}
	}
	if err := recordSnapshotGeneration(ctx, store, generation); err != nil {
		return SnapshotManifest{}, err
	}

	for _, database := range []struct {
		name   string
		handle *sql.DB
	}{{"system", store.SystemDB()}, {"data", store.DataDB()}} {
		if err := integrityCheck(ctx, database.handle); err != nil {
			return SnapshotManifest{}, fmt.Errorf("verify %s database: %w", database.name, err)
		}
	}
	systemVersion, err := schemaVersion(ctx, store.SystemDB())
	if err != nil {
		return SnapshotManifest{}, fmt.Errorf("read system schema version: %w", err)
	}
	dataVersion, err := schemaVersion(ctx, store.DataDB())
	if err != nil {
		return SnapshotManifest{}, fmt.Errorf("read data schema version: %w", err)
	}
	requiresKey, err := encryptedDataExists(ctx, store.SystemDB())
	if err != nil {
		return SnapshotManifest{}, fmt.Errorf("inspect encrypted storage: %w", err)
	}
	if _, err := os.Lstat(filepath.Join(store.DataDir(), "master.key")); requiresKey && errors.Is(err, os.ErrNotExist) {
		return SnapshotManifest{}, errors.New("cannot snapshot stored encrypted data without master.key")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return SnapshotManifest{}, fmt.Errorf("inspect master.key: %w", err)
	}
	temporary, err := os.MkdirTemp(filepath.Dir(outputPath), "."+filepath.Base(outputPath)+".tmp-")
	if err != nil {
		return SnapshotManifest{}, fmt.Errorf("create temporary snapshot: %w", err)
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return SnapshotManifest{}, fmt.Errorf("protect temporary snapshot: %w", err)
	}

	manifest := SnapshotManifest{
		FormatVersion:  SnapshotFormatVersion,
		GatewayVersion: gatewayVersion,
		SQLiteVersion:  store.SQLiteVersion(),
		Generation:     generation,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersions: map[string]int64{"system": systemVersion, "data": dataVersion},
	}
	// Projection is paused across both copies. New admissions remain queued in
	// the outbox and are replayed idempotently after restore.
	for _, database := range []struct {
		name   string
		handle *sql.DB
	}{{"data.db", store.DataDB()}, {"system.db", store.SystemDB()}} {
		if err := vacuumInto(ctx, database.handle, filepath.Join(temporary, database.name)); err != nil {
			return SnapshotManifest{}, fmt.Errorf("snapshot %s: %w", database.name, err)
		}
	}
	names := []string{"system.db", "data.db"}
	if _, err := os.Lstat(filepath.Join(store.DataDir(), "master.key")); err == nil {
		names = append(names, "master.key")
	} else if !errors.Is(err, os.ErrNotExist) {
		return SnapshotManifest{}, fmt.Errorf("inspect master.key: %w", err)
	}
	for _, name := range names {
		destination := filepath.Join(temporary, name)
		if name == "master.key" {
			if err := copyProtectedFile(filepath.Join(store.DataDir(), name), destination); err != nil {
				return SnapshotManifest{}, err
			}
		} else if err := os.Chmod(destination, 0o600); err != nil {
			return SnapshotManifest{}, err
		}
		hash, size, err := hashFileContext(ctx, destination)
		if err != nil {
			return SnapshotManifest{}, err
		}
		manifest.Files = append(manifest.Files, SnapshotFile{Name: name, SHA256: hash, Size: size})
	}
	if err := writeManifest(filepath.Join(temporary, "manifest.json"), manifest); err != nil {
		return SnapshotManifest{}, err
	}
	if err := syncDirectory(temporary); err != nil {
		return SnapshotManifest{}, fmt.Errorf("sync snapshot directory: %w", err)
	}
	if err := requireAbsent(outputPath, "snapshot directory"); err != nil {
		return SnapshotManifest{}, err
	}
	if err := os.Rename(temporary, outputPath); err != nil {
		return SnapshotManifest{}, fmt.Errorf("publish snapshot: %w", err)
	}
	if err := syncDirectory(filepath.Dir(outputPath)); err != nil {
		return SnapshotManifest{}, fmt.Errorf("sync snapshot parent: %w", err)
	}
	return manifest, nil
}

func encryptedDataExists(ctx context.Context, database *sql.DB) (bool, error) {
	for _, item := range []struct {
		table string
		where string
	}{{"provider_credentials", "ciphertext IS NOT NULL"}, {"openai_files", "1"}, {"anthropic_files", "1"}, {"openai_batch_items", "request_ciphertext IS NOT NULL OR result_ciphertext IS NOT NULL"}, {"openai_upload_parts", "ciphertext IS NOT NULL"}} {
		var exists int
		if err := database.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name=?)`, item.table).Scan(&exists); err != nil {
			return false, err
		}
		if exists == 0 {
			continue
		}
		if err := database.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+item.table+" WHERE "+item.where+")").Scan(&exists); err != nil {
			return false, err
		}
		if exists != 0 {
			return true, nil
		}
	}
	return false, nil
}

func vacuumInto(ctx context.Context, database *sql.DB, filename string) error {
	quoted := strings.ReplaceAll(filename, "'", "''")
	if _, err := database.ExecContext(ctx, "VACUUM INTO '"+quoted+"'"); err != nil {
		return err
	}
	snapshot, err := openDatabase(ctx, filename, "snapshot", false, true)
	if err != nil {
		return err
	}
	return errors.Join(integrityCheck(ctx, snapshot), snapshot.Close())
}

func RestoreSnapshot(ctx context.Context, snapshotDir, dataDir string) error {
	snapshotPath, err := filepath.Abs(snapshotDir)
	if err != nil {
		return fmt.Errorf("resolve snapshot directory: %w", err)
	}
	dataPath, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("resolve data directory: %w", err)
	}
	if withinPath(dataPath, snapshotPath) {
		return errors.New("data directory must be outside the snapshot directory")
	}
	if err := requireAbsent(dataPath, "restore target"); err != nil {
		return err
	}

	manifest, err := readManifest(filepath.Join(snapshotPath, "manifest.json"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dataPath), 0o700); err != nil {
		return fmt.Errorf("create restore parent: %w", err)
	}
	temporary, err := os.MkdirTemp(filepath.Dir(dataPath), "."+filepath.Base(dataPath)+".restore-")
	if err != nil {
		return fmt.Errorf("create restore staging directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return fmt.Errorf("protect restore staging directory: %w", err)
	}
	for _, file := range manifest.Files {
		if file.Name != "system.db" && file.Name != "data.db" && file.Name != "master.key" {
			return fmt.Errorf("unexpected snapshot file %q", file.Name)
		}
		if err := copyProtectedFile(filepath.Join(snapshotPath, file.Name), filepath.Join(temporary, file.Name)); err != nil {
			return err
		}
	}
	if err := validateSnapshotFiles(ctx, temporary, manifest); err != nil {
		return err
	}
	if err := validateSnapshotDatabases(ctx, temporary, manifest); err != nil {
		return err
	}

	store, err := Open(ctx, temporary)
	if err != nil {
		return fmt.Errorf("validate restored databases: %w", err)
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close restored databases: %w", err)
	}
	if err := syncDirectory(temporary); err != nil {
		return fmt.Errorf("sync restored data: %w", err)
	}
	if err := requireAbsent(dataPath, "restore target"); err != nil {
		return err
	}
	if err := os.Rename(temporary, dataPath); err != nil {
		return fmt.Errorf("publish restored data: %w", err)
	}
	if err := syncDirectory(filepath.Dir(dataPath)); err != nil {
		return fmt.Errorf("sync restore parent: %w", err)
	}
	return nil
}

func recordSnapshotGeneration(ctx context.Context, store *Store, generation string) error {
	statements := []struct {
		name     string
		database *sql.DB
		query    string
	}{
		{"system", store.SystemDB(), "INSERT INTO gateway_metadata (key, value, updated_at) VALUES ('snapshot_generation', ?, CURRENT_TIMESTAMP) ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP"},
		{"data", store.DataDB(), "INSERT INTO projection_metadata (key, value, updated_at) VALUES ('snapshot_generation', ?, CURRENT_TIMESTAMP) ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP"},
	}
	for _, statement := range statements {
		if _, err := statement.database.ExecContext(ctx, statement.query, generation); err != nil {
			return fmt.Errorf("record %s snapshot generation: %w", statement.name, err)
		}
	}
	return nil
}

func validateSnapshotDatabases(ctx context.Context, directory string, manifest SnapshotManifest) error {
	for _, store := range []struct {
		name          string
		filename      string
		generationSQL string
	}{
		{"system", "system.db", "SELECT value FROM gateway_metadata WHERE key = 'snapshot_generation'"},
		{"data", "data.db", "SELECT value FROM projection_metadata WHERE key = 'snapshot_generation'"},
	} {
		database, err := openReadOnlyDatabase(ctx, filepath.Join(directory, store.filename))
		if err != nil {
			return fmt.Errorf("open snapshot %s database: %w", store.name, err)
		}
		version, versionErr := schemaVersion(ctx, database)
		var generation string
		generationErr := database.QueryRowContext(ctx, store.generationSQL).Scan(&generation)
		integrityErr := integrityCheck(ctx, database)
		closeErr := database.Close()
		if err := errors.Join(versionErr, generationErr, integrityErr, closeErr); err != nil {
			return fmt.Errorf("verify snapshot %s database: %w", store.name, err)
		}
		if version != manifest.SchemaVersions[store.name] {
			return fmt.Errorf("snapshot %s schema version does not match manifest", store.name)
		}
		if generation != manifest.Generation {
			return fmt.Errorf("snapshot %s generation does not match manifest", store.name)
		}
	}
	return nil
}

func openReadOnlyDatabase(ctx context.Context, filename string) (*sql.DB, error) {
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(filename), RawQuery: url.Values{"mode": []string{"ro"}}.Encode()}).String()
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func readManifest(filename string) (SnapshotManifest, error) {
	file, info, err := openRegularFile(filename)
	if err != nil {
		return SnapshotManifest{}, fmt.Errorf("open snapshot manifest: %w", err)
	}
	defer file.Close()
	if info.Size() > 1<<20 {
		return SnapshotManifest{}, errors.New("snapshot manifest exceeds 1 MiB")
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var manifest SnapshotManifest
	if err := decoder.Decode(&manifest); err != nil {
		return SnapshotManifest{}, fmt.Errorf("decode snapshot manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return SnapshotManifest{}, errors.New("snapshot manifest contains trailing data")
	}
	if manifest.FormatVersion != SnapshotFormatVersion {
		return SnapshotManifest{}, fmt.Errorf("unsupported snapshot format %d", manifest.FormatVersion)
	}
	if manifest.Generation == "" || (len(manifest.Files) != 2 && len(manifest.Files) != 3) || len(manifest.SchemaVersions) != 2 {
		return SnapshotManifest{}, errors.New("invalid paired snapshot manifest")
	}
	if _, ok := manifest.SchemaVersions["system"]; !ok {
		return SnapshotManifest{}, errors.New("snapshot manifest is missing system schema version")
	}
	if _, ok := manifest.SchemaVersions["data"]; !ok {
		return SnapshotManifest{}, errors.New("snapshot manifest is missing data schema version")
	}
	return manifest, nil
}

func validateSnapshotFiles(ctx context.Context, directory string, manifest SnapshotManifest) error {
	seen := map[string]bool{}
	for _, file := range manifest.Files {
		if file.Name != "system.db" && file.Name != "data.db" && file.Name != "master.key" {
			return fmt.Errorf("unexpected snapshot file %q", file.Name)
		}
		if seen[file.Name] {
			return fmt.Errorf("duplicate snapshot file %q", file.Name)
		}
		seen[file.Name] = true
		hash, size, err := hashFileContext(ctx, filepath.Join(directory, file.Name))
		if err != nil {
			return err
		}
		if hash != file.SHA256 || size != file.Size {
			return fmt.Errorf("snapshot file %s failed integrity verification", file.Name)
		}
	}
	if !seen["system.db"] || !seen["data.db"] {
		return errors.New("snapshot does not contain both databases")
	}
	if seen["master.key"] {
		info, err := os.Stat(filepath.Join(directory, "master.key"))
		if err != nil || info.Size() != 32 || info.Mode().Perm()&0o077 != 0 {
			return errors.New("snapshot master.key is invalid")
		}
	}
	return nil
}

func writeManifest(filename string, manifest SnapshotManifest) error {
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create snapshot manifest: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		file.Close()
		return fmt.Errorf("write snapshot manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync snapshot manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close snapshot manifest: %w", err)
	}
	return nil
}

func copyProtectedFile(source, destination string) error {
	input, _, err := openRegularFile(source)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(source), err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(destination), err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return fmt.Errorf("copy %s: %w", filepath.Base(source), err)
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return fmt.Errorf("sync %s: %w", filepath.Base(destination), err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close %s: %w", filepath.Base(destination), err)
	}
	return nil
}

func openRegularFile(filename string) (*os.File, os.FileInfo, error) {
	before, err := os.Lstat(filename)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, nil, errors.New("file is not a regular file")
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, nil, err
	}
	after, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !os.SameFile(before, after) {
		file.Close()
		return nil, nil, errors.New("file changed while opening")
	}
	return file, after, nil
}

func hashFile(filename string) (string, int64, error) {
	return hashFileContext(context.Background(), filename)
}

func hashFileContext(ctx context.Context, filename string) (string, int64, error) {
	file, _, err := openRegularFile(filename)
	if err != nil {
		return "", 0, fmt.Errorf("open snapshot file %s: %w", filepath.Base(filename), err)
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, contextReader{ctx: ctx, reader: file})
	if err != nil {
		return "", 0, fmt.Errorf("hash snapshot file %s: %w", filepath.Base(filename), err)
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func requireAbsent(path, label string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists: %s", label, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	return nil
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create snapshot generation: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func withinPath(candidate, parent string) bool {
	relative, err := filepath.Rel(parent, candidate)
	return err == nil && !filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
