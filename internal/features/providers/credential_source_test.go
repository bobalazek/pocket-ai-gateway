package providers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalCredentialSourcesRotateAndSelectBearerAuth(t *testing.T) {
	t.Setenv("PROVIDER_TEST_TOKEN", "first")
	credential, bearer, err := resolveExternalCredential("bearer-env:PROVIDER_TEST_TOKEN")
	if err != nil || credential != "first" || !bearer {
		t.Fatalf("environment credential=%q bearer=%v error=%v", credential, bearer, err)
	}

	filename := filepath.Join(t.TempDir(), "token")
	if err = os.WriteFile(filename, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	credential, bearer, err = resolveExternalCredential("file:" + filename)
	if err != nil || credential != "one" || bearer {
		t.Fatalf("file credential=%q bearer=%v error=%v", credential, bearer, err)
	}
	if err = os.WriteFile(filename, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	credential, bearer, err = resolveExternalCredential("bearer-file:" + filename)
	if err != nil || credential != "two" || !bearer {
		t.Fatalf("rotated credential=%q bearer=%v error=%v", credential, bearer, err)
	}

	for _, reference := range []string{"env:BAD-NAME", "file:relative", "bearer-env:", "bearer-file:relative"} {
		if validExternalRef(reference) {
			t.Fatalf("invalid reference %q accepted", reference)
		}
	}
	if err = os.WriteFile(filename, []byte("bad\nvalue"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = resolveExternalCredential("file:" + filename); err == nil {
		t.Fatal("multiline credential was accepted")
	}
	if err = os.WriteFile(filename, []byte(strings.Repeat("x", maxProviderCredentialBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = resolveExternalCredential("file:" + filename); err == nil {
		t.Fatal("oversized credential was accepted")
	}
}
