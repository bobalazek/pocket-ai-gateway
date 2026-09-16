package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
)

var errResponseFileSearchProvider = errors.New("provider returned an invalid file-search response")

type responseFileSearchAccountedError struct{ cause error }

func (err *responseFileSearchAccountedError) Error() string { return err.cause.Error() }
func (err *responseFileSearchAccountedError) Unwrap() error { return err.cause }

const responseUsageMaximum = int64(9_007_199_254_740_991)

type responseFileSearchCall struct {
	ID      string                          `json:"id"`
	Type    string                          `json:"type"`
	Status  string                          `json:"status"`
	Queries []string                        `json:"queries"`
	Results *[]responseFileSearchResultItem `json:"results,omitempty"`
}

type responseFileSearchResultItem struct {
	Attributes map[string]any `json:"attributes,omitempty"`
	FileID     string         `json:"file_id"`
	Filename   string         `json:"filename"`
	Score      float64        `json:"score"`
	Text       string         `json:"text"`
}

func fileSearchTargetEligibility(target providers.Target) (bool, string) {
	if !providers.NativeTarget("responses", target.Adapter) {
		return false, "file_search_native_responses_required"
	}
	if !slices.Contains(target.Capabilities, "chat") || !slices.Contains(target.UpstreamCapabilities, "chat") {
		return false, "unsupported_capability"
	}
	return true, ""
}

func (handler *Handler) validateResponseFileSearchStores(ctx context.Context, keyID string, request responseFileSearchRequest) error {
	for _, id := range request.vectorStoreIDs {
		if _, err := handler.readVectorStore(ctx, keyID, id, false); err != nil {
			return err
		}
	}
	return nil
}

func responseFileSearchInputReservation(input, output int64, request responseFileSearchRequest) (int64, error) {
	calls := request.maxCalls
	inputFactor, ok := safePositiveProduct(calls+1, input)
	if !ok {
		return 0, errors.New("file-search input reservation exceeds the supported range")
	}
	repeated := calls * (calls + 1) / 2
	perSearch, ok := safePositiveProduct(int64(request.maxResults), maxVectorStoreContentChunkBytes)
	if !ok {
		return 0, errors.New("file-search input reservation exceeds the supported range")
	}
	perRound, ok := safePositiveSum(perSearch, output)
	if !ok {
		return 0, errors.New("file-search input reservation exceeds the supported range")
	}
	repeatedInput, ok := safePositiveProduct(repeated, perRound)
	if !ok {
		return 0, errors.New("file-search input reservation exceeds the supported range")
	}
	input, ok = safePositiveSum(inputFactor, repeatedInput)
	if !ok {
		return 0, errors.New("file-search input reservation exceeds the supported range")
	}
	return input, nil
}

func safePositiveProduct(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left != 0 && right > responseUsageMaximum/left {
		return 0, false
	}
	return left * right, true
}

func safePositiveSum(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > responseUsageMaximum-right {
		return 0, false
	}
	return left + right, true
}

