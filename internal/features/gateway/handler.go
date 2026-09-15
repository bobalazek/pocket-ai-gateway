package gateway

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

const maxInferenceBody = 16 << 20

type Handler struct {
	database  *sql.DB
	keys      *keys.Service
	providers *providers.Service
	usage     *usage.Service
	wake      chan struct{}
	epoch     string
	activeMu  sync.Mutex
	active    map[string]context.CancelFunc
	uploads   chan struct{}
	lastOwner string
	lastKey   string
}

func New(database *sql.DB, keyService *keys.Service, providerService *providers.Service, usageService *usage.Service) *Handler {
	epoch, err := credentials.RandomToken(12)
	if err != nil {
		epoch = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return &Handler{database: database, keys: keyService, providers: providerService, usage: usageService, wake: make(chan struct{}, 1), epoch: epoch, active: map[string]context.CancelFunc{}, uploads: make(chan struct{}, 1)}
}

func (handler *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/openai/v1/models", handler.openAIModels)
	mux.HandleFunc("GET /api/openai/v1/models/{model}", handler.openAIModel)
	mux.HandleFunc("POST /api/openai/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "openai", "chat:generate", "chat/completions", "", nil)
	})
	mux.HandleFunc("POST /api/openai/v1/responses", handler.responses)
	mux.HandleFunc("POST /api/openai/v1/responses/compact", handler.compactResponse)
	mux.HandleFunc("POST /api/openai/v1/responses/input_tokens", handler.responseInputTokens)
	mux.HandleFunc("POST /api/openai/v1/conversations", handler.createConversation)
	mux.HandleFunc("GET /api/openai/v1/conversations/{conversation_id}", handler.getConversation)
	mux.HandleFunc("POST /api/openai/v1/conversations/{conversation_id}", handler.updateConversation)
	mux.HandleFunc("DELETE /api/openai/v1/conversations/{conversation_id}", handler.deleteConversation)
	mux.HandleFunc("POST /api/openai/v1/conversations/{conversation_id}/items", handler.createConversationItems)
	mux.HandleFunc("GET /api/openai/v1/conversations/{conversation_id}/items", handler.listConversationItems)
	mux.HandleFunc("GET /api/openai/v1/conversations/{conversation_id}/items/{item_id}", handler.getConversationItem)
	mux.HandleFunc("DELETE /api/openai/v1/conversations/{conversation_id}/items/{item_id}", handler.deleteConversationItem)
	mux.HandleFunc("GET /api/openai/v1/responses/{response_id}", handler.getResponse)
	mux.HandleFunc("POST /api/openai/v1/responses/{response_id}/cancel", handler.cancelResponse)
	mux.HandleFunc("GET /api/openai/v1/responses/{response_id}/input_items", handler.responseInputItems)
	mux.HandleFunc("DELETE /api/openai/v1/responses/{response_id}", handler.deleteResponse)
	mux.HandleFunc("POST /api/openai/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "openai", "embeddings:generate", "embeddings", "", nil)
	})
	mux.HandleFunc("POST /api/openai/v1/moderations", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "openai", "moderations:classify", "moderations", "", nil)
	})
	mux.HandleFunc("POST /api/openai/v1/images/generations", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "openai", "images:generate", "images/generations", "", nil)
	})
	mux.HandleFunc("POST /api/openai/v1/images/edits", handler.imageEdit)
	mux.HandleFunc("POST /api/openai/v1/images/variations", handler.imageVariation)
	mux.HandleFunc("POST /api/openai/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "openai", "audio:speech", "audio/speech", "", nil)
	})
	mux.HandleFunc("POST /api/openai/v1/audio/transcriptions", handler.audioTranscription)
	mux.HandleFunc("POST /api/openai/v1/audio/translations", handler.audioTranslation)
	mux.HandleFunc("GET /api/anthropic/v1/models", handler.anthropicModels)
	mux.HandleFunc("GET /api/anthropic/v1/models/{model}", handler.anthropicModel)
	mux.HandleFunc("POST /api/anthropic/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "anthropic", "chat:generate", "messages", "", nil)
	})
	mux.HandleFunc("POST /api/anthropic/v1/messages/count_tokens", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "anthropic", "tokens:count", "messages/count_tokens", "", nil)
	})
	mux.HandleFunc("GET /api/gemini/v1beta/models", handler.geminiModels)
	mux.HandleFunc("GET /api/gemini/v1beta/models/{model}", handler.geminiModel)
	mux.HandleFunc("POST /api/gemini/v1beta/models/{action...}", handler.geminiAction)
}
