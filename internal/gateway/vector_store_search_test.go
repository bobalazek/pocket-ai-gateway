package gateway

import (
	"errors"
	"testing"
)

func TestVectorStoreSearchValidationFilteringAndRanking(t *testing.T) {
	options, err := parseVectorStoreSearch([]byte(`{"query":["gateway","notes"],"filters":{"type":"and","filters":[{"type":"eq","key":"kind","value":"docs"},{"type":"gte","key":"priority","value":2}]},"max_num_results":5,"ranking_options":{"ranker":"none","score_threshold":0.25}}`))
	if err != nil || options.limit != 5 || options.threshold != 0.25 || options.filter == nil {
		t.Fatalf("options=%#v error=%v", options, err)
	}
	if !options.filter.matches(map[string]any{"kind": "docs", "priority": float64(3)}) || options.filter.matches(map[string]any{"kind": "docs", "priority": float64(1)}) {
		t.Fatal("compound attribute filter returned the wrong result")
	}
	matching := lexicalCosine(searchTermFrequency("gateway notes"), searchTermFrequency("notes for the gateway"))
	unrelated := lexicalCosine(searchTermFrequency("gateway notes"), searchTermFrequency("weather forecast"))
	if matching <= unrelated || matching <= 0 || unrelated != 0 {
		t.Fatalf("matching=%f unrelated=%f", matching, unrelated)
	}
	if _, err := parseVectorStoreSearch([]byte(`{"query":"gateway","rewrite_query":true}`)); !errors.Is(err, errVectorStoreSearchRewrite) {
		t.Fatalf("rewrite error=%v", err)
	}
	if _, err := parseVectorStoreSearch([]byte(`{"query":"gateway","ranking_options":{"ranker":"auto"}}`)); !errors.Is(err, errVectorStoreSemanticRanker) {
		t.Fatalf("ranker error=%v", err)
	}
	for _, body := range []string{`{}`, `{"query":""}`, `{"query":"🙂"}`, `{"query":"gateway","max_num_results":0}`, `{"query":"gateway","max_num_results":null}`, `{"query":"gateway","filters":{"type":"eq","key":"kind","value":null}}`} {
		if _, err := parseVectorStoreSearch([]byte(body)); err == nil {
			t.Fatalf("accepted invalid body %s", body)
		}
	}
}
