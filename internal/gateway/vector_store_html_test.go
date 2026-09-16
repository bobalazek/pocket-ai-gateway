package gateway

import "testing"

func TestVectorStoreHTMLContentExtraction(t *testing.T) {
	content := []byte(`<!doctype html><html><head><title>Hidden title</title><style>.hidden { display: none }</style></head><body><main><h1>Pocket <em>AI</em> Gateway</h1><p>Routes&nbsp;requests <strong>safely</strong>.</p><script>ignore()</script><ul><li>First</li><li>Second</li></ul></main></body></html>`)
	chunks, err := vectorStoreContentChunks("DOCS.HTML", content)
	if err != nil || len(chunks) != 1 || chunks[0].Text != "Pocket AI Gateway\nRoutes requests safely.\nFirst\nSecond" {
		t.Fatalf("chunks=%#v error=%v", chunks, err)
	}
}
