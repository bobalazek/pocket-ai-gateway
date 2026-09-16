package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
	"github.com/bobalazek/pocket-ai-gateway/internal/gateway"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dataDir, err := os.MkdirTemp("", "pocket-ai-gateway-sdk-")
	must(err)
	defer os.RemoveAll(dataDir)
	store, err := storage.Open(ctx, dataDir)
	must(err)
	defer store.Close()

	upstreamListener, err := net.Listen("tcp4", "127.0.0.1:0")
	must(err)
	upstream := &http.Server{Handler: http.HandlerFunc(upstreamHandler)}
	go upstream.Serve(upstreamListener)
	defer upstream.Shutdown(context.Background())

	owner := auth.User{ID: "usr_sdk", Role: "owner", Status: "active"}
	now := time.Now().UnixMilli()
	_, err = store.SystemDB().ExecContext(ctx, "INSERT INTO users (id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES (?,?,?,'hash','owner','active',1,?,?)", owner.ID, "sdk@example.test", "SDK", now, now)
	must(err)
	masterKey := make([]byte, 32)
	providerService := providers.New(store.SystemDB(), masterKey)
	connections := make([]string, 0, 3)
	base := "http://" + upstreamListener.Addr().String()
	for _, adapter := range []string{"openai", "anthropic", "gemini"} {
		connectionInput := providers.ConnectionInput{Name: adapter, Adapter: adapter, BaseURL: base + "/" + adapter + map[string]string{"openai": "/v1", "anthropic": "/v1", "gemini": "/v1beta"}[adapter], Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 5000}
		if adapter == "anthropic" {
			connectionInput = providers.ConnectionInput{Name: adapter, Preset: "anthropic", Enabled: true, TimeoutMS: 5000}
		}
		connection, err := providerService.CreateConnection(ctx, owner, connectionInput)
		must(err)
		if adapter == "openai" {
			_, err = store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET preset='openai' WHERE id=?", connection.ID)
			must(err)
		} else if adapter == "anthropic" {
			_, err = store.SystemDB().ExecContext(ctx, "UPDATE provider_connections SET base_url=?,allow_private_network=1 WHERE id=?", base+"/anthropic/v1", connection.ID)
			must(err)
		}
		must(providerService.PutCredential(ctx, owner, connection.ID, "provider-secret", ""))
		capabilities := []string{"chat"}
		if adapter == "anthropic" {
			capabilities = append(capabilities, "prompt_cache", "web_search", "web_fetch")
		}
		if adapter == "openai" {
			capabilities = append(capabilities, "moderations", "count_tokens", "images", "audio_speech", "audio_transcription", "audio_translation")
		}
		upstreamID := adapter + "-upstream"
		if adapter == "openai" {
			upstreamID = "whisper-1"
		}
		upstreamModel, err := providerService.CreateUpstreamModel(ctx, owner, connection.ID, upstreamID, capabilities)
		must(err)
		_, err = providerService.CreatePublicModel(ctx, owner, "target-"+adapter, "Target "+adapter, "", upstreamModel.ID, capabilities)
		must(err)
		if adapter == "openai" {
			completionModel, createErr := providerService.CreateUpstreamModel(ctx, owner, connection.ID, "gpt-3.5-turbo-instruct", []string{"completions"})
			must(createErr)
			_, createErr = providerService.CreatePublicModel(ctx, owner, "target-openai-completion", "Target OpenAI completion", "", completionModel.ID, completionModel.Capabilities)
			must(createErr)
			embeddingModel, createErr := providerService.CreateUpstreamModel(ctx, owner, connection.ID, "text-embedding-3-small", []string{"embeddings"})
			must(createErr)
			_, createErr = providerService.CreatePublicModel(ctx, owner, "target-openai-embedding", "Target OpenAI embedding", "", embeddingModel.ID, embeddingModel.Capabilities)
			must(createErr)
			editModel, createErr := providerService.CreateUpstreamModel(ctx, owner, connection.ID, "gpt-image-1", []string{"image_edit"})
			must(createErr)
			_, createErr = providerService.CreatePublicModel(ctx, owner, "target-openai-edit", "Target OpenAI edit", "", editModel.ID, editModel.Capabilities)
			must(createErr)
			variationModel, createErr := providerService.CreateUpstreamModel(ctx, owner, connection.ID, "dall-e-2", []string{"image_variation"})
			must(createErr)
			_, createErr = providerService.CreatePublicModel(ctx, owner, "target-openai-variation", "Target OpenAI variation", "", variationModel.ID, variationModel.Capabilities)
			must(createErr)
			webModel, createErr := providerService.CreateUpstreamModel(ctx, owner, connection.ID, "gpt-5-mini", []string{"chat", "web_search"})
			must(createErr)
			_, createErr = providerService.CreatePublicModel(ctx, owner, "target-openai-web", "Target OpenAI web search", "", webModel.ID, webModel.Capabilities)
			must(createErr)
		}
		connections = append(connections, connection.ID)
	}
	keyService := keys.New(store.SystemDB())
	_, secret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Official SDK matrix", Scopes: []string{"chat:generate", "completions:generate", "messages:batches", "messages:web_search", "messages:web_fetch", "responses:generate", "responses:web_search", "embeddings:generate", "moderations:classify", "images:generate", "images:edit", "images:variation", "audio:speech", "audio:transcribe", "audio:translate", "models:read", "tokens:count", "files:manage", "batches:manage"}, ModelPatterns: []string{"target-*"}, ConnectionIDs: connections})
	must(err)
	_, otherSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Official SDK ownership boundary", Scopes: []string{"files:manage", "batches:manage", "responses:generate"}, ModelPatterns: []string{"target-*"}, ConnectionIDs: connections})
	must(err)
	_, noFilesSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Official SDK Files scope boundary", Scopes: []string{"chat:generate"}, ModelPatterns: []string{"target-*"}, ConnectionIDs: connections})
	must(err)
	_, noBatchesSecret, err := keyService.Create(ctx, owner.ID, keys.Input{Label: "Official SDK Batches scope boundary", Scopes: []string{"files:manage", "responses:generate"}, ModelPatterns: []string{"target-*"}, ConnectionIDs: connections})
	must(err)

	mux := http.NewServeMux()
	gatewayHandler := gateway.NewWithMasterKey(store.SystemDB(), keyService, providerService, usage.New(store.SystemDB()), masterKey)
	gatewayHandler.Register(mux)
	go gatewayHandler.RunBackground(ctx)
	gatewayListener, err := net.Listen("tcp4", "127.0.0.1:0")
	must(err)
	server := &http.Server{Handler: mux}
	go server.Serve(gatewayListener)
	encoded, _ := json.Marshal(map[string]string{"url": "http://" + gatewayListener.Addr().String(), "key": secret, "otherKey": otherSecret, "noFilesKey": noFilesSecret, "noBatchesKey": noBatchesSecret})
	fmt.Println(string(encoded))
	<-ctx.Done()
	server.Shutdown(context.Background())
}

