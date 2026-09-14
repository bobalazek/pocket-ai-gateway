package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	_ "modernc.org/sqlite"
)

const minimumSQLiteVersion = "3.51.3"

type Store struct {
	dataDir       string
	lock          *flock.Flock
	system        *sql.DB
	data          *sql.DB
	sqliteVersion string
}

func Open(ctx context.Context, dataDir string) (store *Store, err error) {
	return OpenWithVersion(ctx, dataDir, "dev")
}

func OpenWithVersion(ctx context.Context, dataDir, gatewayVersion string) (store *Store, err error) {
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("protect data directory: %w", err)
	}

	store, err = lockedStore(absolute)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = store.Close()
		}
	}()

	systemPath := filepath.Join(absolute, "system.db")
	dataPath := filepath.Join(absolute, "data.db")
	systemExists, err := regularFileExists(systemPath)
	if err != nil {
		return nil, err
	}
	dataExists, err := regularFileExists(dataPath)
	if err != nil {
		return nil, err
	}
	if systemExists != dataExists {
		return nil, errors.New("data directory must contain both system.db and data.db, or neither")
	}
	creating := !systemExists
	defer func() {
		if err != nil && creating {
			removeDatabaseFiles(systemPath)
			removeDatabaseFiles(dataPath)
		}
	}()

	store.system, err = openDatabase(ctx, systemPath, "system", creating, creating)
	if err != nil {
		return nil, err
	}
	store.data, err = openDatabase(ctx, dataPath, "data", creating, creating)
	if err != nil {
		return nil, err
	}
	if err := store.readAndValidateSQLiteVersion(ctx); err != nil {
		return nil, err
	}
	if !creating {
		systemPending, pendingErr := migrationPending(ctx, store.system, "system")
		if pendingErr != nil {
			return nil, pendingErr
		}
		dataPending, pendingErr := migrationPending(ctx, store.data, "data")
		if pendingErr != nil {
			return nil, pendingErr
		}
		if systemPending || dataPending {
			generation, generationErr := randomID()
			if generationErr != nil {
				return nil, generationErr
			}
			backupPath := filepath.Join(absolute, ".pre-migration", time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+generation)
			if _, err = createSnapshotFromStore(ctx, store, backupPath, gatewayVersion, generation); err != nil {
				return nil, fmt.Errorf("create pre-migration snapshot: %w", err)
			}
			store.system, err = openDatabase(ctx, systemPath, "system", true, false)
			if err != nil {
				return nil, err
			}
			store.data, err = openDatabase(ctx, dataPath, "data", true, false)
			if err != nil {
				return nil, err
			}
		}
	}
	return store, nil
}

func openForSnapshot(ctx context.Context, dataDir string) (store *Store, err error) {
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("open existing data directory: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("data path is not a directory")
	}
	store, err = lockedStore(absolute)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = store.Close()
		}
	}()
	for _, item := range []struct {
		name string
		path string
	}{{"system", filepath.Join(absolute, "system.db")}, {"data", filepath.Join(absolute, "data.db")}} {
		exists, checkErr := regularFileExists(item.path)
		if checkErr != nil {
			return nil, checkErr
		}
		if !exists {
			return nil, fmt.Errorf("snapshot source is missing %s.db", item.name)
		}
	}
	store.system, err = openDatabase(ctx, filepath.Join(absolute, "system.db"), "system", false, false)
	if err != nil {
		return nil, err
	}
	store.data, err = openDatabase(ctx, filepath.Join(absolute, "data.db"), "data", false, false)
	if err != nil {
		return nil, err
	}
	if _, err := migrationPending(ctx, store.system, "system"); err != nil {
		return nil, err
	}
	if _, err := migrationPending(ctx, store.data, "data"); err != nil {
		return nil, err
	}
	if err := store.readAndValidateSQLiteVersion(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func lockedStore(dataDir string) (*Store, error) {
	instanceLock := flock.New(filepath.Join(dataDir, "instance.lock"), flock.SetPermissions(0o600))
	locked, err := instanceLock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock data directory: %w", err)
	}
	if !locked {
		return nil, errors.New("data directory is already in use by another Pocket AI Gateway process")
	}
	return &Store{dataDir: dataDir, lock: instanceLock}, nil
}

