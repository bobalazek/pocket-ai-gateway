package gateway

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type vectorStoreSearchOptions struct {
	queries   []string
	filter    *vectorStoreAttributeFilter
	limit     int
	threshold float64
}

type vectorStoreAttributeFilter struct {
	operator string
	key      string
	value    any
	children []vectorStoreAttributeFilter
}

type vectorStoreSearchFile struct {
	id         string
	filename   string
	attributes map[string]any
}

type vectorStoreSearchResult struct {
	FileID     string               `json:"file_id"`
	Filename   string               `json:"filename"`
	Score      float64              `json:"score"`
	Attributes map[string]any       `json:"attributes"`
	Content    []vectorStoreContent `json:"content"`
	ordinal    int
}

func (handler *Handler) searchVectorStore(response http.ResponseWriter, request *http.Request) {
	principal, ok := handler.vectorStorePrincipal(response, request)
	if !ok {
		return
	}
	body, err := readJSONBody(response, request)
	if err != nil {
		writeVectorStoreBodyError(handler, response, err)
		return
	}
	options, err := parseVectorStoreSearch(body)
	if err != nil {
		code := "invalid_request"
		if errors.Is(err, errVectorStoreSearchRewrite) || errors.Is(err, errVectorStoreSemanticRanker) {
			code = "unsupported_feature"
		}
		handler.writeError(response, "openai", http.StatusBadRequest, code, err.Error())
		return
	}
	storeID := request.PathValue("vector_store_id")
	if _, err := handler.readVectorStore(request.Context(), principal.KeyID, storeID, false); errors.Is(err, sql.ErrNoRows) {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Vector Store not found")
		return
	} else if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store search is unavailable")
		return
	}
	if !handler.acquireFileTransfer(response) {
		return
	}
	defer handler.releaseFileTransfer()
	files, err := handler.listVectorStoreSearchFiles(request, principal.KeyID, storeID, options.filter)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store search is unavailable")
		return
	}
	queryTerms := searchTermFrequency(strings.Join(options.queries, " "))
	results := make([]vectorStoreSearchResult, 0, options.limit)
	for _, file := range files {
		storedFile, content, loadErr := handler.loadOpenAIFileContent(request.Context(), principal.KeyID, file.id)
		if loadErr != nil {
			handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store search is unavailable")
			return
		}
		chunks, chunkErr := vectorStoreContentChunks(storedFile.Filename, content)
		if chunkErr != nil {
			handler.writeError(response, "openai", http.StatusBadRequest, "unsupported_feature", chunkErr.Error())
			return
		}
		for ordinal, chunk := range chunks {
			score := lexicalCosine(queryTerms, searchTermFrequency(chunk.Text))
			if score <= 0 || score < options.threshold {
				continue
			}
			results = append(results, vectorStoreSearchResult{FileID: file.id, Filename: file.filename, Score: score, Attributes: file.attributes, Content: []vectorStoreContent{chunk}, ordinal: ordinal})
			sort.Slice(results, func(left, right int) bool { return betterVectorStoreResult(results[left], results[right]) })
			if len(results) > options.limit {
				results = results[:options.limit]
			}
		}
	}
	now := time.Now().UnixMilli()
	updated, err := handler.database.ExecContext(request.Context(), `UPDATE openai_vector_stores SET last_active_at=?,expires_at=CASE WHEN expires_after_days IS NULL THEN NULL ELSE ?+expires_after_days*86400000 END WHERE id=? AND key_id=? AND (expires_at IS NULL OR expires_at>?)`, now, now, storeID, principal.KeyID, now)
	if err != nil {
		handler.writeError(response, "openai", http.StatusServiceUnavailable, "gateway_unavailable", "Vector Store search is unavailable")
		return
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		handler.writeError(response, "openai", http.StatusNotFound, "not_found", "Vector Store not found")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, map[string]any{"object": "vector_store.search_results.page", "search_query": strings.Join(options.queries, " "), "data": results, "has_more": false, "next_page": nil})
}

var (
	errVectorStoreSearchRewrite  = errors.New("query rewriting is not supported by local Vector Store search")
	errVectorStoreSemanticRanker = errors.New("embedding-backed Vector Store ranking is not supported")
)