func (handler *Handler) dispatchResponseFileSearch(response *attemptWriter, request *http.Request, target providers.Target, body []byte, publicModel, keyID string, config responseFileSearchRequest, releaseDispatch func()) (int, []byte, error) {
	working, publicFields, err := prepareResponseFileSearchRequest(body)
	if err != nil {
		releaseDispatch()
		return 0, nil, err
	}

	var carried []json.RawMessage
	var usage map[string]any
	usageKnown := true
	remainingOutput := config.maxOutput
	var completedSearchCalls int64
	for dispatchIndex := int64(0); ; dispatchIndex++ {
		release := releaseDispatch
		if dispatchIndex > 0 {
			var current bool
			release, current = handler.providers.BeginDispatch(request.Context(), target)
			if !current {
				cause := errors.New("provider configuration changed during file search")
				if accountRaw, accountErr, ok := responseFileSearchAccountingFailure(cause, usage, usageKnown, completedSearchCalls); ok {
					return 0, accountRaw, accountErr
				}
				return 0, nil, cause
			}
		}
		capture := newAttemptWriter(response, false, maxInferenceBody)
		status, raw, dispatchErr := handler.dispatch(capture, request, target, "responses", working, false, "responses", publicModel, false, 0, release)
		if dispatchErr != nil || status < 200 || status >= 300 {
			capture.Commit()
			cause := dispatchErr
			if cause == nil {
				cause = fmt.Errorf("%w: provider returned status %d", errResponseFileSearchProvider, status)
			}
			if accountRaw, accountErr, ok := responseFileSearchAccountingFailure(cause, usage, usageKnown, completedSearchCalls); ok {
				return status, accountRaw, accountErr
			}
			return status, raw, dispatchErr
		}

		var upstream map[string]json.RawMessage
		var output []json.RawMessage
		if json.Unmarshal(raw, &upstream) != nil || upstream == nil || json.Unmarshal(upstream["output"], &output) != nil {
			if accountRaw, accountErr, ok := responseFileSearchAccountingFailure(errResponseFileSearchProvider, usage, usageKnown, completedSearchCalls); ok {
				return status, accountRaw, accountErr
			}
			return status, raw, errResponseFileSearchProvider
		}
		turnOutput, turnUsageKnown := mergeResponseUsage(&usage, upstream["usage"])
		if !turnUsageKnown {
			usageKnown = false
		} else if turnOutput > remainingOutput {
			cause := fmt.Errorf("%w: provider exceeded max_output_tokens", errResponseFileSearchProvider)
			if accountRaw, accountErr, ok := responseFileSearchAccountingFailure(cause, usage, usageKnown, completedSearchCalls); ok {
				return status, accountRaw, accountErr
			}
			return status, raw, cause
		} else {
			remainingOutput -= turnOutput
		}
		internal, external, err := responseFileSearchFunctionCalls(output)
		if err != nil {
			if accountRaw, accountErr, ok := responseFileSearchAccountingFailure(err, usage, usageKnown, completedSearchCalls); ok {
				return status, accountRaw, accountErr
			}
			return status, raw, err
		}
		if len(internal) == 0 {
			carried = append(carried, output...)
			final, err := finishResponseFileSearch(upstream, carried, usage, usageKnown, publicModel, publicFields)
			if err != nil {
				if accountRaw, accountErr, ok := responseFileSearchAccountingFailure(err, usage, usageKnown, completedSearchCalls); ok {
					return status, accountRaw, accountErr
				}
				return status, raw, err
			}
			response.Header().Set("Content-Type", "application/json")
			response.Header().Set("Cache-Control", "no-store")
			response.WriteHeader(status)
			_, err = response.Write(final)
			return status, final, err
		}
		if completedSearchCalls+int64(len(internal)) > config.maxCalls {
			cause := fmt.Errorf("%w: provider exceeded max_tool_calls", errResponseFileSearchProvider)
			if accountRaw, accountErr, ok := responseFileSearchAccountingFailure(cause, usage, usageKnown, completedSearchCalls); ok {
				return status, accountRaw, accountErr
			}
			return status, raw, cause
		}
		if !external && (!turnUsageKnown || remainingOutput < 1) {
			cause := fmt.Errorf("%w: provider did not leave a bounded output budget", errResponseFileSearchProvider)
			if accountRaw, accountErr, ok := responseFileSearchAccountingFailure(cause, usage, usageKnown, completedSearchCalls); ok {
				return status, accountRaw, accountErr
			}
			return status, raw, cause
		}

		toolOutputs := make([]json.RawMessage, 0, len(internal))
		replacements := make(map[int]json.RawMessage, len(internal))
		for _, call := range internal {
			queries, err := responseFileSearchQueries(call.arguments)
			if err != nil {
				if accountRaw, accountErr, ok := responseFileSearchAccountingFailure(err, usage, usageKnown, completedSearchCalls); ok {
					return status, accountRaw, accountErr
				}
				return status, raw, err
			}
			results, err := handler.searchVectorStores(request.Context(), keyID, config.vectorStoreIDs, vectorStoreSearchOptions{queries: queries, filter: config.filter, limit: config.maxResults, threshold: config.scoreThreshold})
			if err != nil {
				response.Reset()
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					if accountRaw, accountErr, ok := responseFileSearchAccountingFailure(err, usage, usageKnown, completedSearchCalls); ok {
						return status, accountRaw, accountErr
					}
					return status, raw, err
				}
				errorStatus, code, message := vectorStoreSearchError(err)
				handler.writeError(response, "responses", errorStatus, code, message)
				if accountRaw, accountErr, ok := responseFileSearchAccountingFailure(err, usage, usageKnown, completedSearchCalls); ok {
					return errorStatus, accountRaw, accountErr
				}
				return errorStatus, raw, err
			}
			items := responseFileSearchResults(results)
			var included *[]responseFileSearchResultItem
			if config.includeResults {
				included = &items
			}
			publicCall := responseFileSearchCall{ID: fileSearchCallID(call.callID), Type: "file_search_call", Status: "completed", Queries: queries, Results: included}
			replacements[call.index], _ = json.Marshal(publicCall)
			completedSearchCalls++
			encoded, _ := json.Marshal(map[string]any{"results": responseFileSearchContext(items)})
			toolOutput, _ := json.Marshal(map[string]any{"type": "function_call_output", "call_id": call.callID, "output": string(encoded)})
			toolOutputs = append(toolOutputs, toolOutput)
		}
		carried = appendResponseFileSearchOutput(carried, output, internal, replacements)
		if external {
			final, err := finishResponseFileSearch(upstream, carried, usage, usageKnown, publicModel, publicFields)
			if err != nil {
				if accountRaw, accountErr, ok := responseFileSearchAccountingFailure(err, usage, usageKnown, completedSearchCalls); ok {
					return status, accountRaw, accountErr
				}
				return status, raw, err
			}
			response.Header().Set("Content-Type", "application/json")
			response.Header().Set("Cache-Control", "no-store")
			response.WriteHeader(status)
			_, err = response.Write(final)
			return status, final, err
		}
		working, err = appendResponseFileSearchInput(working, output, toolOutputs, remainingOutput)
		if err != nil {
			if accountRaw, accountErr, ok := responseFileSearchAccountingFailure(err, usage, usageKnown, completedSearchCalls); ok {
				return status, accountRaw, accountErr
			}
			return status, raw, err
		}
	}
}

