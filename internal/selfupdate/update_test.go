package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestSignedLinuxUpdateDryRunApplyAndRollback(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	newBinary := []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo v1.1.0; exit 0; fi\nexit 1\n")
	manifest := Manifest{SchemaVersion: 1, Version: "v1.1.0", Artifacts: []Artifact{{OS: "linux", Arch: "amd64", SHA256: checksum(newBinary), Size: int64(len(newBinary))}}}
	var manifestBytes, signature []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/release.json":
			_, _ = response.Write(manifestBytes)
		case "/release.json.sig":
			_, _ = response.Write([]byte(base64.StdEncoding.EncodeToString(signature)))
		case "/gateway":
			_, _ = response.Write(newBinary)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	manifest.Artifacts[0].URL = server.URL + "/gateway"
	manifestBytes, _ = json.Marshal(manifest)
	signature = ed25519.Sign(privateKey, manifestBytes)

	root := t.TempDir()
	executable := filepath.Join(root, "pocket-ai-gateway")
	oldBinary := []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo v1.0.0; exit 0; fi\nexit 1\n")
	if err := os.WriteFile(executable, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, "data")
	store, err := storage.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().Exec(`INSERT INTO gateway_metadata(key,value) VALUES('update-test','preserved')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	base := Options{CurrentVersion: "v1.0.0", ManifestURL: server.URL + "/release.json", TrustedPublicKey: base64.StdEncoding.EncodeToString(publicKey), Executable: executable, DataDir: dataDir, Client: server.Client(), goos: "linux", goarch: "amd64"}

	dryRun, err := Run(context.Background(), base)
	if err != nil || !dryRun.DryRun || dryRun.TargetVersion != "v1.1.0" {
		t.Fatalf("dry run = %#v, %v", dryRun, err)
	}
	if content, _ := os.ReadFile(executable); string(content) != string(oldBinary) {
		t.Fatal("dry run changed the executable")
	}

	apply := base
	apply.Apply = true
	apply.probe = func(context.Context, string, string) error { return nil }
	result, err := Run(context.Background(), apply)
	if err != nil || result.DryRun || result.Snapshot == "" || !strings.HasPrefix(result.PreviousBinary, executable+".previous-") {
		t.Fatalf("apply = %#v, %v", result, err)
	}
	if content, _ := os.ReadFile(executable); string(content) != string(newBinary) {
		t.Fatal("updated executable was not installed")
	}
	if content, _ := os.ReadFile(result.PreviousBinary); string(content) != string(oldBinary) {
		t.Fatal("previous executable was not retained")
	}

	if err := os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(result.PreviousBinary, executable); err != nil {
		t.Fatal(err)
	}
	failed := base
	failed.Apply = true
	failed.probe = func(_ context.Context, _ string, directory string) error {
		if err := os.WriteFile(filepath.Join(directory, "new-only"), []byte("failed"), 0o600); err != nil {
			t.Fatal(err)
		}
		return errors.New("probe failed")
	}
	if _, err := Run(context.Background(), failed); err == nil || !strings.Contains(err.Error(), "probe failed") {
		t.Fatalf("rollback error = %v", err)
	}
	if content, _ := os.ReadFile(executable); string(content) != string(oldBinary) {
		t.Fatal("failed update did not restore the previous executable")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "new-only")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed update data survived rollback: %v", err)
	}
	restored, err := storage.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var value string
	if err := restored.SystemDB().QueryRow(`SELECT value FROM gateway_metadata WHERE key='update-test'`).Scan(&value); err != nil || value != "preserved" {
		t.Fatalf("restored metadata = %q, %v", value, err)
	}
}

func TestSignedUpdateRejectsTamperingAndNonUpgrade(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"schema_version":1,"version":"v1.0.0","artifacts":[]}`)
	signature := ed25519.Sign(privateKey, manifest)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, ".sig") {
			_, _ = response.Write([]byte(base64.StdEncoding.EncodeToString(signature)))
			return
		}
		_, _ = response.Write(manifest)
	}))
	defer server.Close()
	options := Options{CurrentVersion: "v1.0.0", ManifestURL: server.URL + "/release.json", TrustedPublicKey: base64.StdEncoding.EncodeToString(publicKey), Client: server.Client(), goos: "linux", goarch: "amd64"}
	if _, err := resolve(context.Background(), options); err == nil || !strings.Contains(err.Error(), "not newer") {
		t.Fatalf("non-upgrade error = %v", err)
	}
	options.TrustedPublicKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))
	if _, err := resolve(context.Background(), options); err == nil || !strings.Contains(err.Error(), "signature is invalid") {
		t.Fatalf("signature error = %v", err)
	}
}

func TestStageArtifactRejectsChecksumMismatch(t *testing.T) {
	content := []byte("tampered")
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(content)
	}))
	defer server.Close()
	artifact := Artifact{URL: server.URL, Size: int64(len(content)), SHA256: strings.Repeat("0", 64)}
	if _, err := stageArtifact(context.Background(), server.Client(), artifact, t.TempDir()); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("checksum error = %v", err)
	}
}

func TestProbeReadyStartsChecksAndStopsServer(t *testing.T) {
	wrapper := filepath.Join(t.TempDir(), "gateway")
	script := "#!/bin/sh\nGO_WANT_SELFUPDATE_HELPER=1 exec " + shellQuote(os.Args[0]) + " -test.run '^TestSelfUpdateProbeHelper$' -- \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := probeReady(context.Background(), wrapper, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestSelfUpdateProbeHelper(t *testing.T) {
	if os.Getenv("GO_WANT_SELFUPDATE_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 4 || args[1] != "serve" || args[2] != "--listen" {
		t.Fatalf("helper arguments = %q", os.Args)
	}
	server := &http.Server{Addr: args[3], Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/readyz" {
			http.NotFound(response, request)
			return
		}
		response.WriteHeader(http.StatusOK)
	})}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	stopped := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		close(stopped)
	}()
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
	<-stopped
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func checksum(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
