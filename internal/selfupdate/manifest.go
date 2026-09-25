package selfupdate

import (
	"bytes"
	"cmp"
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
	for index := range left.core {
		if left.core[index] != right.core[index] {
			return cmp.Compare(left.core[index], right.core[index]), nil
		}
	}
	// SemVer precedence: a pre-release sorts before its release, then identifiers compare in order.
	if len(left.pre) == 0 || len(right.pre) == 0 {
		return cmp.Compare(len(right.pre), len(left.pre)), nil
	}
	for index := range min(len(left.pre), len(right.pre)) {
		if order := comparePrereleaseIdentifier(left.pre[index], right.pre[index]); order != 0 {
			return order, nil
		}
	}
	return cmp.Compare(len(left.pre), len(right.pre)), nil
}

func comparePrereleaseIdentifier(left, right string) int {
	leftNumber, leftErr := strconv.ParseUint(left, 10, 64)
	rightNumber, rightErr := strconv.ParseUint(right, 10, 64)
	switch {
	case leftErr == nil && rightErr == nil:
		return cmp.Compare(leftNumber, rightNumber)
	case leftErr == nil:
		return -1
	case rightErr == nil:
		return 1
	}
	return strings.Compare(left, right)
}

type parsedReleaseVersion struct {
	core [3]uint64
	pre  []string
}

// releaseVersion parses vMAJOR.MINOR.PATCH with an optional SemVer pre-release such as -alpha.1.
// Build metadata is rejected so each tag maps to exactly one release.
func releaseVersion(value string) (parsedReleaseVersion, error) {
	var result parsedReleaseVersion
	if !strings.HasPrefix(value, "v") || strings.Contains(value, "+") {
		return result, errors.New("release version must use vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-PRERELEASE")
	}
	core, prerelease, hasPrerelease := strings.Cut(strings.TrimPrefix(value, "v"), "-")
	parts := strings.Split(core, ".")
	if len(parts) != len(result.core) {
		return result, errors.New("release version must use vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-PRERELEASE")
	}
	for index, part := range parts {
		if !validNumericIdentifier(part) {
			return result, errors.New("release version contains an invalid number")
		}
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return result, errors.New("release version contains an invalid number")
		}
		result.core[index] = number
	}
	if !hasPrerelease {
		return result, nil
	}
	for _, identifier := range strings.Split(prerelease, ".") {
		if identifier == "" || strings.Trim(identifier, "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-") != "" {
			return result, errors.New("release version contains an invalid pre-release identifier")
		}
		if strings.Trim(identifier, "0123456789") == "" && !validNumericIdentifier(identifier) {
			return result, errors.New("release version contains an invalid pre-release identifier")
		}
		result.pre = append(result.pre, identifier)
	}
	return result, nil
}

func validNumericIdentifier(value string) bool {
	return value != "" && strings.Trim(value, "0123456789") == "" && (len(value) == 1 || value[0] != '0')
}
