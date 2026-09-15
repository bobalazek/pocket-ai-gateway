package storage

import (
	"archive/tar"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	archiveMagic = "PAGBAK01"
	archiveChunk = 64 << 10
)

func DecodeBackupKey(value string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(key) != 32 {
		return nil, errors.New("backup key must be standard base64 encoding of exactly 32 random bytes")
	}
	return key, nil
}

func CreateEncryptedSnapshot(ctx context.Context, store *Store, output, version string, key []byte) (SnapshotManifest, string, int64, error) {
	if len(key) != 32 {
		return SnapshotManifest{}, "", 0, errors.New("backup key must contain 32 bytes")
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return SnapshotManifest{}, "", 0, err
	}
	parent, err := os.MkdirTemp(filepath.Dir(output), ".pocket-ai-gateway-snapshot-")
	if err != nil {
		return SnapshotManifest{}, "", 0, err
	}
	defer os.RemoveAll(parent)
	manifest, err := CreateLiveSnapshot(ctx, store, filepath.Join(parent, "snapshot"), version)
	if err != nil {
		return SnapshotManifest{}, "", 0, err
	}
	if err = encryptSnapshot(ctx, filepath.Join(parent, "snapshot"), output, key); err != nil {
		return SnapshotManifest{}, "", 0, err
	}
	hash, size, err := hashFileContext(ctx, output)
	return manifest, hash, size, err
}

func EncryptSnapshot(snapshotDir, output string, key []byte) (err error) {
	return encryptSnapshot(context.Background(), snapshotDir, output, key)
}

func encryptSnapshot(ctx context.Context, snapshotDir, output string, key []byte) (err error) {
	if len(key) != 32 {
		return errors.New("backup key must contain 32 bytes")
	}
	if err := requireAbsent(output, "backup archive"); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(output), "."+filepath.Base(output)+".tmp-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if err != nil {
			_ = os.Remove(temporaryName)
		}
	}()
	if err = temporary.Chmod(0o600); err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	header := make([]byte, len(archiveMagic)+8)
	copy(header, archiveMagic)
	if _, err = rand.Read(header[len(archiveMagic):]); err != nil {
		return err
	}
	if _, err = temporary.Write(header); err != nil {
		return err
	}
	encrypted := &archiveWriter{ctx: ctx, output: temporary, aead: aead, header: header, buffer: make([]byte, 0, archiveChunk)}
	tarWriter := tar.NewWriter(encrypted)
	for _, name := range []string{"manifest.json", "system.db", "data.db", "master.key"} {
		filename := filepath.Join(snapshotDir, name)
		file, info, openErr := openRegularFile(filename)
		if errors.Is(openErr, os.ErrNotExist) && name == "master.key" {
			continue
		}
		if openErr != nil {
			return openErr
		}
		header := &tar.Header{Name: name, Mode: 0o600, Size: info.Size(), ModTime: info.ModTime()}
		if err = tarWriter.WriteHeader(header); err == nil {
			_, err = io.Copy(tarWriter, file)
		}
		_ = file.Close()
		if err != nil {
			return err
		}
	}
	if err = tarWriter.Close(); err != nil {
		return err
	}
	if err = encrypted.Close(); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if err = requireAbsent(output, "backup archive"); err != nil {
		return err
	}
	if err = os.Rename(temporaryName, output); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(output))
}

