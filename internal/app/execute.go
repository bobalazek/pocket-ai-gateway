package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
	"github.com/bobalazek/pocket-ai-gateway/internal/server"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func Execute(ctx context.Context, version string, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	switch args[0] {
	case "version":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "version does not accept arguments")
			return 2
		}
		fmt.Fprintln(stdout, version)
		return 0
	case "serve":
		cfg, err := ParseServe(args[1:], getenv, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "configuration error: %v\n", err)
			return 2
		}
		if err := serve(ctx, version, cfg, stdout); err != nil {
			fmt.Fprintf(stderr, "server error: %v\n", err)
			return 1
		}
		return 0
	case "snapshot":
		dataDir, snapshotDir, err := parseStorageCommand("snapshot", args[1:], getenv, stderr, "output")
		if err != nil {
			fmt.Fprintf(stderr, "configuration error: %v\n", err)
			return 2
		}
		manifest, err := storage.CreateSnapshot(ctx, dataDir, snapshotDir, version)
		if err != nil {
			fmt.Fprintf(stderr, "snapshot error: %v\n", err)
			return 1
		}
		if err := json.NewEncoder(stdout).Encode(manifest); err != nil {
			fmt.Fprintf(stderr, "snapshot output error: %v\n", err)
			return 1
		}
		return 0
	case "restore":
		dataDir, snapshotDir, err := parseStorageCommand("restore", args[1:], getenv, stderr, "snapshot")
		if err != nil {
			fmt.Fprintf(stderr, "configuration error: %v\n", err)
			return 2
		}
		if err := storage.RestoreSnapshot(ctx, snapshotDir, dataDir); err != nil {
			fmt.Fprintf(stderr, "restore error: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "restored snapshot into %s\n", dataDir)
		return 0
	case "owner-reset":
		dataDir, err := parseDataDirCommand("owner-reset", args[1:], getenv, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "configuration error: %v\n", err)
			return 2
		}
		stores, err := storage.OpenWithVersion(ctx, dataDir, version)
		if err != nil {
			fmt.Fprintf(stderr, "owner reset error: %v\n", err)
			return 1
		}
		code, codeErr := credentials.RandomToken(24)
		if codeErr != nil {
			_ = stores.Close()
			fmt.Fprintf(stderr, "owner reset error: %v\n", codeErr)
			return 1
		}
		if err := writeProtectedCode(dataDir, "recovery-code", code); err != nil {
			_ = stores.Close()
			fmt.Fprintf(stderr, "owner reset error: %v\n", err)
			return 1
		}
		recoveryErr := auth.New(stores.SystemDB()).PrepareOwnerRecovery(ctx, code)
		closeErr := stores.Close()
		if recoveryErr != nil || closeErr != nil {
			_ = os.Remove(filepath.Join(dataDir, "recovery-code"))
			fmt.Fprintf(stderr, "owner reset error: %v\n", errors.Join(recoveryErr, closeErr))
			return 1
		}
		fmt.Fprintf(stdout, "Owner recovery code file: %s\n", filepath.Join(dataDir, "recovery-code"))
		return 0
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func serve(ctx context.Context, version string, cfg Config, logOutput io.Writer) error {
	stores, err := storage.OpenWithVersion(ctx, cfg.DataDir, version)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer stores.Close()
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	publicOrigin := cfg.PublicURL
	if publicOrigin == "" {
		publicOrigin = "http://" + listener.Addr().String()
	}
	authService := auth.New(stores.SystemDB())
	usageService := usage.New(stores.SystemDB())
	var storedCredentials int
	if err := stores.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM provider_credentials WHERE ciphertext IS NOT NULL").Scan(&storedCredentials); err != nil {
		return fmt.Errorf("inspect provider credentials: %w", err)
	}
	masterKey, err := providers.LoadOrCreateMasterKey(stores.DataDir(), storedCredentials > 0)
	if err != nil {
		return fmt.Errorf("load provider credential key: %w", err)
	}
	providerService := providers.New(stores.SystemDB(), masterKey)
	if err := usageService.Recover(ctx); err != nil {
		return fmt.Errorf("recover usage accounting: %w", err)
	}
	setupCode, setupRequired, err := authService.PrepareSetup(ctx)
	if err != nil {
		return fmt.Errorf("prepare owner setup: %w", err)
	}
	if setupRequired {
		if err := writeSetupCode(stores.DataDir(), setupCode); err != nil {
			return err
		}
		fmt.Fprintf(logOutput, "First-time setup: %s\nSetup code file: %s\n", setupURL(publicOrigin), filepath.Join(stores.DataDir(), "setup-code"))
	} else if state, stateErr := authService.SetupStatus(ctx); stateErr != nil {
		return fmt.Errorf("read owner setup state: %w", stateErr)
	} else if !state.Recoverable {
		_ = os.Remove(filepath.Join(stores.DataDir(), "setup-code"))
	}

	logger := slog.New(slog.NewTextHandler(logOutput, &slog.HandlerOptions{Level: slog.LevelInfo}))
	workerContext, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go projectUsage(workerContext, stores.SystemDB(), stores.DataDB(), logger, workerDone)
	catalogDone := make(chan struct{})
	go func() { defer close(catalogDone); providerService.RunCatalogRefresh(workerContext) }()
	defer func() { stopWorker(); <-workerDone; <-catalogDone }()
	httpServer := &http.Server{
		Handler:           server.NewWithServices(stores.SystemDB(), publicOrigin, usageService, providerService),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpServer.Serve(listener)
	}()

	logger.Info("gateway started", "listen", listener.Addr().String(), "data_dir", cfg.DataDir, "sqlite_version", stores.SQLiteVersion(), "version", version)
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("gateway stopped")
	return nil
}

