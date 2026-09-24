package gateway

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/giraffesyo/pdf/pdftest"
)

func TestOpenAIVectorStoreFileLifecycle(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Vector Store files", Scopes: []string{"files:manage", "vector_stores:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	_, otherSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Other", Scopes: []string{"files:manage", "vector_stores:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	_, deniedSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Denied", Scopes: []string{"files:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{8}, 32)).Register(mux)
	uploaded := performFileUpload(t, mux, secret, "notes.txt", []byte("gateway notes"), map[string]string{"purpose": "user_data"}, nil)
	var file openAIFile
	if uploaded.Code != http.StatusOK || json.Unmarshal(uploaded.Body.Bytes(), &file) != nil {
		t.Fatalf("upload status=%d body=%s", uploaded.Code, uploaded.Body.String())
	}
	created := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores", secret, `{"name":"Knowledge"}`)
	var storeItem vectorStore
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &storeItem) != nil {
		t.Fatalf("create store status=%d body=%s", created.Code, created.Body.String())
	}
	path := "/api/openai/v1/vector_stores/" + storeItem.ID + "/files"
	attached := performVectorStoreRequest(t, mux, http.MethodPost, path, secret, `{"file_id":"`+file.ID+`","attributes":{"kind":"docs","priority":2,"active":true},"chunking_strategy":{"type":"auto"}}`)
	var item vectorStoreFile
	if attached.Code != http.StatusOK || json.Unmarshal(attached.Body.Bytes(), &item) != nil || item.ID != file.ID || item.VectorStoreID != storeItem.ID || item.Status != "completed" || item.UsageBytes != file.Bytes || item.Attributes["kind"] != "docs" || item.ChunkingStrategy["type"] != "other" {
		t.Fatalf("attach status=%d body=%s", attached.Code, attached.Body.String())
	}
	if duplicate := performVectorStoreRequest(t, mux, http.MethodPost, path, secret, `{"file_id":"`+file.ID+`"}`); duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	if foreign := performVectorStoreRequest(t, mux, http.MethodPost, path, otherSecret, `{"file_id":"`+file.ID+`"}`); foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	if denied := performVectorStoreRequest(t, mux, http.MethodGet, path, deniedSecret, ""); denied.Code != http.StatusForbidden {
		t.Fatalf("denied status=%d body=%s", denied.Code, denied.Body.String())
	}
	if static := performVectorStoreRequest(t, mux, http.MethodPost, path, secret, `{"file_id":"missing","chunking_strategy":{"type":"static","static":{"max_chunk_size_tokens":800,"chunk_overlap_tokens":400}}}`); static.Code != http.StatusBadRequest || !strings.Contains(static.Body.String(), `"code":"unsupported_feature"`) {
		t.Fatalf("static status=%d body=%s", static.Code, static.Body.String())
	}
	if invalid := performVectorStoreRequest(t, mux, http.MethodPost, path, secret, `{"file_id":"missing","attributes":{"nested":{}}}`); invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	listed := performVectorStoreRequest(t, mux, http.MethodGet, path+"?filter=completed&limit=1", secret, "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), file.ID) || !strings.Contains(listed.Body.String(), `"has_more":false`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	resource := path + "/" + file.ID
	content := performVectorStoreRequest(t, mux, http.MethodGet, resource+"/content", secret, "")
	if content.Code != http.StatusOK || content.Header().Get("Cache-Control") != "no-store" || content.Body.String() != "{\"data\":[{\"type\":\"text\",\"text\":\"gateway notes\"}],\"object\":\"list\"}\n" {
		t.Fatalf("content status=%d body=%s", content.Code, content.Body.String())
	}
	if foreign := performVectorStoreRequest(t, mux, http.MethodGet, resource+"/content", otherSecret, ""); foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign content status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	updated := performVectorStoreRequest(t, mux, http.MethodPost, resource, secret, `{"attributes":{"kind":"updated"}}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"kind":"updated"`) {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	searched := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores/"+storeItem.ID+"/search", secret, `{"query":"gateway notes","filters":{"type":"eq","key":"kind","value":"updated"},"max_num_results":5,"ranking_options":{"ranker":"none","score_threshold":0.1}}`)
	var searchPage struct {
		Object string                    `json:"object"`
		Data   []vectorStoreSearchResult `json:"data"`
	}
	if searched.Code != http.StatusOK || json.Unmarshal(searched.Body.Bytes(), &searchPage) != nil || searchPage.Object != "vector_store.search_results.page" || len(searchPage.Data) != 1 || searchPage.Data[0].FileID != file.ID || searchPage.Data[0].Score <= 0 || searchPage.Data[0].Content[0].Text != "gateway notes" {
		t.Fatalf("search status=%d body=%s", searched.Code, searched.Body.String())
	}
	if foreign := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores/"+storeItem.ID+"/search", otherSecret, `{"query":"gateway"}`); foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign search status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	if rewritten := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores/"+storeItem.ID+"/search", secret, `{"query":"gateway","rewrite_query":true}`); rewritten.Code != http.StatusBadRequest || !strings.Contains(rewritten.Body.String(), `"code":"unsupported_feature"`) {
		t.Fatalf("rewrite status=%d body=%s", rewritten.Code, rewritten.Body.String())
	}
	storeResult := performVectorStoreRequest(t, mux, http.MethodGet, "/api/openai/v1/vector_stores/"+storeItem.ID, secret, "")
	var aggregate vectorStore
	if storeResult.Code != http.StatusOK || json.Unmarshal(storeResult.Body.Bytes(), &aggregate) != nil || aggregate.UsageBytes != file.Bytes || aggregate.FileCounts.Completed != 1 || aggregate.FileCounts.Total != 1 {
		t.Fatalf("aggregate status=%d body=%s", storeResult.Code, storeResult.Body.String())
	}
	deleted := performVectorStoreRequest(t, mux, http.MethodDelete, resource, secret, "")
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"object":"vector_store.file.deleted"`) {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if missing := performVectorStoreRequest(t, mux, http.MethodGet, resource, secret, ""); missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}
	if retained := performFileRequest(t, mux, http.MethodGet, "/api/openai/v1/files/"+file.ID, secret, nil, ""); retained.Code != http.StatusOK {
		t.Fatalf("source file status=%d body=%s", retained.Code, retained.Body.String())
	}
}

func TestVectorStoreTextChunksPreserveUTF8WithinBounds(t *testing.T) {
	input := []byte(strings.Repeat("a", maxVectorStoreContentChunkBytes-1) + "🙂" + strings.Repeat("b", maxVectorStoreContentChunkBytes))
	chunks, err := vectorStoreTextChunks(input)
	if err != nil || len(chunks) < 2 {
		t.Fatalf("chunks=%d error=%v", len(chunks), err)
	}
	var rebuilt strings.Builder
	for _, chunk := range chunks {
		if len(chunk.Text) > maxVectorStoreContentChunkBytes || !utf8.ValidString(chunk.Text) {
			t.Fatalf("invalid chunk size=%d", len(chunk.Text))
		}
		rebuilt.WriteString(chunk.Text)
	}
	if rebuilt.String() != string(input) {
		t.Fatal("chunks did not preserve source text")
	}
	if _, err := vectorStoreTextChunks([]byte{0xff}); err == nil {
		t.Fatal("binary content was accepted")
	}
}

func TestVectorStoreTextChunksDecodeUTF16(t *testing.T) {
	want := "Café 🙂\n"
	for name, order := range map[string]binary.ByteOrder{"little-endian": binary.LittleEndian, "big-endian": binary.BigEndian} {
		t.Run(name, func(t *testing.T) {
			chunks, err := vectorStoreTextChunks(vectorStoreTestUTF16(want, order))
			if err != nil || len(chunks) != 1 || chunks[0].Text != want {
				t.Fatalf("chunks=%#v error=%v", chunks, err)
			}
		})
	}
	if _, err := vectorStoreTextChunks([]byte{0xff, 0xfe, 0x00, 0xd8}); err == nil {
		t.Fatal("unpaired UTF-16 surrogate was accepted")
	}
	if _, err := vectorStoreTextChunks([]byte{0xff, 0xfe, 0x41}); err == nil {
		t.Fatal("incomplete UTF-16 code unit was accepted")
	}
}

func TestVectorStoreRejectsUnsupportedBinaryFormats(t *testing.T) {
	for _, test := range []struct {
		filename string
		content  []byte
	}{
		{"paper.pdf", []byte("plain text")},
		{"PAPER.PDF", []byte("plain text")},
		{"paper.txt", []byte("%PDF-1.7\nASCII body")},
		{"legacy.doc", []byte("plain text")},
		{"legacy.PPT", []byte("plain text")},
		{"legacy.xls", []byte("plain text")},
		{"disguised.txt", []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}},
	} {
		if _, err := vectorStoreContentChunks(context.Background(), test.filename, test.content); err == nil {
			t.Fatalf("%s was accepted", test.filename)
		}
	}
	chunks, err := vectorStoreContentChunks(context.Background(), "notes.unknown", []byte("ordinary UTF-8 text"))
	if err != nil || len(chunks) != 1 || chunks[0].Text != "ordinary UTF-8 text" {
		t.Fatalf("generic text chunks=%#v error=%v", chunks, err)
	}
	chunks, err = vectorStoreContentChunks(context.Background(), "pdf-notes.txt", []byte("PDF files begin with %PDF-1.7"))
	if err != nil || len(chunks) != 1 || chunks[0].Text != "PDF files begin with %PDF-1.7" {
		t.Fatalf("PDF documentation chunks=%#v error=%v", chunks, err)
	}
}

func TestVectorStorePDFContentExtraction(t *testing.T) {
	want := "Opening\n\nDetails"
	for _, filename := range []string{"paper.pdf", "disguised.txt"} {
		chunks, err := vectorStoreContentChunks(context.Background(), filename, vectorStoreTestPDF())
		if err != nil || len(chunks) != 1 || chunks[0].Text != want {
			t.Fatalf("%s chunks=%#v error=%v", filename, chunks, err)
		}
	}
	for name, content := range map[string][]byte{
		"malformed": []byte("%PDF-1.7\nnot a PDF"),
		"no text":   pdftest.Build(1, pdftest.Catalog(2), pdftest.Pages(3), pdftest.Page(2, 4, "<< >>"), pdftest.Stream("", "")),
		"oversized": bytes.Repeat([]byte("x"), maxFileBytes+1),
	} {
		if _, err := vectorStoreContentChunks(context.Background(), name+".pdf", content); err == nil {
			t.Fatalf("%s PDF was accepted", name)
		}
	}
	compressed := pdftest.Build(1, pdftest.Catalog(2), pdftest.Pages(3), pdftest.Page(2, 4, "<< >>"), pdftest.Flate("", strings.Repeat(" ", maxVectorStoreParsedContentBytes+1)))
	if _, err := vectorStoreContentChunks(context.Background(), "compressed.pdf", compressed); err == nil || !strings.Contains(strings.ToLower(err.Error()), "limit") {
		t.Fatalf("decompression-heavy PDF limit error=%v", err)
	}
	const pages = maxVectorStorePDFPages + 1
	ids := make([]int, pages)
	objects := []string{pdftest.Catalog(2), ""}
	for i := range ids {
		ids[i] = i + 3
		objects = append(objects, pdftest.Page(2, pages+3, "<< >>"))
	}
	objects[1] = pdftest.Pages(ids...)
	objects = append(objects, pdftest.Stream("", ""))
	if _, err := vectorStoreContentChunks(context.Background(), "many-pages.pdf", pdftest.Build(1, objects...)); err == nil || !strings.Contains(err.Error(), "256 pages") {
		t.Fatalf("page-limit error=%v", err)
	}
}

func vectorStoreTestPDF() []byte {
	resources := "<< /Font << /F1 7 0 R >> >>"
	return pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3, 4),
		pdftest.Page(2, 5, resources),
		pdftest.Page(2, 6, resources),
		pdftest.Stream("", "BT /F1 12 Tf 72 700 Td (Opening) Tj ET"),
		pdftest.Stream("", "BT /F1 12 Tf 72 700 Td (Details) Tj ET"),
		pdftest.Helvetica(),
	)
}

func vectorStoreTestPDFFacts() []byte {
	return pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", "BT /F1 12 Tf 72 700 Td (Pocket AI facts for gateway facts) Tj ET"),
		pdftest.Helvetica(),
	)
}

func TestOpenAIVectorStoreRejectsMalformedPDFContentAndSearch(t *testing.T) {
	ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
	defer store.Close()
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "PDF guard", Scopes: []string{"files:manage", "vector_stores:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{6}, 32)).Register(mux)
	uploaded := performFileUpload(t, mux, secret, "paper.txt", []byte("%PDF-1.7\nASCII body"), map[string]string{"purpose": "user_data"}, nil)
	var file openAIFile
	if uploaded.Code != http.StatusOK || json.Unmarshal(uploaded.Body.Bytes(), &file) != nil {
		t.Fatalf("upload status=%d body=%s", uploaded.Code, uploaded.Body.String())
	}
	created := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores", secret, `{"name":"PDF","file_ids":["`+file.ID+`"]}`)
	var storeItem vectorStore
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &storeItem) != nil {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	base := "/api/openai/v1/vector_stores/" + storeItem.ID
	content := performVectorStoreRequest(t, mux, http.MethodGet, base+"/files/"+file.ID+"/content", secret, "")
	if content.Code != http.StatusBadRequest || !strings.Contains(content.Body.String(), `"code":"unsupported_feature"`) {
		t.Fatalf("content status=%d body=%s", content.Code, content.Body.String())
	}
	searched := performVectorStoreRequest(t, mux, http.MethodPost, base+"/search", secret, `{"query":"ASCII"}`)
	if searched.Code != http.StatusBadRequest || !strings.Contains(searched.Body.String(), `"code":"unsupported_feature"`) {
		t.Fatalf("search status=%d body=%s", searched.Code, searched.Body.String())
	}
}

func TestVectorStoreDOCXContentExtraction(t *testing.T) {
	document := vectorStoreTestDOCX(t)
	chunks, err := vectorStoreContentChunks(context.Background(), "NOTES.DOCX", document)
	if err != nil || len(chunks) != 1 || chunks[0].Text != "First\tcell\nSecond\n" {
		t.Fatalf("chunks=%#v error=%v", chunks, err)
	}
	if _, err := vectorStoreContentChunks(context.Background(), "invalid.docx", []byte("not a zip")); err == nil {
		t.Fatal("invalid DOCX was accepted")
	}
}

func TestVectorStorePPTXContentExtraction(t *testing.T) {
	presentation := vectorStoreTestPPTX(t)
	chunks, err := vectorStoreContentChunks(context.Background(), "DECK.PPTX", presentation)
	if err != nil || len(chunks) != 1 || chunks[0].Text != "Opening\nDetails\tline\n" {
		t.Fatalf("chunks=%#v error=%v", chunks, err)
	}
	if _, err := vectorStoreContentChunks(context.Background(), "invalid.pptx", []byte("not a zip")); err == nil {
		t.Fatal("invalid PPTX was accepted")
	}
	if _, err := vectorStoreContentChunks(context.Background(), "traversal.pptx", vectorStoreTestPPTXTarget(t, "../outside.xml")); err == nil {
		t.Fatal("PPTX package traversal was accepted")
	}
}

func TestVectorStoreXLSXContentExtraction(t *testing.T) {
	workbook := vectorStoreTestXLSX(t)
	chunks, err := vectorStoreContentChunks(context.Background(), "SHEET.XLSX", workbook)
	if err != nil || len(chunks) != 1 || chunks[0].Text != "Opening\t42\nDetails\tGateway\nDone\n" {
		t.Fatalf("chunks=%#v error=%v", chunks, err)
	}
	if _, err := vectorStoreContentChunks(context.Background(), "invalid.xlsx", []byte("not a zip")); err == nil {
		t.Fatal("invalid XLSX was accepted")
	}
	if _, err := vectorStoreContentChunks(context.Background(), "traversal.xlsx", vectorStoreTestXLSXTarget(t, "../outside.xml")); err == nil {
		t.Fatal("XLSX package traversal was accepted")
	}
	if _, err := vectorStoreContentChunks(context.Background(), "duplicate.xlsx", vectorStoreTestXLSXPackage(t, "worksheets/sheet1.xml", "rId1")); err == nil {
		t.Fatal("duplicate XLSX relationship IDs were accepted")
	}
	if _, err := vectorStoreContentChunks(context.Background(), "oversized.xlsx", vectorStoreTestXLSXOversizedMetadata(t)); err == nil {
		t.Fatal("aggregate XLSX metadata over 16 MiB was accepted")
	}
}

func TestOpenAIVectorStoreParsedContentAndSearch(t *testing.T) {
	tests := []struct {
		name, filename, want, query string
		content                     func(*testing.T) []byte
	}{
		{name: "UTF-16", filename: "notes.txt", want: "Café gateway\n", query: "gateway", content: func(t *testing.T) []byte { return vectorStoreTestUTF16("Café gateway\n", binary.LittleEndian) }},
		{name: "PDF", filename: "paper.pdf", want: "Opening\n\nDetails", query: "details", content: func(*testing.T) []byte { return vectorStoreTestPDF() }},
		{name: "DOCX", filename: "notes.docx", want: "First\tcell\nSecond\n", query: "second", content: vectorStoreTestDOCX},
		{name: "PPTX", filename: "slides.pptx", want: "Opening\nDetails\tline\n", query: "details", content: vectorStoreTestPPTX},
		{name: "XLSX", filename: "sheet.xlsx", want: "Opening\t42\nDetails\tGateway\nDone\n", query: "gateway", content: vectorStoreTestXLSX},
		{name: "HTML", filename: "docs.html", want: "Pocket AI Gateway\nRoutes requests safely.", query: "safely", content: func(*testing.T) []byte {
			return []byte(`<main><h1>Pocket AI Gateway</h1><p>Routes requests safely.</p><script>ignore()</script></main>`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, store, owner, keyService, providerService, usageService := gatewayFixture(t)
			defer store.Close()
			_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: test.name + " search", Scopes: []string{"files:manage", "vector_stores:manage"}})
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			NewWithMasterKey(store.SystemDB(), keyService, providerService, usageService, bytes.Repeat([]byte{6}, 32)).Register(mux)
			uploaded := performFileUpload(t, mux, secret, test.filename, test.content(t), map[string]string{"purpose": "user_data"}, nil)
			var file openAIFile
			if uploaded.Code != http.StatusOK || json.Unmarshal(uploaded.Body.Bytes(), &file) != nil {
				t.Fatalf("upload status=%d body=%s", uploaded.Code, uploaded.Body.String())
			}
			created := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores", secret, `{"name":"`+test.name+`","file_ids":["`+file.ID+`"]}`)
			var item vectorStore
			if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &item) != nil {
				t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
			}
			content := performVectorStoreRequest(t, mux, http.MethodGet, "/api/openai/v1/vector_stores/"+item.ID+"/files/"+file.ID+"/content", secret, "")
			var page struct {
				Data []vectorStoreContent `json:"data"`
			}
			if content.Code != http.StatusOK || json.Unmarshal(content.Body.Bytes(), &page) != nil || len(page.Data) != 1 || page.Data[0].Text != test.want {
				t.Fatalf("content status=%d body=%s", content.Code, content.Body.String())
			}
			searched := performVectorStoreRequest(t, mux, http.MethodPost, "/api/openai/v1/vector_stores/"+item.ID+"/search", secret, `{"query":"`+test.query+`"}`)
			if searched.Code != http.StatusOK || !strings.Contains(searched.Body.String(), `"file_id":"`+file.ID+`"`) {
				t.Fatalf("search status=%d body=%s", searched.Code, searched.Body.String())
			}
		})
	}
}

func vectorStoreTestDOCX(t *testing.T) []byte {
	t.Helper()
	var document bytes.Buffer
	archive := zip.NewWriter(&document)
	entry, err := archive.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = entry.Write([]byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>First</w:t><w:tab/><w:t>cell</w:t></w:r></w:p><w:p><w:r><w:t>Second</w:t><w:br/></w:r></w:p></w:body></w:document>`))
	if err != nil || archive.Close() != nil {
		t.Fatal(err)
	}
	return document.Bytes()
}

