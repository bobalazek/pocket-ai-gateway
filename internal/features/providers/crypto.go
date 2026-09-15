package providers

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
)

const masterKeySize = 32

func LoadOrCreateMasterKey(dataDir string, requireExisting ...bool) ([]byte, error) {
	filename := filepath.Join(dataDir, "master.key")
	if info, err := os.Lstat(filename); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("master.key must be a private regular file")
		}
		value, err := os.ReadFile(filename)
		if err != nil || len(value) != masterKeySize {
			return nil, errors.New("master.key must contain exactly 32 bytes")
		}
		return value, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect master.key: %w", err)
	}
	if len(requireExisting) > 0 && requireExisting[0] {
		return nil, errors.New("master.key is missing for stored encrypted data")
	}
	value := make([]byte, masterKeySize)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return nil, fmt.Errorf("create master key: %w", err)
	}
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create master.key: %w", err)
	}
	if _, err := file.Write(value); err != nil {
		file.Close()
		return nil, fmt.Errorf("write master.key: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return nil, fmt.Errorf("sync master.key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close master.key: %w", err)
	}
	return value, nil
}

func seal(key []byte, connectionID, plaintext string) ([]byte, []byte, error) {
	return credentials.Seal(key, []byte(plaintext), []byte(connectionID))
}

func openSecret(key []byte, connectionID string, ciphertext, nonce []byte) (string, error) {
	value, err := credentials.Open(key, ciphertext, nonce, []byte(connectionID))
	if err != nil {
		return "", errors.New("provider credential cannot be decrypted")
	}
	return string(value), nil
}
