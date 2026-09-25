package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobalazek/pocket-ai-gateway/internal/selfupdate"
)

func main() {
	version := flag.String("version", "", "release version in vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-PRERELEASE form")
	baseURL := flag.String("base-url", "", "HTTPS release download root")
	directory := flag.String("directory", "dist/release", "release artifact directory")
	printPublicKey := flag.Bool("print-public-key", false, "print the public key for the configured signing key and exit")
	verify := flag.Bool("verify", false, "verify the signed manifest and Linux binaries without a signing key")
	publicKey := flag.String("public-key", "", "base64 Ed25519 public key for --verify")
	flag.Parse()
	if flag.NArg() != 0 {
		fail(errors.New("unexpected positional arguments"))
	}
	if *verify {
		if err := verifyRelease(*directory, *version, *publicKey); err != nil {
			fail(err)
		}
		return
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
		fail(errors.New("version must use vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-PRERELEASE"))
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
	if !bytes.Equal(decoded, ed25519.NewKeyFromSeed(decoded[:ed25519.SeedSize])) {
		return nil, errors.New("POCKET_AI_GATEWAY_RELEASE_SIGNING_KEY contains an invalid Ed25519 private key")
	}
	return ed25519.PrivateKey(decoded), nil
}

func verifyRelease(directory, version, encodedPublicKey string) error {
	if !selfupdate.ValidReleaseVersion(version) {
		return errors.New("version must use vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-PRERELEASE")
	}
	publicKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedPublicKey))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("public-key must be a base64-encoded Ed25519 public key")
	}
	manifestBytes, err := os.ReadFile(filepath.Join(directory, "release-manifest.json"))
	if err != nil {
		return err
	}
	signatureBytes, err := os.ReadFile(filepath.Join(directory, "release-manifest.json.sig"))
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signatureBytes)))
	if err != nil || !ed25519.Verify(publicKey, manifestBytes, signature) {
		return errors.New("release manifest signature is invalid")
	}
	var manifest selfupdate.Manifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("release manifest has trailing data")
	}
	if manifest.SchemaVersion != 1 || manifest.Version != version || len(manifest.Artifacts) != 2 {
		return errors.New("release manifest has an unexpected schema, version, or artifact count")
	}
	for index, arch := range []string{"amd64", "arm64"} {
		artifact := manifest.Artifacts[index]
		name := "pocket-ai-gateway-" + strings.TrimPrefix(version, "v") + "-linux-" + arch
		parsed, err := url.Parse(artifact.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.HasSuffix(parsed.Path, "/"+name) || artifact.OS != "linux" || artifact.Arch != arch {
			return fmt.Errorf("invalid %s artifact URL or platform", arch)
		}
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		if artifact.Size != int64(len(content)) || artifact.SHA256 != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("%s artifact size or checksum mismatch", arch)
		}
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
