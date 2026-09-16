package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobalazek/pocket-ai-gateway/internal/selfupdate"
)

func main() {
	version := flag.String("version", "", "release version in vMAJOR.MINOR.PATCH form")
	baseURL := flag.String("base-url", "", "HTTPS release download root")
	directory := flag.String("directory", "dist/release", "release artifact directory")
	printPublicKey := flag.Bool("print-public-key", false, "print the public key for the configured signing key and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		fail(errors.New("unexpected positional arguments"))
	}
	privateKey, err := signingKey(os.Getenv("POCKET_AI_GATEWAY_RELEASE_SIGNING_KEY"))
	if err != nil {
		fail(err)
	}
	if *printPublicKey {
		fmt.Println(base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)))
		return
	}
	if !selfupdate.ValidReleaseVersion(*version) {
		fail(errors.New("version must use vMAJOR.MINOR.PATCH"))
	}
	parsed, err := url.Parse(*baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		fail(errors.New("base-url must be an HTTPS URL without credentials, query, or fragment"))
	}
	manifest := selfupdate.Manifest{SchemaVersion: 1, Version: *version}
	for _, arch := range []string{"amd64", "arm64"} {
		name := "pocket-ai-gateway-" + strings.TrimPrefix(*version, "v") + "-linux-" + arch
		content, err := os.ReadFile(filepath.Join(*directory, name))
		if err != nil {
			fail(fmt.Errorf("read %s: %w", name, err))
		}
		sum := sha256.Sum256(content)
		manifest.Artifacts = append(manifest.Artifacts, selfupdate.Artifact{OS: "linux", Arch: arch, URL: strings.TrimRight(*baseURL, "/") + "/" + name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(content))})
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		fail(err)
	}
	encoded = append(encoded, '\n')
	signature := ed25519.Sign(privateKey, encoded)
	if err := os.WriteFile(filepath.Join(*directory, "release-manifest.json"), encoded, 0o644); err != nil {
		fail(err)
	}
	if err := os.WriteFile(filepath.Join(*directory, "release-manifest.json.sig"), []byte(base64.StdEncoding.EncodeToString(signature)+"\n"), 0o644); err != nil {
		fail(err)
	}
}

func signingKey(value string) (ed25519.PrivateKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(value))
	}
	if err != nil {
		return nil, errors.New("POCKET_AI_GATEWAY_RELEASE_SIGNING_KEY must be base64")
	}
	if len(decoded) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(decoded), nil
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("POCKET_AI_GATEWAY_RELEASE_SIGNING_KEY must contain %d-byte seed or %d-byte private key", ed25519.SeedSize, ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(decoded), nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
