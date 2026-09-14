package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCreatesProtectedWALStoresAndLocksDirectory(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "data")
	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for _, name := range []string{"system.db", "data.db", "instance.lock"} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s permissions = %o", name, info.Mode().Perm())
		}
	}
	if versionLess(store.SQLiteVersion(), MinimumSQLiteVersion()) {
		t.Fatalf("SQLite version = %s", store.SQLiteVersion())
	}
	if store.SystemDB().Stats().MaxOpenConnections != 1 || store.DataDB().Stats().MaxOpenConnections != 1 {
		t.Fatal("Phase 1 stores must use one connection each")
	}

	if _, err := Open(ctx, directory); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("second open error = %v", err)
	}
}

func TestOpenRejectsMigrationChecksumMismatchAndNewerSchema(t *testing.T) {
	tests := []struct {
		name   string
		change string
		want   string
	}{
		{name: "checksum", change: "UPDATE schema_migrations SET checksum = 'tampered' WHERE version = 1", want: "checksum mismatch"},
		{name: "newer", change: "INSERT INTO schema_migrations (version, name, checksum) VALUES (999, 'future.sql', 'future')", want: "newer than supported"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			directory := filepath.Join(t.TempDir(), "data")
			store, err := Open(ctx, directory)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.SystemDB().ExecContext(ctx, test.change); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			_, err = Open(ctx, directory)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("open error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestVersionLess(t *testing.T) {
	if !versionLess("3.51.2", "3.51.3") || versionLess("3.53.4", "3.51.3") {
		t.Fatal("unexpected version comparison")
	}
}

func TestOpenPreflightsBothMigrationHistoriesBeforeApplying(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "data")
	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	system, err := sql.Open("sqlite", filepath.Join(directory, "system.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := system.ExecContext(ctx, "DELETE FROM schema_migrations"); err != nil {
		t.Fatal(err)
	}
	system.Close()
	data, err := sql.Open("sqlite", filepath.Join(directory, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.ExecContext(ctx, "INSERT INTO schema_migrations (version, name, checksum) VALUES (999, 'future.sql', 'future')"); err != nil {
		t.Fatal(err)
	}
	data.Close()

	if _, err := Open(ctx, directory); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("open error = %v", err)
	}
	system, err = sql.Open("sqlite", filepath.Join(directory, "system.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer system.Close()
	var count int
	if err := system.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("system migrations changed before data preflight: %d", count)
	}
}

func TestOpenCreatesVerifiedPreMigrationSnapshot(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "data")
	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	database, err := sql.Open("sqlite", filepath.Join(directory, "system.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "DROP TABLE event_outbox; DROP TABLE pricing_jobs; DROP TABLE cost_assessments; DROP TABLE usage_ledger; DROP TABLE concurrency_leases; DROP TABLE reservations; DROP TABLE attempts; DROP TABLE price_versions; DROP TABLE requests; DROP TABLE admission_clock; DROP TABLE quota_periods; DROP TABLE bucket_state; DROP TABLE limit_policies; DROP TABLE audit_events; DROP TABLE api_key_secrets; DROP TABLE api_keys; DROP TABLE login_throttles; DROP TABLE activation_tokens; DROP TABLE user_sessions; DROP TABLE setup_tokens; DROP TABLE users; DELETE FROM schema_migrations WHERE version >= 2"); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations("system")
	if err != nil {
		t.Fatal(err)
	}
	identity := migrations[1]
	if _, err := database.ExecContext(ctx, identity.contents); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO schema_migrations (version, name, checksum) VALUES (?, ?, ?)", identity.version, identity.name, identity.checksum); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO users (id, email, display_name, password_hash, role, status, created_at, updated_at) VALUES ('usr_owner', 'owner@example.test', 'Owner', 'hash', 'owner', 'active', 1, 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO sessions (verifier, user_id, expires_at, created_at) VALUES (x'0102', 'usr_owner', 9999999999999, 1)"); err != nil {
		t.Fatal(err)
	}
	database.Close()

	store, err = OpenWithVersion(ctx, directory, "v0.1.0-test")
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	database, err = sql.Open("sqlite", filepath.Join(directory, "system.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var ownerID, sessionUserID string
	var unrestricted int
	if err := database.QueryRowContext(ctx, "SELECT id, inference_unrestricted FROM users WHERE role = 'owner'").Scan(&ownerID, &unrestricted); err != nil || ownerID != "usr_owner" || unrestricted != 1 {
		t.Fatalf("migrated owner = %q unrestricted=%d, %v", ownerID, unrestricted, err)
	}
	if err := database.QueryRowContext(ctx, "SELECT user_id FROM user_sessions WHERE verifier = x'0102'").Scan(&sessionUserID); err != nil || sessionUserID != ownerID {
		t.Fatalf("migrated session user = %q, %v", sessionUserID, err)
	}
	manifests, err := filepath.Glob(filepath.Join(directory, ".pre-migration", "*", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 {
		t.Fatalf("pre-migration manifests = %d", len(manifests))
	}
	manifest, err := readManifest(manifests[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSnapshotFiles(filepath.Dir(manifests[0]), manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.GatewayVersion != "v0.1.0-test" || manifest.SQLiteVersion == "" {
		t.Fatalf("pre-migration provenance = gateway %q, SQLite %q", manifest.GatewayVersion, manifest.SQLiteVersion)
	}
}