func parseVectorStoreSearch(body []byte) (vectorStoreSearchOptions, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil || !onlyJSONFields(body, "query", "filters", "max_num_results", "ranking_options", "rewrite_query") {
		return vectorStoreSearchOptions{}, errors.New("request body must be a Vector Store search object")
	}
	queries, err := parseVectorStoreSearchQueries(fields["query"])
	if err != nil {
		return vectorStoreSearchOptions{}, err
	}
	if len(searchTermFrequency(strings.Join(queries, " "))) == 0 {
		return vectorStoreSearchOptions{}, errors.New("query must contain at least one letter or number")
	}
	options := vectorStoreSearchOptions{queries: queries, limit: 10}
	if raw := fields["filters"]; len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		filter, filterErr := parseVectorStoreAttributeFilter(raw, 0)
		if filterErr != nil {
			return vectorStoreSearchOptions{}, filterErr
		}
		options.filter = &filter
	}
	if raw := fields["max_num_results"]; len(raw) > 0 {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &options.limit) != nil {
			return vectorStoreSearchOptions{}, errors.New("max_num_results must be an integer between 1 and 50")
		}
	}
	if options.limit < 1 || options.limit > 50 {
		return vectorStoreSearchOptions{}, errors.New("max_num_results must be an integer between 1 and 50")
	}
	if raw := fields["rewrite_query"]; len(raw) > 0 {
		var rewrite bool
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &rewrite) != nil {
			return vectorStoreSearchOptions{}, errors.New("rewrite_query must be a boolean")
		}
		if rewrite {
			return vectorStoreSearchOptions{}, errVectorStoreSearchRewrite
		}
	}
	if raw := fields["ranking_options"]; len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var ranking map[string]json.RawMessage
		if json.Unmarshal(raw, &ranking) != nil || ranking == nil || !onlyJSONFields(raw, "ranker", "score_threshold") {
			return vectorStoreSearchOptions{}, errors.New("ranking_options is invalid")
		}
		if rankerRaw := ranking["ranker"]; len(rankerRaw) > 0 {
			var ranker string
			if json.Unmarshal(rankerRaw, &ranker) != nil || (ranker != "none" && ranker != "auto" && ranker != "default-2024-11-15") {
				return vectorStoreSearchOptions{}, errors.New("ranking_options.ranker is invalid")
			}
			if ranker != "none" {
				return vectorStoreSearchOptions{}, errVectorStoreSemanticRanker
			}
		}
		if thresholdRaw := ranking["score_threshold"]; len(thresholdRaw) > 0 {
			if bytes.Equal(bytes.TrimSpace(thresholdRaw), []byte("null")) || json.Unmarshal(thresholdRaw, &options.threshold) != nil || math.IsNaN(options.threshold) || math.IsInf(options.threshold, 0) || options.threshold < 0 || options.threshold > 1 {
				return vectorStoreSearchOptions{}, errors.New("ranking_options.score_threshold must be between 0 and 1")
			}
		}
	}
	return options, nil
}

func parseVectorStoreSearchQueries(raw json.RawMessage) ([]string, error) {
	var queries []string
	var single string
	if json.Unmarshal(raw, &single) == nil {
		queries = []string{single}
	} else if json.Unmarshal(raw, &queries) != nil || len(queries) == 0 || len(queries) > 16 {
		return nil, errors.New("query must be a string or an array of 1 to 16 strings")
	}
	for _, query := range queries {
		if strings.TrimSpace(query) == "" || len(query) > 4096 || !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
			return nil, errors.New("each query must be non-empty UTF-8 text of at most 4096 bytes")
		}
	}
	return queries, nil
}

func parseVectorStoreAttributeFilter(raw json.RawMessage, depth int) (vectorStoreAttributeFilter, error) {
	if depth > 4 {
		return vectorStoreAttributeFilter{}, errors.New("filters may be nested at most four levels")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return vectorStoreAttributeFilter{}, errors.New("filters must be an object")
	}
	var operator string
	if json.Unmarshal(fields["type"], &operator) != nil {
		return vectorStoreAttributeFilter{}, errors.New("filter type is required")
	}
	filter := vectorStoreAttributeFilter{operator: operator}
	if operator == "and" || operator == "or" {
		if !onlyJSONFields(raw, "type", "filters") {
			return filter, errors.New("compound filter fields are invalid")
		}
		var children []json.RawMessage
		if json.Unmarshal(fields["filters"], &children) != nil || len(children) == 0 || len(children) > 20 {
			return filter, errors.New("compound filters require 1 to 20 filters")
		}
		for _, child := range children {
			parsed, err := parseVectorStoreAttributeFilter(child, depth+1)
			if err != nil {
				return filter, err
			}
			filter.children = append(filter.children, parsed)
		}
		return filter, nil
	}
	if operator != "eq" && operator != "ne" && operator != "gt" && operator != "gte" && operator != "lt" && operator != "lte" && operator != "in" && operator != "nin" {
		return filter, errors.New("comparison filter type is invalid")
	}
	if !onlyJSONFields(raw, "type", "key", "value") || json.Unmarshal(fields["key"], &filter.key) != nil || filter.key == "" || utf8.RuneCountInString(filter.key) > 64 {
		return filter, errors.New("comparison filters require a key of at most 64 characters")
	}
	if json.Unmarshal(fields["value"], &filter.value) != nil || !validVectorStoreFilterValue(operator, filter.value) {
		return filter, errors.New("comparison filter value is invalid")
	}
	return filter, nil
}

