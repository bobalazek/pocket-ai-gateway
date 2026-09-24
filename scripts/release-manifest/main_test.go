package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/selfupdate"
)

func TestVerifyRelease(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	manifest := selfupdate.Manifest{SchemaVersion: 1, Version: "v1.2.3"}
	for _, arch := range []string{"amd64", "arm64"} {
		name := "pocket-ai-gateway-1.2.3-linux-" + arch
		content := []byte("binary-" + arch)
		if err := os.WriteFile(filepath.Join(directory, name), content, 0o755); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		manifest.Artifacts = append(manifest.Artifacts, selfupdate.Artifact{OS: "linux", Arch: arch, URL: "https://example.test/releases/v1.2.3/" + name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(content))})
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(directory, "release-manifest.json")
	signaturePath := manifestPath + ".sig"
	if err := os.WriteFile(manifestPath, manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signaturePath, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, manifestBytes))), 0o644); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(publicKey)
	if err := verifyRelease(directory, "v1.2.3", key); err != nil {
		t.Fatal(err)
	}
	if err := verifyRelease(directory, "v1.2.4", key); err == nil {
		t.Fatal("accepted a different release version")
	}
	if err := os.WriteFile(filepath.Join(directory, "pocket-ai-gateway-1.2.3-linux-arm64"), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifyRelease(directory, "v1.2.3", key); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered binary: %v", err)
	}
	if err := os.WriteFile(signaturePath, []byte(base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyRelease(directory, "v1.2.3", key); err == nil || !strings.Contains(err.Error(), "signature is invalid") {
		t.Fatalf("tampered signature: %v", err)
	}
}

func TestSigningKeyRejectsInconsistentPrivateKey(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateKey[len(privateKey)-1] ^= 1
	if _, err := signingKey(base64.StdEncoding.EncodeToString(privateKey)); err == nil {
		t.Fatal("accepted an inconsistent private key")
	}
}
