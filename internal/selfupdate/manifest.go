package selfupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	manifestLimit = 1 << 20
	artifactLimit = 256 << 20
)

type Manifest struct {
	SchemaVersion int        `json:"schema_version"`
	Version       string     `json:"version"`
	Artifacts     []Artifact `json:"artifacts"`
}

type Artifact struct {
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	URL            string `json:"url"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	ReleaseVersion string `json:"-"`
}

type Options struct {
	CurrentVersion   string
	ManifestURL      string
	SignatureURL     string
	TrustedPublicKey string
	Executable       string
	DataDir          string
	Apply            bool
	AllowDowngrade   bool
	Client           *http.Client

	goos   string
	goarch string
	probe  func(context.Context, string, string) error
}

type Plan struct {
	CurrentVersion string `json:"current_version"`
	TargetVersion  string `json:"target_version"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	ArtifactURL    string `json:"artifact_url"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size_bytes"`
	Executable     string `json:"executable"`
	DataDir        string `json:"data_dir"`
	DryRun         bool   `json:"dry_run"`
	Snapshot       string `json:"snapshot,omitempty"`
	PreviousBinary string `json:"previous_binary,omitempty"`
}

func resolve(ctx context.Context, options Options) (Artifact, error) {
	if err := requireHTTPS(options.ManifestURL); err != nil {
		return Artifact{}, fmt.Errorf("manifest URL: %w", err)
	}
	signatureURL := options.SignatureURL
	if signatureURL == "" {
		signatureURL = options.ManifestURL + ".sig"
	}
	if err := requireHTTPS(signatureURL); err != nil {
		return Artifact{}, fmt.Errorf("signature URL: %w", err)
	}
	publicKey, err := decodeBase64(options.TrustedPublicKey, ed25519.PublicKeySize, "trusted public key")
	if err != nil {
		return Artifact{}, err
	}
	client := options.Client
	if client == nil {
		client = secureHTTPClient()
	}
	manifestBytes, err := fetch(ctx, client, options.ManifestURL, manifestLimit)
	if err != nil {
		return Artifact{}, fmt.Errorf("download release manifest: %w", err)
	}
	signatureBytes, err := fetch(ctx, client, signatureURL, 4096)
	if err != nil {
		return Artifact{}, fmt.Errorf("download release signature: %w", err)
	}
	signature, err := decodeBase64(strings.TrimSpace(string(signatureBytes)), ed25519.SignatureSize, "release signature")
	if err != nil {
		return Artifact{}, err
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), manifestBytes, signature) {
		return Artifact{}, errors.New("release manifest signature is invalid")
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Artifact{}, fmt.Errorf("decode release manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Artifact{}, errors.New("release manifest contains trailing data")
	}
	if manifest.SchemaVersion != 1 || !ValidReleaseVersion(manifest.Version) {
		return Artifact{}, errors.New("release manifest has an unsupported schema or version")
	}
	if !options.AllowDowngrade {
		comparison, err := compareReleaseVersions(options.CurrentVersion, manifest.Version)
		if err != nil {
			return Artifact{}, err
		}
		if comparison >= 0 {
			return Artifact{}, fmt.Errorf("target version %s is not newer than %s", manifest.Version, options.CurrentVersion)
		}
	}
	var selected *Artifact
	for index := range manifest.Artifacts {
		item := &manifest.Artifacts[index]
		if item.OS != options.goos || item.Arch != options.goarch {
			continue
		}
		if selected != nil {
			return Artifact{}, errors.New("release manifest contains duplicate platform artifacts")
		}
		selected = item
	}
	if selected == nil {
		return Artifact{}, fmt.Errorf("release has no artifact for %s/%s", options.goos, options.goarch)
	}
	if err := validateArtifact(*selected); err != nil {
		return Artifact{}, err
	}
	selected.SHA256 = strings.ToLower(selected.SHA256)
	selected.ReleaseVersion = manifest.Version
	return *selected, nil
}

func validateArtifact(artifact Artifact) error {
	if artifact.OS != "linux" || (artifact.Arch != "amd64" && artifact.Arch != "arm64") {
		return errors.New("release artifact platform is unsupported")
	}
	if err := requireHTTPS(artifact.URL); err != nil {
		return fmt.Errorf("artifact URL: %w", err)
	}
	checksum, err := hex.DecodeString(artifact.SHA256)
	if err != nil || len(checksum) != 32 {
		return errors.New("release artifact SHA-256 is invalid")
	}
	if artifact.Size < 1 || artifact.Size > artifactLimit {
		return errors.New("release artifact size is invalid")
	}
	return nil
}

func fetch(ctx context.Context, client *http.Client, address string, maximum int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "pocket-ai-gateway-updater")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	if response.ContentLength > maximum {
		return nil, errors.New("response exceeds the allowed size")
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maximum {
		return nil, errors.New("response exceeds the allowed size")
	}
	return content, nil
}

func secureHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(request *http.Request, previous []*http.Request) error {
			if len(previous) >= 5 {
				return errors.New("too many redirects")
			}
			return requireHTTPS(request.URL.String())
		},
	}
}

func requireHTTPS(address string) error {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("must be an HTTPS URL without credentials or a fragment")
	}
	return nil
}

func decodeBase64(value string, size int, label string) ([]byte, error) {
	value = strings.TrimSpace(value)
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(value)
	}
	if err != nil || len(decoded) != size {
		return nil, fmt.Errorf("%s must be base64-encoded %d bytes", label, size)
	}
	return decoded, nil
}

func ValidReleaseVersion(value string) bool {
	_, err := releaseVersion(value)
	return err == nil
}

func compareReleaseVersions(current, target string) (int, error) {
	left, err := releaseVersion(current)
	if err != nil {
		return 0, fmt.Errorf("current version %q is not a release version", current)
	}
	right, err := releaseVersion(target)
	if err != nil {
		return 0, fmt.Errorf("target version %q is invalid", target)
	}
	for index := range left {
		if left[index] < right[index] {
			return -1, nil
		}
		if left[index] > right[index] {
			return 1, nil
		}
	}
	return 0, nil
}

func releaseVersion(value string) ([3]uint64, error) {
	var result [3]uint64
	if !strings.HasPrefix(value, "v") || strings.ContainsAny(value, "+-") {
		return result, errors.New("release version must use vMAJOR.MINOR.PATCH")
	}
	parts := strings.Split(strings.TrimPrefix(value, "v"), ".")
	if len(parts) != len(result) {
		return result, errors.New("release version must use vMAJOR.MINOR.PATCH")
	}
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return result, errors.New("release version contains an invalid number")
		}
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return result, errors.New("release version contains an invalid number")
		}
		result[index] = number
	}
	return result, nil
}