func validVectorStoreFilterValue(operator string, value any) bool {
	if operator == "in" || operator == "nin" {
		items, ok := value.([]any)
		if !ok || len(items) == 0 || len(items) > 100 {
			return false
		}
		for _, item := range items {
			if !validVectorStoreFilterScalar(item, false) {
				return false
			}
		}
		return true
	}
	ordered := operator == "gt" || operator == "gte" || operator == "lt" || operator == "lte"
	return validVectorStoreFilterScalar(value, !ordered)
}

func validVectorStoreFilterScalar(value any, allowBool bool) bool {
	switch value := value.(type) {
	case string:
		return utf8.RuneCountInString(value) <= 512
	case float64:
		return !math.IsNaN(value) && !math.IsInf(value, 0)
	case bool:
		return allowBool
	default:
		return false
	}
}

func (handler *Handler) listVectorStoreSearchFiles(request *http.Request, keyID, storeID string, filter *vectorStoreAttributeFilter) ([]vectorStoreSearchFile, error) {
	rows, err := handler.database.QueryContext(request.Context(), `SELECT openai_vector_store_files.file_id,openai_files.filename,openai_vector_store_files.attributes_json
		FROM openai_vector_store_files
		JOIN openai_vector_stores ON openai_vector_stores.id=openai_vector_store_files.vector_store_id
		JOIN openai_files ON openai_files.id=openai_vector_store_files.file_id
		WHERE openai_vector_store_files.vector_store_id=? AND openai_vector_stores.key_id=? AND (openai_vector_stores.expires_at IS NULL OR openai_vector_stores.expires_at>?) AND openai_files.key_id=? AND openai_files.expires_at>? ORDER BY openai_vector_store_files.created_at,openai_vector_store_files.file_id`, storeID, keyID, time.Now().UnixMilli(), keyID, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []vectorStoreSearchFile
	for rows.Next() {
		var file vectorStoreSearchFile
		var attributes []byte
		if err := rows.Scan(&file.id, &file.filename, &attributes); err != nil {
			return nil, err
		}
		if json.Unmarshal(attributes, &file.attributes) != nil {
			return nil, errors.New("invalid Vector Store file attributes")
		}
		if filter == nil || filter.matches(file.attributes) {
			files = append(files, file)
		}
	}
	return files, rows.Err()
}

func (filter vectorStoreAttributeFilter) matches(attributes map[string]any) bool {
	if filter.operator == "and" {
		for _, child := range filter.children {
			if !child.matches(attributes) {
				return false
			}
		}
		return true
	}
	if filter.operator == "or" {
		for _, child := range filter.children {
			if child.matches(attributes) {
				return true
			}
		}
		return false
	}
	actual, exists := attributes[filter.key]
	if !exists {
		return false
	}
	if filter.operator == "in" || filter.operator == "nin" {
		found := false
		for _, expected := range filter.value.([]any) {
			found = found || equalFilterValue(actual, expected)
		}
		return found == (filter.operator == "in")
	}
	comparison, comparable := compareFilterValue(actual, filter.value)
	switch filter.operator {
	case "eq":
		return equalFilterValue(actual, filter.value)
	case "ne":
		return !equalFilterValue(actual, filter.value)
	case "gt":
		return comparable && comparison > 0
	case "gte":
		return comparable && comparison >= 0
	case "lt":
		return comparable && comparison < 0
	case "lte":
		return comparable && comparison <= 0
	default:
		return false
	}
}

func equalFilterValue(left, right any) bool {
	switch left := left.(type) {
	case string:
		right, ok := right.(string)
		return ok && left == right
	case float64:
		right, ok := right.(float64)
		return ok && left == right
	case bool:
		right, ok := right.(bool)
		return ok && left == right
	default:
		return false
	}
}

func compareFilterValue(left, right any) (int, bool) {
	switch left := left.(type) {
	case string:
		right, ok := right.(string)
		return strings.Compare(left, right), ok
	case float64:
		right, ok := right.(float64)
		if !ok {
			return 0, false
		}
		if left < right {
			return -1, true
		}
		if left > right {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func searchTermFrequency(value string) map[string]float64 {
	terms := map[string]float64{}
	for _, term := range strings.FieldsFunc(strings.ToLower(value), func(character rune) bool { return !unicode.IsLetter(character) && !unicode.IsNumber(character) }) {
		terms[term]++
	}
	return terms
}

func lexicalCosine(left, right map[string]float64) float64 {
	var dot, leftNorm, rightNorm float64
	for term, frequency := range left {
		dot += frequency * right[term]
		leftNorm += frequency * frequency
	}
	for _, frequency := range right {
		rightNorm += frequency * frequency
	}
	if dot == 0 || leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return min(1, dot/math.Sqrt(leftNorm*rightNorm))
}

func betterVectorStoreResult(left, right vectorStoreSearchResult) bool {
	if left.Score != right.Score {
		return left.Score > right.Score
	}
	if left.FileID != right.FileID {
		return left.FileID < right.FileID
	}
	return left.ordinal < right.ordinal
}
