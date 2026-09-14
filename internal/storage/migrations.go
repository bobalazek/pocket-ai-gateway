package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/system/*.sql migrations/data/*.sql
var migrationFiles embed.FS

type migration struct {
	version  int64
	name     string
	checksum string
	contents string
}

func applyMigrations(ctx context.Context, database *sql.DB, storeName string) error {
	migrations, err := loadMigrations(storeName)
	if err != nil {
		return err
	}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin %s migrations: %w", storeName, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		return fmt.Errorf("create %s migration table: %w", storeName, err)
	}

	applied, err := readAppliedMigrations(ctx, tx, storeName)
	if err != nil {
		return err
	}
	if err := validateAppliedMigrations(storeName, migrations, applied); err != nil {
		return err
	}

	for _, item := range migrations {
		if previous, ok := applied[item.version]; ok {
			if previous.name != item.name || previous.checksum != item.checksum {
				return fmt.Errorf("%s migration %04d checksum mismatch", storeName, item.version)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, item.contents); err != nil {
			return fmt.Errorf("apply %s migration %s: %w", storeName, item.name, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, name, checksum) VALUES (?, ?, ?)", item.version, item.name, item.checksum); err != nil {
			return fmt.Errorf("record %s migration %s: %w", storeName, item.name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s migrations: %w", storeName, err)
	}
	return nil
}

type migrationQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func migrationPending(ctx context.Context, database *sql.DB, storeName string) (bool, error) {
	migrations, err := loadMigrations(storeName)
	if err != nil {
		return false, err
	}
	applied, err := readAppliedMigrations(ctx, database, storeName)
	if err != nil {
		return false, err
	}
	if err := validateAppliedMigrations(storeName, migrations, applied); err != nil {
		return false, err
	}
	return len(applied) < len(migrations), nil
}

func readAppliedMigrations(ctx context.Context, database migrationQueryer, storeName string) (map[int64]migration, error) {
	applied := make(map[int64]migration)
	rows, err := database.QueryContext(ctx, "SELECT version, name, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("read %s migrations: %w", storeName, err)
	}
	defer rows.Close()
	for rows.Next() {
		var item migration
		if err := rows.Scan(&item.version, &item.name, &item.checksum); err != nil {
			return nil, fmt.Errorf("scan %s migration: %w", storeName, err)
		}
		applied[item.version] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s migration rows: %w", storeName, err)
	}
	return applied, nil
}

func validateAppliedMigrations(storeName string, migrations []migration, applied map[int64]migration) error {
	known := make(map[int64]migration, len(migrations))
	latest := int64(0)
	for _, item := range migrations {
		known[item.version] = item
		latest = item.version
	}
	for version, previous := range applied {
		if version > latest {
			return fmt.Errorf("%s database schema %d is newer than supported schema %d", storeName, version, latest)
		}
		current, ok := known[version]
		if !ok {
			return fmt.Errorf("%s database contains unrecognized migration %d", storeName, version)
		}
		if previous.name != current.name || previous.checksum != current.checksum {
			return fmt.Errorf("%s migration %04d checksum mismatch", storeName, version)
		}
	}
	return nil
}

func loadMigrations(storeName string) ([]migration, error) {
	directory := path.Join("migrations", storeName)
	entries, err := fs.ReadDir(migrationFiles, directory)
	if err != nil {
		return nil, fmt.Errorf("read %s migrations: %w", storeName, err)
	}

	items := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("invalid %s migration filename %q", storeName, entry.Name())
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil || version < 1 {
			return nil, fmt.Errorf("invalid %s migration version in %q", storeName, entry.Name())
		}
		contents, err := fs.ReadFile(migrationFiles, path.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s migration %q: %w", storeName, entry.Name(), err)
		}
		sum := sha256.Sum256(contents)
		items = append(items, migration{version: version, name: entry.Name(), checksum: hex.EncodeToString(sum[:]), contents: string(contents)})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].version < items[j].version })
	for index := 1; index < len(items); index++ {
		if items[index-1].version == items[index].version {
			return nil, fmt.Errorf("duplicate %s migration version %d", storeName, items[index].version)
		}
	}
	return items, nil
}

func schemaVersion(ctx context.Context, database *sql.DB) (int64, error) {
	var version int64
	if err := database.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}