type responseFileSearchFunctionCall struct {
	index     int
	callID    string
	arguments json.RawMessage
}

func prepareResponseFileSearchRequest(body []byte) ([]byte, map[string]json.RawMessage, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil {
		return nil, nil, errors.New("request body must be a JSON object")
	}
	publicFields := make(map[string]json.RawMessage, 4)
	for _, name := range []string{"tools", "tool_choice", "max_output_tokens", "max_tool_calls"} {
		if raw, exists := envelope[name]; exists {
			publicFields[name] = append(json.RawMessage(nil), raw...)
		}
	}
	if _, exists := publicFields["tool_choice"]; !exists {
		publicFields["tool_choice"] = json.RawMessage(`"auto"`)
	}
	var tools []map[string]json.RawMessage
	if json.Unmarshal(envelope["tools"], &tools) != nil {
		return nil, nil, errors.New("tools must be an array")
	}
	for index, tool := range tools {
		var kind string
		_ = json.Unmarshal(tool["type"], &kind)
		if kind != "file_search" {
			continue
		}
		tools[index] = map[string]json.RawMessage{
			"type":        json.RawMessage(`"function"`),
			"name":        json.RawMessage(`"` + responseFileSearchFunctionName + `"`),
			"description": json.RawMessage(`"Search the configured private knowledge base. Use concise queries containing the terms needed to answer the user."`),
			"strict":      json.RawMessage(`true`),
			"parameters":  json.RawMessage(`{"type":"object","properties":{"queries":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":16}},"required":["queries"],"additionalProperties":false}`),
		}
	}
	envelope["tools"], _ = json.Marshal(tools)
	delete(envelope, "max_tool_calls")
	delete(envelope, "include")
	if raw := envelope["tool_choice"]; len(raw) > 0 {
		var choice map[string]json.RawMessage
		if json.Unmarshal(raw, &choice) == nil {
			var kind string
			_ = json.Unmarshal(choice["type"], &kind)
			if kind == "file_search" {
				envelope["tool_choice"] = json.RawMessage(`{"type":"function","name":"` + responseFileSearchFunctionName + `"}`)
			} else if kind == "allowed_tools" {
				var allowed []map[string]json.RawMessage
				if json.Unmarshal(choice["tools"], &allowed) == nil {
					for index, tool := range allowed {
						var toolType string
						_ = json.Unmarshal(tool["type"], &toolType)
						if toolType == "file_search" {
							allowed[index] = map[string]json.RawMessage{"type": json.RawMessage(`"function"`), "name": json.RawMessage(`"` + responseFileSearchFunctionName + `"`)}
						}
					}
					choice["tools"], _ = json.Marshal(allowed)
					envelope["tool_choice"], _ = json.Marshal(choice)
				}
			}
		}
	}
	raw, err := marshalBoundedResponse(envelope)
	return raw, publicFields, err
}

