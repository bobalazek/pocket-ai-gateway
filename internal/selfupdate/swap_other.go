//go:build !linux

package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
)

func exchangeExecutable(executable, staged string) (string, error) {
	backupFile, err := os.CreateTemp(filepath.Dir(executable), ".pocket-ai-gateway-rollback-")
	if err != nil {
		return "", fmt.Errorf("reserve rollback binary: %w", err)
	}
	backup := backupFile.Name()
	if err := backupFile.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(backup); err != nil {
		return "", err
	}
	if err := os.Rename(executable, backup); err != nil {
		return "", fmt.Errorf("stage current binary for rollback: %w", err)
	}
	if err := os.Rename(staged, executable); err != nil {
		_ = os.Rename(backup, executable)
		return "", fmt.Errorf("install updated binary: %w", err)
	}
	return backup, nil
}