func openDatabase(ctx context.Context, filename, storeName string, migrate, create bool) (*sql.DB, error) {
	pragmas := []string{"busy_timeout(5000)", "foreign_keys(ON)", "synchronous(FULL)"}
	mode := "rw"
	if create {
		mode = "rwc"
		pragmas = append(pragmas, "journal_mode(WAL)")
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(filename), RawQuery: url.Values{
		"_pragma": pragmas,
		"mode":    []string{mode},
	}.Encode()}).String()
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s database: %w", storeName, err)
	}
	// A single connection per store prevents accidental in-process write contention.
	// Add a separate bounded read pool only when real concurrent query paths need it.
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("connect to %s database: %w", storeName, err)
	}
	if migrate {
		if err := applyMigrations(ctx, database, storeName); err != nil {
			database.Close()
			return nil, err
		}
	}
	if err := validatePragmas(ctx, database, storeName); err != nil {
		database.Close()
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(filename+suffix, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			database.Close()
			return nil, fmt.Errorf("protect %s database file: %w", storeName, err)
		}
	}
	return database, nil
}

func (store *Store) readAndValidateSQLiteVersion(ctx context.Context) error {
	if err := store.system.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&store.sqliteVersion); err != nil {
		return fmt.Errorf("read SQLite version: %w", err)
	}
	if versionLess(store.sqliteVersion, minimumSQLiteVersion) {
		return fmt.Errorf("SQLite %s is unsupported; version %s or newer is required", store.sqliteVersion, minimumSQLiteVersion)
	}
	return nil
}

func regularFileExists(filename string) (bool, error) {
	info, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", filepath.Base(filename), err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s must be a regular file", filepath.Base(filename))
	}
	return true, nil
}

func removeDatabaseFiles(filename string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(filename + suffix)
	}
}

func validatePragmas(ctx context.Context, database *sql.DB, storeName string) error {
	var foreignKeys, busyTimeout int
	var journalMode string
	if err := database.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read %s foreign key setting: %w", storeName, err)
	}
	if err := database.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return fmt.Errorf("read %s busy timeout: %w", storeName, err)
	}
	if err := database.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("read %s journal mode: %w", storeName, err)
	}
	if foreignKeys != 1 || busyTimeout != 5000 || !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("unsafe %s SQLite settings: foreign_keys=%d busy_timeout=%d journal_mode=%s", storeName, foreignKeys, busyTimeout, journalMode)
	}
	return nil
}

func (store *Store) Close() error {
	if store == nil {
		return nil
	}
	closeErrors := store.closeDatabases()
	if store.lock != nil {
		closeErrors = append(closeErrors, store.lock.Close())
		store.lock = nil
	}
	return errors.Join(closeErrors...)
}

func (store *Store) closeDatabases() []error {
	var closeErrors []error
	if store.data != nil {
		closeErrors = append(closeErrors, store.data.Close())
		store.data = nil
	}
	if store.system != nil {
		closeErrors = append(closeErrors, store.system.Close())
		store.system = nil
	}
	return closeErrors
}

func (store *Store) SystemDB() *sql.DB     { return store.system }
func (store *Store) DataDB() *sql.DB       { return store.data }
func (store *Store) DataDir() string       { return store.dataDir }
func (store *Store) SQLiteVersion() string { return store.sqliteVersion }
func MinimumSQLiteVersion() string         { return minimumSQLiteVersion }

func versionLess(current, minimum string) bool {
	currentParts := strings.Split(current, ".")
	minimumParts := strings.Split(minimum, ".")
	for index := 0; index < 3; index++ {
		currentPart, minimumPart := 0, 0
		if index < len(currentParts) {
			currentPart, _ = strconv.Atoi(currentParts[index])
		}
		if index < len(minimumParts) {
			minimumPart, _ = strconv.Atoi(minimumParts[index])
		}
		if currentPart != minimumPart {
			return currentPart < minimumPart
		}
	}
	return false
}

func integrityCheck(ctx context.Context, database *sql.DB) error {
	var result string
	if err := database.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity check returned %q", result)
	}
	return nil
}