func responseFileSearchFunctionCalls(output []json.RawMessage) ([]responseFileSearchFunctionCall, bool, error) {
	var internal []responseFileSearchFunctionCall
	external := false
	for index, item := range output {
		var fields map[string]json.RawMessage
		if json.Unmarshal(item, &fields) != nil || fields == nil {
			return nil, false, errResponseFileSearchProvider
		}
		var kind, name string
		_ = json.Unmarshal(fields["type"], &kind)
		_ = json.Unmarshal(fields["name"], &name)
		if kind != "function_call" || name != responseFileSearchFunctionName {
			external = external || kind == "function_call"
			continue
		}
		var callID, arguments string
		if json.Unmarshal(fields["call_id"], &callID) != nil || callID == "" || json.Unmarshal(fields["arguments"], &arguments) != nil || !json.Valid([]byte(arguments)) {
			return nil, false, errResponseFileSearchProvider
		}
		internal = append(internal, responseFileSearchFunctionCall{index: index, callID: callID, arguments: json.RawMessage(arguments)})
	}
	return internal, external, nil
}

func appendResponseFileSearchOutput(carried, output []json.RawMessage, internal []responseFileSearchFunctionCall, replacements map[int]json.RawMessage) []json.RawMessage {
	indexes := make(map[int]struct{}, len(internal))
	for _, call := range internal {
		indexes[call.index] = struct{}{}
	}
	for index, item := range output {
		if replacement := replacements[index]; len(replacement) > 0 {
			carried = append(carried, replacement)
			continue
		}
		if _, private := indexes[index]; !private {
			carried = append(carried, item)
		}
	}
	return carried
}

func responseFileSearchQueries(arguments json.RawMessage) ([]string, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(arguments, &fields) != nil || fields == nil || !onlyJSONFields(arguments, "queries") {
		return nil, fmt.Errorf("%w: file-search arguments are invalid", errResponseFileSearchProvider)
	}
	queries, err := parseVectorStoreSearchQueries(fields["queries"])
	if err != nil || len(searchTermFrequency(strings.Join(queries, " "))) == 0 {
		return nil, fmt.Errorf("%w: file-search queries are invalid", errResponseFileSearchProvider)
	}
	return queries, nil
}

func responseFileSearchResults(results []vectorStoreSearchResult) []responseFileSearchResultItem {
	items := make([]responseFileSearchResultItem, 0, len(results))
	for _, result := range results {
		var text bytes.Buffer
		for index, content := range result.Content {
			if index > 0 {
				text.WriteByte('\n')
			}
			text.WriteString(content.Text)
		}
		items = append(items, responseFileSearchResultItem{Attributes: result.Attributes, FileID: result.FileID, Filename: result.Filename, Score: result.Score, Text: text.String()})
	}
	return items
}

func responseFileSearchContext(results []responseFileSearchResultItem) []map[string]string {
	items := make([]map[string]string, 0, len(results))
	for _, result := range results {
		items = append(items, map[string]string{"text": result.Text})
	}
	return items
}

