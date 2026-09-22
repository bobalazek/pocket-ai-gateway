package operations

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestS3UploadPreservesAndEscapesObjectKey(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "test.pagbak")
	if err := os.WriteFile(filename, []byte("encrypted-backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ prefix, escaped string }{
		{"", "/bucket/test.pagbak"},
		{"nightly +%/č", "/bucket/nightly%20%2B%25/%C4%8D/test.pagbak"},
		{"a:b@c!$&'()*+,;=", "/bucket/a%3Ab%40c%21%24%26%27%28%29%2A%2B%2C%3B%3D/test.pagbak"},
		{"AZaz09-._~/literal%2F", "/bucket/AZaz09-._~/literal%252F/test.pagbak"},
		{"nested//prefix/../keep", "/bucket/nested//prefix/../keep/test.pagbak"},
	} {
		t.Run(test.prefix, func(t *testing.T) {
			previousClient := s3Client
			t.Cleanup(func() { s3Client = previousClient })
			s3Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if got := request.URL.EscapedPath(); got != test.escaped {
					t.Errorf("S3 request path = %q, want %q", got, test.escaped)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
			})}
			settings := Settings{S3Endpoint: "http://127.0.0.1:9000", S3Region: "us-east-1", S3Bucket: "bucket", S3Prefix: test.prefix, S3AccessKeyEnv: "ACCESS", S3SecretKeyEnv: "SECRET"}
			if err := uploadS3(context.Background(), filename, "test.pagbak", settings, func(string) string { return "test-credential" }); err != nil {
				t.Fatal(err)
			}
		})
	}
}