func upstreamHandler(response http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	if strings.Contains(string(body), "Batch fail") {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusBadRequest)
		io.WriteString(response, `{"error":{"message":"Batch item failed","type":"invalid_request_error","code":"batch_item_failed"}}`)
		return
	}
	if strings.Contains(string(body), "Cancel me") {
		select {
		case <-request.Context().Done():
			return
		case <-time.After(time.Second):
		}
	}
	stream := strings.Contains(string(body), `"stream":true`) || strings.Contains(request.URL.Path, "streamGenerateContent") || strings.HasSuffix(request.URL.Path, "/audio/transcriptions") && bytes.Contains(body, []byte("\r\n\r\ntrue\r\n"))
	response.Header().Set("Content-Type", map[bool]string{true: "text/event-stream", false: "application/json"}[stream])
	target := strings.Split(strings.TrimPrefix(request.URL.Path, "/"), "/")[0]
	if stream {
		if strings.HasSuffix(request.URL.Path, "/audio/transcriptions") {
			io.WriteString(response, "event: transcript.text.delta\ndata: {\"type\":\"transcript.text.delta\",\"delta\":\"gateway \"}\n\nevent: transcript.text.delta\ndata: {\"type\":\"transcript.text.delta\",\"delta\":\"stream\"}\n\nevent: transcript.text.done\ndata: {\"type\":\"transcript.text.done\",\"text\":\"gateway stream\",\"usage\":{\"type\":\"tokens\",\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}\n\n")
			return
		}
		if target == "openai" && strings.HasSuffix(request.URL.Path, "/responses") {
			if strings.Contains(string(body), "Fail stream") {
				io.WriteString(response, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_failed\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\nevent: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed\",\"object\":\"response\",\"status\":\"failed\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n")
				return
			}
			io.WriteString(response, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"Hello\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n")
			return
		}
		if target == "openai" && strings.HasSuffix(request.URL.Path, "/v1/completions") {
			io.WriteString(response, "data: {\"id\":\"cmpl_stream\",\"object\":\"text_completion\",\"created\":1764967971,\"model\":\"gpt-3.5-turbo-instruct\",\"choices\":[{\"text\":\"Legacy \",\"index\":0,\"logprobs\":null,\"finish_reason\":null}],\"usage\":null}\n\n"+
				"data: {\"id\":\"cmpl_stream\",\"object\":\"text_completion\",\"created\":1764967971,\"model\":\"gpt-3.5-turbo-instruct\",\"choices\":[{\"text\":\"completion\",\"index\":0,\"logprobs\":null,\"finish_reason\":\"stop\"}],\"usage\":null}\n\n"+
				"data: {\"id\":\"cmpl_stream\",\"object\":\"text_completion\",\"created\":1764967971,\"model\":\"gpt-3.5-turbo-instruct\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n"+
				"data: [DONE]\n\n")
			return
		}
		if target == "anthropic" && bytes.Contains(body, []byte(`"type":"web_fetch_20250910"`)) {
			io.WriteString(response, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fetch_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"anthropic-upstream\",\"content\":[],\"container\":null,\"stop_details\":null,\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"cache_creation\":null,\"cache_creation_input_tokens\":null,\"cache_read_input_tokens\":null,\"inference_geo\":null,\"input_tokens\":9,\"output_tokens\":0,\"output_tokens_details\":null,\"server_tool_use\":null,\"service_tier\":\"standard\"}}}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"server_tool_use\",\"id\":\"srvtoolu_fetch_1\",\"name\":\"web_fetch\",\"caller\":{\"type\":\"direct\"},\"input\":{}}}\n\n"+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"url\\\":\\\"https://example.com/page\\\"}\"}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"web_fetch_tool_result\",\"tool_use_id\":\"srvtoolu_fetch_1\",\"caller\":{\"type\":\"direct\"},\"content\":{\"type\":\"web_fetch_result\",\"url\":\"https://example.com/page\",\"retrieved_at\":\"2026-09-16T08:00:00Z\",\"content\":{\"type\":\"document\",\"source\":{\"type\":\"text\",\"media_type\":\"text/plain\",\"data\":\"Fetched page\"},\"title\":\"Example page\",\"citations\":{\"enabled\":true}}}}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"text\",\"text\":\"Fetched answer\",\"citations\":[]}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":2}\n\n"+
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"container\":null,\"stop_details\":null,\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"cache_creation_input_tokens\":null,\"cache_read_input_tokens\":null,\"input_tokens\":9,\"output_tokens\":3,\"output_tokens_details\":null,\"server_tool_use\":{\"web_fetch_requests\":1,\"web_search_requests\":0}}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		if target == "anthropic" && bytes.Contains(body, []byte(`"type":"web_search_20250305"`)) {
			io.WriteString(response, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_web_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"anthropic-upstream\",\"content\":[],\"container\":null,\"stop_details\":null,\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"cache_creation\":null,\"cache_creation_input_tokens\":null,\"cache_read_input_tokens\":null,\"inference_geo\":null,\"input_tokens\":8,\"output_tokens\":0,\"output_tokens_details\":null,\"server_tool_use\":null,\"service_tier\":\"standard\"}}}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"server_tool_use\",\"id\":\"srvtoolu_1\",\"name\":\"web_search\",\"caller\":{\"type\":\"direct\"},\"input\":{}}}\n\n"+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"query\\\":\\\"Pocket AI Gateway\\\"}\"}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"web_search_tool_result\",\"tool_use_id\":\"srvtoolu_1\",\"caller\":{\"type\":\"direct\"},\"content\":[{\"type\":\"web_search_result\",\"url\":\"https://example.com/source\",\"title\":\"Example source\",\"encrypted_content\":\"encrypted-result\",\"page_age\":\"September 15, 2026\"}]}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"text\",\"text\":\"\",\"citations\":[]}}\n\n"+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"text_delta\",\"text\":\"A sourced answer\"}}\n\n"+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"citations_delta\",\"citation\":{\"type\":\"web_search_result_location\",\"url\":\"https://example.com/source\",\"title\":\"Example source\",\"encrypted_index\":\"encrypted-index\",\"cited_text\":\"Pocket AI Gateway\"}}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":2}\n\n"+
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"container\":null,\"stop_details\":null,\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"cache_creation_input_tokens\":null,\"cache_read_input_tokens\":null,\"input_tokens\":8,\"output_tokens\":4,\"output_tokens_details\":null,\"server_tool_use\":{\"web_fetch_requests\":0,\"web_search_requests\":1}}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		switch target {
		case "openai":
			io.WriteString(response, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
		case "anthropic":
			io.WriteString(response, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		case "gemini":
			io.WriteString(response, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1}}\n\n")
		}
		return
	}
	switch target {
	case "openai":
		if strings.HasSuffix(request.URL.Path, "/v1/completions") {
			io.WriteString(response, `{"id":"cmpl_1","object":"text_completion","created":1764967971,"model":"gpt-3.5-turbo-instruct","choices":[{"text":"Legacy completion","index":0,"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/embeddings") {
			io.WriteString(response, `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0},{"object":"embedding","embedding":[0.3,0.4],"index":1}],"model":"text-embedding-3-small","usage":{"prompt_tokens":2,"total_tokens":2}}`)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/images/edits") {
			io.WriteString(response, `{"created":1764967971,"data":[{"b64_json":"ZWRpdA=="}],"usage":{"input_tokens":3,"input_tokens_details":{"image_tokens":2,"text_tokens":1},"output_tokens":2,"total_tokens":5}}`)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/images/variations") {
			io.WriteString(response, `{"created":1,"data":[{"b64_json":"dmFyaWF0aW9u"}]}`)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/audio/translations") {
			io.WriteString(response, `{"text":"gateway translation"}`)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/audio/transcriptions") {
			io.WriteString(response, `{"text":"gateway transcript"}`)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/audio/speech") {
			response.Header().Set("Content-Type", "audio/mpeg")
			response.Write([]byte("ID3gateway-audio"))
			return
		}
		if strings.HasSuffix(request.URL.Path, "/images/generations") {
			io.WriteString(response, `{"created":1764967971,"data":[{"b64_json":"eA=="}],"usage":{"input_tokens":5,"input_tokens_details":{"image_tokens":0,"text_tokens":5},"output_tokens":7,"total_tokens":12}}`)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/responses/input_tokens") {
			io.WriteString(response, `{"object":"response.input_tokens","input_tokens":12}`)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/moderations") {
			io.WriteString(response, `{"id":"modr_1","model":"openai-upstream","results":[{"flagged":true,"categories":{"violence":true},"category_scores":{"violence":0.9}}]}`)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/responses/compact") {
			io.WriteString(response, `{"id":"resp_compact","object":"response.compaction","created_at":1764967971,"output":[{"id":"cmp_1","type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/responses") {
			if bytes.Contains(body, []byte(`"type":"web_search"`)) {
				if !bytes.Contains(body, []byte(`"store":false`)) {
					response.WriteHeader(http.StatusBadRequest)
					io.WriteString(response, `{"error":{"message":"web search must disable upstream storage","type":"invalid_request_error","code":"invalid_request"}}`)
					return
				}
				io.WriteString(response, `{"id":"resp_web","object":"response","status":"completed","model":"gpt-5-mini","output":[{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","queries":["Pocket AI Gateway"],"sources":[{"type":"url","url":"https://example.com/source"}]}},{"id":"msg_web","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"A sourced answer","annotations":[{"type":"url_citation","start_index":2,"end_index":8,"url":"https://example.com/source","title":"Example source"}]}]}],"usage":{"input_tokens":8,"output_tokens":4,"total_tokens":12}}`)
				return
			}
			io.WriteString(response, `{"id":"resp_1","object":"response","status":"completed","model":"openai-upstream","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello","annotations":[]}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
			return
		}
		io.WriteString(response, `{"id":"chat_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	case "anthropic":
		if bytes.Contains(body, []byte(`"type":"web_fetch_20250910"`)) {
			io.WriteString(response, `{"id":"msg_fetch","type":"message","role":"assistant","model":"anthropic-upstream","container":null,"content":[{"type":"server_tool_use","id":"srvtoolu_fetch_1","name":"web_fetch","caller":{"type":"direct"},"input":{"url":"https://example.com/page"}},{"type":"web_fetch_tool_result","tool_use_id":"srvtoolu_fetch_1","caller":{"type":"direct"},"content":{"type":"web_fetch_result","url":"https://example.com/page","retrieved_at":null,"content":{"type":"document","source":{"type":"text","media_type":"text/plain","data":"Fetched page"},"title":"Example page","citations":{"enabled":true}}}},{"type":"text","text":"Fetched answer","citations":[]}],"stop_details":null,"stop_reason":"end_turn","stop_sequence":null,"usage":{"cache_creation":null,"cache_creation_input_tokens":null,"cache_read_input_tokens":null,"inference_geo":null,"input_tokens":9,"output_tokens":3,"output_tokens_details":null,"server_tool_use":{"web_fetch_requests":1,"web_search_requests":0},"service_tier":"standard"}}`)
			return
		}
		if bytes.Contains(body, []byte(`"type":"web_search_20250305"`)) {
			io.WriteString(response, `{"id":"msg_web","type":"message","role":"assistant","model":"target-anthropic","container":null,"content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","caller":{"type":"direct"},"input":{"query":"Pocket AI Gateway"}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","caller":{"type":"direct"},"content":[{"type":"web_search_result","url":"https://example.com/source","title":"Example source","encrypted_content":"encrypted-result","page_age":"September 15, 2026"}]},{"type":"text","text":"A sourced answer","citations":[{"type":"web_search_result_location","url":"https://example.com/source","title":"Example source","encrypted_index":"encrypted-index","cited_text":"Pocket AI Gateway"}]}],"stop_details":null,"stop_reason":"end_turn","stop_sequence":null,"usage":{"cache_creation":null,"cache_creation_input_tokens":null,"cache_read_input_tokens":null,"inference_geo":null,"input_tokens":8,"output_tokens":4,"output_tokens_details":null,"server_tool_use":{"web_fetch_requests":0,"web_search_requests":1},"service_tier":"standard"}}`)
			return
		}
		if bytes.Contains(body, []byte(`"cache_control"`)) {
			io.WriteString(response, `{"id":"msg_cache","type":"message","role":"assistant","model":"anthropic-upstream","container":null,"content":[{"type":"text","text":"Hello","citations":null}],"stop_details":null,"stop_reason":"end_turn","stop_sequence":null,"usage":{"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":0},"cache_creation_input_tokens":3,"cache_read_input_tokens":5,"inference_geo":null,"input_tokens":2,"output_tokens":1,"output_tokens_details":null,"server_tool_use":null,"service_tier":"standard"}}`)
			return
		}
		io.WriteString(response, `{"id":"msg_1","type":"message","role":"assistant","model":"anthropic-upstream","container":null,"content":[{"type":"text","text":"Hello","citations":null}],"stop_details":null,"stop_reason":"end_turn","stop_sequence":null,"usage":{"cache_creation":null,"cache_creation_input_tokens":null,"cache_read_input_tokens":null,"inference_geo":null,"input_tokens":2,"output_tokens":1,"output_tokens_details":null,"server_tool_use":null,"service_tier":"standard"}}`)
	case "gemini":
		io.WriteString(response, `{"responseId":"gemini_1","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}`)
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