func appendResponseFileSearchInput(body []byte, output, toolOutputs []json.RawMessage, remainingOutput int64) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil {
		return nil, errResponseFileSearchProvider
	}
	var input []json.RawMessage
	if raw := bytes.TrimSpace(envelope["input"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if len(raw) > 0 && raw[0] == '[' {
			if json.Unmarshal(raw, &input) != nil {
				return nil, errResponseFileSearchProvider
			}
		} else {
			var text string
			if json.Unmarshal(raw, &text) != nil {
				return nil, errResponseFileSearchProvider
			}
			message, _ := json.Marshal(map[string]any{"role": "user", "content": text})
			input = append(input, message)
		}
	}
	input = append(input, output...)
	input = append(input, toolOutputs...)
	envelope["input"], _ = json.Marshal(input)
	envelope["max_output_tokens"], _ = json.Marshal(remainingOutput)
	if len(toolOutputs) > 0 {
		var choice map[string]json.RawMessage
		if json.Unmarshal(envelope["tool_choice"], &choice) == nil {
			var kind string
			_ = json.Unmarshal(choice["type"], &kind)
			if kind == "allowed_tools" {
				choice["mode"] = json.RawMessage(`"auto"`)
				envelope["tool_choice"], _ = json.Marshal(choice)
			} else {
				envelope["tool_choice"] = json.RawMessage(`"auto"`)
			}
		} else {
			envelope["tool_choice"] = json.RawMessage(`"auto"`)
		}
	}
	return marshalBoundedResponse(envelope)
}

func finishResponseFileSearch(upstream map[string]json.RawMessage, output []json.RawMessage, usage map[string]any, usageKnown bool, publicModel string, publicFields map[string]json.RawMessage) ([]byte, error) {
	upstream["output"], _ = json.Marshal(output)
	upstream["model"], _ = json.Marshal(publicModel)
	for _, name := range []string{"tools", "tool_choice", "max_output_tokens", "max_tool_calls"} {
		if raw, exists := publicFields[name]; exists {
			upstream[name] = raw
		} else {
			delete(upstream, name)
		}
	}
	if usageKnown && usage != nil {
		upstream["usage"], _ = json.Marshal(usage)
	} else {
		delete(upstream, "usage")
	}
	return marshalBoundedResponse(upstream)
}

func responseFileSearchAccountingFailure(cause error, usage map[string]any, usageKnown bool, completedSearchCalls int64) ([]byte, error, bool) {
	if cause == nil || !usageKnown || usage == nil {
		return nil, nil, false
	}
	raw, err := marshalBoundedResponse(map[string]any{
		"object":                    "response",
		"status":                    "incomplete",
		"output":                    []any{},
		"usage":                     usage,
		"_gateway_tool_call_count":  completedSearchCalls,
		"_gateway_tool_call_status": "incomplete",
	})
	if err != nil {
		return nil, nil, false
	}
	return raw, &responseFileSearchAccountedError{cause: cause}, true
}

func mergeResponseUsage(total *map[string]any, raw json.RawMessage) (int64, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var current map[string]any
	if decoder.Decode(&current) != nil || current == nil {
		return 0, false
	}
	for _, name := range []string{"input_tokens", "output_tokens", "total_tokens"} {
		if _, ok := responseUsageInteger(current[name]); !ok {
			return 0, false
		}
	}
	output, _ := responseUsageInteger(current["output_tokens"])
	if *total == nil {
		*total = map[string]any{}
	}
	return output, mergeResponseUsageMap(*total, current)
}

func mergeResponseUsageMap(total, current map[string]any) bool {
	for name, value := range current {
		switch value := value.(type) {
		case json.Number:
			amount, ok := responseUsageInteger(value)
			if !ok {
				return false
			}
			previous, _ := responseUsageInteger(total[name])
			if amount > responseUsageMaximum-previous {
				return false
			}
			total[name] = previous + amount
		case map[string]any:
			nested, _ := total[name].(map[string]any)
			if nested == nil {
				nested = map[string]any{}
				total[name] = nested
			}
			if !mergeResponseUsageMap(nested, value) {
				return false
			}
		case nil:
		default:
			return false
		}
	}
	return true
}

func responseUsageInteger(value any) (int64, bool) {
	switch value := value.(type) {
	case json.Number:
		parsed, err := value.Int64()
		return parsed, err == nil && parsed >= 0 && parsed <= responseUsageMaximum
	case int64:
		return value, value >= 0 && value <= responseUsageMaximum
	case nil:
		return 0, false
	default:
		return 0, false
	}
}

func fileSearchCallID(callID string) string {
	sum := sha256.Sum256([]byte(callID))
	return "fs_" + hex.EncodeToString(sum[:12])
}

func marshalBoundedResponse(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > maxInferenceBody {
		return nil, errors.New("file-search context exceeds 16 MiB")
	}
	return raw, nil
}
