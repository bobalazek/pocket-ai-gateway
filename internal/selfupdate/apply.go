package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func Run(ctx context.Context, options Options) (Plan, error) {
	if options.goos == "" {
		options.goos = runtime.GOOS
	}
	if options.goarch == "" {
		options.goarch = runtime.GOARCH
	}
	if options.goos != "linux" || (options.goarch != "amd64" && options.goarch != "arm64") {
		return Plan{}, errors.New("self-update supports Linux amd64 and arm64 executables")
	}
	executable, err := resolveExecutable(options.Executable)
	if err != nil {
		return Plan{}, err
	}
	dataDir, err := filepath.Abs(options.DataDir)
	if err != nil || strings.TrimSpace(options.DataDir) == "" {
		return Plan{}, errors.New("data directory is required")
	}
	options.Executable, options.DataDir = executable, dataDir
	artifact, err := resolve(ctx, options)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{CurrentVersion: options.CurrentVersion, TargetVersion: artifact.ReleaseVersion, OS: options.goos, Arch: options.goarch, ArtifactURL: artifact.URL, SHA256: artifact.SHA256, Size: artifact.Size, Executable: executable, DataDir: dataDir, DryRun: !options.Apply}
	staged, err := stageArtifact(ctx, options.Client, artifact, filepath.Dir(executable))
	if err != nil {
		return Plan{}, err
	}
	defer func() { _ = os.Remove(staged) }()
	if err := validateBinary(ctx, staged, plan.TargetVersion); err != nil {
		return Plan{}, err
	}
	if !options.Apply {
		return plan, nil
	}
	snapshot, err := createUpdateSnapshot(ctx, dataDir, plan.TargetVersion, options.CurrentVersion)
	if err != nil {
		return Plan{}, fmt.Errorf("create pre-update snapshot (is the server stopped?): %w", err)
	}
	plan.Snapshot = snapshot
	backup, err := swapIn(executable, staged)
	if err != nil {
		return Plan{}, err
	}
	staged = ""
	previous, err := reserveSibling(executable, ".previous-")
	if err != nil {
		_, restoreErr := restoreExecutable(executable, backup)
		return Plan{}, errors.Join(fmt.Errorf("reserve previous binary path: %w", err), restoreErr)
	}
	if err := os.Rename(backup, previous); err != nil {
		_, restoreErr := restoreExecutable(executable, backup)
		return Plan{}, errors.Join(fmt.Errorf("retain previous binary: %w", err), restoreErr)
	}
	if err := syncDirectory(filepath.Dir(executable)); err != nil {
		_, restoreErr := restoreExecutable(executable, previous)
		return Plan{}, errors.Join(fmt.Errorf("sync retained previous binary: %w", err), restoreErr)
	}
	probe := options.probe
	if probe == nil {
		probe = probeReady
	}
	if err := probe(ctx, executable, dataDir); err != nil {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		rollbackErr := rollback(rollbackCtx, executable, previous, dataDir, snapshot)
		return Plan{}, errors.Join(fmt.Errorf("updated binary failed readiness: %w", err), rollbackErr)
	}
	plan.PreviousBinary = previous
	return plan, nil
}

func resolveExecutable(value string) (string, error) {
	if value == "" {
		var err error
		value, err = os.Executable()
		if err != nil {
			return "", fmt.Errorf("resolve executable: %w", err)
		}
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("executable must be a regular file, not a symlink")
	}
	return absolute, nil
}

func stageArtifact(ctx context.Context, client *http.Client, artifact Artifact, directory string) (name string, err error) {
	if client == nil {
		client = secureHTTPClient()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "pocket-ai-gateway-updater")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download release artifact: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download release artifact: unexpected HTTP status %d", response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != artifact.Size {
		return "", errors.New("release artifact size does not match the signed manifest")
	}
	temporary, err := os.CreateTemp(directory, ".pocket-ai-gateway-update-")
	if err != nil {
		return "", fmt.Errorf("create staged binary: %w", err)
	}
	name = temporary.Name()
	defer func() {
		if closeErr := temporary.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(name)
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(response.Body, artifact.Size+1))
	if err != nil {
		return "", fmt.Errorf("write staged binary: %w", err)
	}
	if written != artifact.Size {
		return "", errors.New("release artifact size does not match the signed manifest")
	}
	var extra [1]byte
	if count, readErr := response.Body.Read(extra[:]); readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", fmt.Errorf("finish release artifact: %w", readErr)
	} else if count != 0 {
		return "", errors.New("release artifact exceeds the signed size")
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), artifact.SHA256) {
		return "", errors.New("release artifact checksum does not match the signed manifest")
	}
	if err = temporary.Chmod(0o755); err != nil {
		return "", fmt.Errorf("make staged binary executable: %w", err)
	}
	if err = temporary.Sync(); err != nil {
		return "", fmt.Errorf("sync staged binary: %w", err)
	}
	return name, nil
}