func projectUsage(ctx context.Context, system, data *sql.DB, logger *slog.Logger, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, err := usage.ProjectOutbox(ctx, system, data, 100); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("usage projection delayed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func setupURL(publicOrigin string) string { return strings.TrimRight(publicOrigin, "/") + "/_/setup/" }

func writeSetupCode(dataDir, code string) (err error) {
	return writeProtectedCode(dataDir, "setup-code", code)
}

func writeProtectedCode(dataDir, name, code string) (err error) {
	temporary, err := os.CreateTemp(dataDir, "."+name+"-")
	if err != nil {
		return fmt.Errorf("create setup code file: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if err != nil {
			_ = os.Remove(temporaryName)
		}
	}()
	if err = temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect setup code file: %w", err)
	}
	if _, err = temporary.WriteString(code + "\n"); err != nil {
		return fmt.Errorf("write setup code file: %w", err)
	}
	if err = temporary.Sync(); err != nil {
		return fmt.Errorf("sync setup code file: %w", err)
	}
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("close setup code file: %w", err)
	}
	if err = os.Rename(temporaryName, filepath.Join(dataDir, name)); err != nil {
		return fmt.Errorf("publish setup code file: %w", err)
	}
	directory, err := os.Open(dataDir)
	if err != nil {
		return fmt.Errorf("open setup code directory: %w", err)
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil {
		return fmt.Errorf("sync setup code directory: %w", err)
	}
	return nil
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: pocket-ai-gateway <serve|snapshot|restore|owner-reset|version>")
}

func parseDataDirCommand(name string, args []string, getenv func(string) string, output io.Writer) (string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(output)
	dataDir := flags.String("data-dir", valueOr(getenv("POCKET_AI_GATEWAY_DATA_DIR"), defaultDataDir), "directory for local gateway data")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if flags.NArg() != 0 {
		return "", fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if strings.TrimSpace(*dataDir) == "" {
		return "", errors.New("data directory cannot be empty")
	}
	return filepath.Abs(*dataDir)
}

func parseStorageCommand(name string, args []string, getenv func(string) string, output io.Writer, snapshotFlag string) (string, string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(output)
	dataDir := flags.String("data-dir", valueOr(getenv("POCKET_AI_GATEWAY_DATA_DIR"), defaultDataDir), "directory for local gateway data")
	snapshotDir := flags.String(snapshotFlag, "", "snapshot directory")
	if err := flags.Parse(args); err != nil {
		return "", "", err
	}
	if flags.NArg() != 0 {
		return "", "", fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if *snapshotDir == "" {
		return "", "", fmt.Errorf("--%s is required", snapshotFlag)
	}
	absoluteDataDir, err := filepath.Abs(*dataDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve data directory: %w", err)
	}
	absoluteSnapshotDir, err := filepath.Abs(*snapshotDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve snapshot directory: %w", err)
	}
	return absoluteDataDir, absoluteSnapshotDir, nil
}
