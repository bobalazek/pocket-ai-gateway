//go:build linux && e2e

package selfupdate_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/selfupdate"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

// This opt-in gate exercises the real Linux CLI and atomic executable exchange.
// Ordinary selfupdate tests use scripts and injected probes for faster feedback.
func TestLinuxCLIUpdateAndRollback(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	installed := filepath.Join(directory, "gateway")
	build := func(target, version, output string) []byte {
		t.Helper()
		command := exec.CommandContext(t.Context(), "go", "build", "-buildvcs=false", "-trimpath", "-ldflags=-X=main.version="+version, "-o", output, target)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", target, err, output)
		}
		content, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		return content
	}
	oldBinary := build("./cmd/pocket-ai-gateway", "v1.0.0", installed)
	newBinary := build("./cmd/pocket-ai-gateway", "v1.1.0", filepath.Join(directory, "candidate"))
	brokenBinary := build("./internal/integration/selfupdate/testdata/broken", "v1.2.0", filepath.Join(directory, "broken"))

	dataDir := filepath.Join(directory, "data")
	store, err := storage.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	_, systemErr := store.SystemDB().Exec(`INSERT INTO gateway_metadata(key,value) VALUES('cli-e2e','preserved')`)
	_, dataErr := store.DataDB().Exec(`INSERT INTO projection_metadata(key,value) VALUES('cli-e2e','preserved')`)
	closeErr := store.Close()
	if systemErr != nil || dataErr != nil || closeErr != nil {
		t.Fatalf("seed stores: %v / %v / %v", systemErr, dataErr, closeErr)
	}
	masterKey, err := providers.LoadOrCreateMasterKey(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	type release struct{ manifest, signature, binary []byte }
	var current atomic.Pointer[release]
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := current.Load()
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write(value.manifest)
		case "/manifest.json.sig":
			_, _ = w.Write(value.signature)
		case "/gateway":
			_, _ = w.Write(value.binary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	certificate := filepath.Join(directory, "test-ca.pem")
	if err := os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	publish := func(version string, binary []byte) {
		t.Helper()
		sum := sha256.Sum256(binary)
		manifest, err := json.Marshal(selfupdate.Manifest{SchemaVersion: 1, Version: version, Artifacts: []selfupdate.Artifact{{OS: "linux", Arch: runtime.GOARCH, URL: upstream.URL + "/gateway", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(binary))}}})
		if err != nil {
			t.Fatal(err)
		}
		current.Store(&release{manifest, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, manifest))), binary})
	}
	publish("v1.1.0", newBinary)
	var environment []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "POCKET_AI_GATEWAY_") && !strings.HasPrefix(entry, "SSL_CERT_FILE=") {
			environment = append(environment, entry)
		}
	}
	// A deployed service normally has a public origin. Its temporary readiness
	// probe must still work on loopback without changing that configuration.
	environment = append(environment, "SSL_CERT_FILE="+certificate, "POCKET_AI_GATEWAY_PUBLIC_URL=https://gateway.example.test")
	run := func(args ...string) ([]byte, error) {
		t.Helper()
		command := exec.CommandContext(t.Context(), installed, args...)
		command.Env = environment
		return command.CombinedOutput()
	}
	args := []string{"update", "--manifest-url", upstream.URL + "/manifest.json", "--signature-url", upstream.URL + "/manifest.json.sig", "--trusted-public-key", base64.StdEncoding.EncodeToString(publicKey), "--data-dir", dataDir}
	checkBinary := func(path string, expected []byte) {
		t.Helper()
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("executable mismatch at %s: %v", path, err)
		}
	}
	output, err := run(args...)
	var plan selfupdate.Plan
	if err != nil || json.Unmarshal(output, &plan) != nil || !plan.DryRun || plan.TargetVersion != "v1.1.0" {
		t.Fatalf("dry run: %v\n%s", err, output)
	}
	checkBinary(installed, oldBinary)
	checkData(t, dataDir, "preserved", masterKey)

	output, err = run(append(args, "--apply")...)
	if err != nil || json.Unmarshal(output, &plan) != nil || plan.DryRun || plan.Snapshot == "" {
		t.Fatalf("apply: %v\n%s", err, output)
	}
	checkBinary(installed, newBinary)
	checkBinary(plan.PreviousBinary, oldBinary)
	checkData(t, dataDir, "preserved", masterKey)

	publish("v1.2.0", brokenBinary)
	output, err = run(append(args, "--apply")...)
	if err == nil || !strings.Contains(string(output), "forced readiness failure") {
		t.Fatalf("expected failed candidate: %v\n%s", err, output)
	}
	checkBinary(installed, newBinary)
	checkData(t, dataDir, "preserved", masterKey)
	failed, err := filepath.Glob(filepath.Join(directory, ".data.failed-update-*"))
	if err != nil || len(failed) != 1 {
		t.Fatalf("failed data quarantine: %v / %v", failed, err)
	}
	checkData(t, failed[0], "broken-release", bytes.Repeat([]byte{42}, 32))
	output, err = run("version")
	if err != nil || strings.TrimSpace(string(output)) != "v1.1.0" {
		t.Fatalf("restored binary: %v\n%s", err, output)
	}
	// Reapplying the healthy artifact probes /readyz against the restored stores.
	publish("v1.1.0", newBinary)
	output, err = run(append(args, "--apply", "--allow-downgrade")...)
	if err != nil {
		t.Fatalf("restored instance readiness: %v\n%s", err, output)
	}
	checkData(t, dataDir, "preserved", masterKey)
}

func checkData(t *testing.T, directory, expected string, key []byte) {
	t.Helper()
	store, err := storage.Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var systemValue, dataValue string
	systemErr := store.SystemDB().QueryRow(`SELECT value FROM gateway_metadata WHERE key='cli-e2e'`).Scan(&systemValue)
	dataErr := store.DataDB().QueryRow(`SELECT value FROM projection_metadata WHERE key='cli-e2e'`).Scan(&dataValue)
	if systemErr != nil || dataErr != nil || systemValue != expected || dataValue != expected {
		t.Fatalf("paired data: %q / %q / %v / %v", systemValue, dataValue, systemErr, dataErr)
	}
	actualKey, err := os.ReadFile(filepath.Join(directory, "master.key"))
	if err != nil || !bytes.Equal(actualKey, key) {
		t.Fatalf("master key was not preserved: %v", err)
	}
}