func RestoreEncryptedSnapshot(ctx context.Context, archive, dataDir string, key []byte) error {
	if len(key) != 32 {
		return errors.New("backup key must contain 32 bytes")
	}
	if err := os.MkdirAll(filepath.Dir(dataDir), 0o700); err != nil {
		return err
	}
	parent, err := os.MkdirTemp(filepath.Dir(dataDir), ".pocket-ai-gateway-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(parent)
	snapshot := filepath.Join(parent, "snapshot")
	if err = os.Mkdir(snapshot, 0o700); err != nil {
		return err
	}
	if err = decryptSnapshot(ctx, archive, snapshot, key); err != nil {
		return err
	}
	return RestoreSnapshot(ctx, snapshot, dataDir)
}

type archiveWriter struct {
	ctx     context.Context
	output  io.Writer
	aead    cipher.AEAD
	header  []byte
	buffer  []byte
	counter uint32
	closed  bool
}

func (writer *archiveWriter) Write(value []byte) (int, error) {
	if err := writer.ctx.Err(); err != nil {
		return 0, err
	}
	written := len(value)
	for len(value) > 0 {
		space := archiveChunk - len(writer.buffer)
		count := min(space, len(value))
		writer.buffer = append(writer.buffer, value[:count]...)
		value = value[count:]
		if len(writer.buffer) == archiveChunk {
			if err := writer.flush(false); err != nil {
				return written - len(value), err
			}
		}
	}
	return written, nil
}

func (writer *archiveWriter) Close() error {
	if writer.closed {
		return nil
	}
	writer.closed = true
	if len(writer.buffer) > 0 {
		if err := writer.flush(false); err != nil {
			return err
		}
	}
	return writer.flush(true)
}

func (writer *archiveWriter) flush(final bool) error {
	if err := writer.ctx.Err(); err != nil {
		return err
	}
	if writer.counter == ^uint32(0) {
		return errors.New("backup archive is too large")
	}
	nonce := make([]byte, writer.aead.NonceSize())
	copy(nonce, writer.header[len(archiveMagic):])
	binary.BigEndian.PutUint32(nonce[len(nonce)-4:], writer.counter)
	plain := writer.buffer
	if final {
		plain = nil
	}
	ciphertext := writer.aead.Seal(nil, nonce, plain, append(writer.header, byte(writer.counter>>24), byte(writer.counter>>16), byte(writer.counter>>8), byte(writer.counter)))
	if err := binary.Write(writer.output, binary.BigEndian, uint32(len(ciphertext))); err != nil {
		return err
	}
	if _, err := writer.output.Write(ciphertext); err != nil {
		return err
	}
	writer.counter++
	writer.buffer = writer.buffer[:0]
	return nil
}

func decryptSnapshot(ctx context.Context, archive, destination string, key []byte) error {
	file, info, err := openRegularFile(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	if info.Size() < int64(len(archiveMagic)+8+4+16) {
		return errors.New("backup archive is truncated")
	}
	header := make([]byte, len(archiveMagic)+8)
	if _, err = io.ReadFull(file, header); err != nil || string(header[:len(archiveMagic)]) != archiveMagic {
		return errors.New("backup archive header is invalid")
	}
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	reader := &archiveReader{ctx: ctx, input: file, aead: aead, header: header}
	tarReader := tar.NewReader(reader)
	seen := map[string]bool{}
	var extracted int64
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		if seen[header.Name] || (header.Name != "manifest.json" && header.Name != "system.db" && header.Name != "data.db" && header.Name != "master.key") || !header.FileInfo().Mode().IsRegular() || header.Size < 0 || header.Size > info.Size()-extracted {
			return fmt.Errorf("unsafe backup archive entry %q", header.Name)
		}
		extracted += header.Size
		seen[header.Name] = true
		output, createErr := os.OpenFile(filepath.Join(destination, header.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			return createErr
		}
		_, copyErr := io.CopyN(output, tarReader, header.Size)
		closeErr := output.Close()
		if copyErr != nil || closeErr != nil {
			return errors.Join(copyErr, closeErr)
		}
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return err
	}
	if !reader.finished || !seen["manifest.json"] || !seen["system.db"] || !seen["data.db"] {
		return errors.New("backup archive is incomplete")
	}
	var trailing [1]byte
	if count, err := file.Read(trailing[:]); count != 0 || !errors.Is(err, io.EOF) {
		return errors.New("backup archive contains trailing data")
	}
	return nil
}

type archiveReader struct {
	ctx      context.Context
	input    io.Reader
	aead     cipher.AEAD
	header   []byte
	plain    []byte
	counter  uint32
	finished bool
}

func (reader *archiveReader) Read(output []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if len(reader.plain) > 0 {
		count := copy(output, reader.plain)
		reader.plain = reader.plain[count:]
		return count, nil
	}
	if reader.finished {
		return 0, io.EOF
	}
	var size uint32
	if err := binary.Read(reader.input, binary.BigEndian, &size); err != nil || size < uint32(reader.aead.Overhead()) || size > archiveChunk+uint32(reader.aead.Overhead()) {
		return 0, errors.New("backup archive record is invalid")
	}
	ciphertext := make([]byte, size)
	if _, err := io.ReadFull(reader.input, ciphertext); err != nil {
		return 0, errors.New("backup archive is truncated")
	}
	nonce := make([]byte, reader.aead.NonceSize())
	copy(nonce, reader.header[len(archiveMagic):])
	binary.BigEndian.PutUint32(nonce[len(nonce)-4:], reader.counter)
	plain, err := reader.aead.Open(nil, nonce, ciphertext, append(reader.header, byte(reader.counter>>24), byte(reader.counter>>16), byte(reader.counter>>8), byte(reader.counter)))
	if err != nil {
		return 0, errors.New("backup archive authentication failed")
	}
	reader.counter++
	if len(plain) == 0 {
		reader.finished = true
		return 0, io.EOF
	}
	reader.plain = plain
	return reader.Read(output)
}