func vectorStoreTestUTF16(text string, order binary.ByteOrder) []byte {
	units := utf16.Encode([]rune(text))
	content := make([]byte, 2+len(units)*2)
	if order == binary.LittleEndian {
		copy(content, []byte{0xff, 0xfe})
	} else {
		copy(content, []byte{0xfe, 0xff})
	}
	for index, unit := range units {
		order.PutUint16(content[2+index*2:], unit)
	}
	return content
}

func vectorStoreTestPPTX(t *testing.T) []byte {
	return vectorStoreTestPPTXTarget(t, "slides/slide1.xml")
}

func vectorStoreTestPPTXTarget(t *testing.T, firstTarget string) []byte {
	t.Helper()
	parts := map[string]string{
		"ppt/presentation.xml":            `<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:sldIdLst><p:sldId id="256" r:id="rId2"/><p:sldId id="257" r:id="rId1"/></p:sldIdLst></p:presentation>`,
		"ppt/_rels/presentation.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="` + firstTarget + `"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide2.xml"/></Relationships>`,
		"ppt/slides/slide1.xml":           `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><a:p><a:r><a:t>Details</a:t><a:tab/><a:t>line</a:t></a:r></a:p></p:cSld></p:sld>`,
		"ppt/slides/slide2.xml":           `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><a:p><a:r><a:t>Opening</a:t></a:r></a:p></p:cSld></p:sld>`,
	}
	var presentation bytes.Buffer
	archive := zip.NewWriter(&presentation)
	for name, body := range parts {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return presentation.Bytes()
}

func vectorStoreTestXLSX(t *testing.T) []byte {
	return vectorStoreTestXLSXTarget(t, "worksheets/sheet1.xml")
}

func vectorStoreTestXLSXTarget(t *testing.T, firstTarget string) []byte {
	return vectorStoreTestXLSXPackage(t, firstTarget, "rId3")
}

func vectorStoreTestXLSXPackage(t *testing.T, firstTarget, sharedID string) []byte {
	t.Helper()
	parts := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" sheetId="1" r:id="rId2"/><sheet name="Second" sheetId="2" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="` + firstTarget + `"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/><Relationship Id="` + sharedID + `" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="2" uniqueCount="2"><si><t>Details</t></si><si><r><t>Gate</t></r><r><t>way</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row><row r="2"><c r="A2" t="str"><v>Done</v></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml":   `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>Opening</t></is></c><c r="B1"><v>42</v></c></row></sheetData></worksheet>`,
	}
	var workbook bytes.Buffer
	archive := zip.NewWriter(&workbook)
	for name, body := range parts {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return workbook.Bytes()
}

func vectorStoreTestXLSXOversizedMetadata(t *testing.T) []byte {
	t.Helper()
	padding := strings.Repeat(" ", maxVectorStoreParsedContentBytes/2)
	parts := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` + padding + `</workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + padding + `</Relationships>`,
	}
	var workbook bytes.Buffer
	archive := zip.NewWriter(&workbook)
	for name, body := range parts {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return workbook.Bytes()
}