func validateBinary(ctx context.Context, binary, version string) error {
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(checkCtx, binary, "version")
	var output limitedBuffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		return fmt.Errorf("run staged binary version check: %w (%s)", err, strings.TrimSpace(output.String()))
	}
	if strings.TrimSpace(output.String()) != version {
		return fmt.Errorf("staged binary reports %q, expected %q", strings.TrimSpace(output.String()), version)
	}
	return nil
}

func createUpdateSnapshot(ctx context.Context, dataDir, targetVersion, currentVersion string) (string, error) {
	parent := filepath.Join(filepath.Dir(dataDir), "."+filepath.Base(dataDir)+".pre-update")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	name := strings.TrimPrefix(targetVersion, "v") + "-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	output := filepath.Join(parent, name)
	if _, err := storage.CreateSnapshot(ctx, dataDir, output, currentVersion); err != nil {
		return "", err
	}
	return output, nil
}

func swapIn(executable, staged string) (string, error) {
	backup, err := exchangeExecutable(executable, staged)
	if err != nil {
		return "", err
	}
	if err := syncDirectory(filepath.Dir(executable)); err != nil {
		_, restoreErr := restoreExecutable(executable, backup)
		return "", errors.Join(fmt.Errorf("sync updated executable: %w", err), restoreErr)
	}
	return backup, nil
}

func rollback(ctx context.Context, executable, backup, dataDir, snapshot string) error {
	_, binaryErr := restoreExecutable(executable, backup)
	failedData := filepath.Join(filepath.Dir(dataDir), "."+filepath.Base(dataDir)+".failed-update-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
	dataErr := os.Rename(dataDir, failedData)
	if dataErr == nil {
		dataErr = storage.RestoreSnapshot(ctx, snapshot, dataDir)
		if dataErr != nil {
			dataErr = errors.Join(dataErr, restoreFailedData(dataDir, failedData))
		}
	}
	return errors.Join(binaryErr, dataErr)
}

func restoreFailedData(dataDir, failedData string) error {
	failedRestore := ""
	if _, err := os.Lstat(dataDir); err == nil {
		var reserveErr error
		failedRestore, reserveErr = reserveSibling(dataDir, ".failed-restore-")
		if reserveErr != nil {
			return reserveErr
		}
		if err := os.Rename(dataDir, failedRestore); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(failedData, dataDir); err != nil {
		if failedRestore != "" {
			_ = os.Rename(failedRestore, dataDir)
		}
		return err
	}
	return syncDirectory(filepath.Dir(dataDir))
}

func restoreExecutable(executable, backup string) (string, error) {
	failed, err := reserveSibling(executable, ".failed-update-")
	if err != nil {
		return "", err
	}
	if err := os.Rename(executable, failed); err != nil {
		return "", err
	}
	if err := os.Rename(backup, executable); err != nil {
		_ = os.Rename(failed, executable)
		return "", err
	}
	return failed, syncDirectory(filepath.Dir(executable))
}

func reserveSibling(executable, suffix string) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(executable), filepath.Base(executable)+suffix)
	if err != nil {
		return "", err
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(name); err != nil {
		return "", err
	}
	return name, nil
}

func probeReady(ctx context.Context, binary, dataDir string) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := exec.Command(binary, "serve", "--listen", address, "--data-dir", dataDir)
	var output limitedBuffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	client := &http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{Proxy: nil}}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return fmt.Errorf("server exited before readiness: %w (%s)", err, strings.TrimSpace(output.String()))
		case <-probeCtx.Done():
			_ = command.Process.Kill()
			<-done
			return fmt.Errorf("readiness timed out: %w (%s)", probeCtx.Err(), strings.TrimSpace(output.String()))
		case <-ticker.C:
			request, _ := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://"+address+"/readyz", nil)
			response, requestErr := client.Do(request)
			if requestErr != nil {
				continue
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				continue
			}
			_ = command.Process.Signal(os.Interrupt)
			select {
			case err := <-done:
				if err != nil {
					return fmt.Errorf("updated server did not stop cleanly: %w", err)
				}
				return nil
			case <-time.After(10 * time.Second):
				_ = command.Process.Kill()
				<-done
				return errors.New("updated server did not stop within 10 seconds")
			}
		}
	}
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

type limitedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	remaining := 64<<10 - buffer.buffer.Len()
	if remaining > 0 {
		_, _ = buffer.buffer.Write(value[:min(len(value), remaining)])
	}
	return len(value), nil
}

func (buffer *limitedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}
