package gateway

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

const maxInferenceBody = 16 << 20

type Handler struct {
	database       *sql.DB
	keys           *keys.Service
	providers      *providers.Service
	usage          *usage.Service
	wake           chan struct{}
	epoch          string
	activeMu       sync.Mutex
	active         map[string]context.CancelFunc
	uploads        chan struct{}
	fileTransfers  chan struct{}
	lastOwner      string
	lastKey        string
	publicOrigin   string
	nextBackground int
	masterKey      []byte
}

func New(database *sql.DB, keyService *keys.Service, providerService *providers.Service, usageService *usage.Service, publicOrigin ...string) *Handler {
	return newHandler(database, keyService, providerService, usageService, nil, publicOrigin...)
}

func NewWithMasterKey(database *sql.DB, keyService *keys.Service, providerService *providers.Service, usageService *usage.Service, masterKey []byte, publicOrigin ...string) *Handler {
	return newHandler(database, keyService, providerService, usageService, masterKey, publicOrigin...)
}

func newHandler(database *sql.DB, keyService *keys.Service, providerService *providers.Service, usageService *usage.Service, masterKey []byte, publicOrigin ...string) *Handler {
	epoch, err := credentials.RandomToken(12)
	if err != nil {
		epoch = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	origin := ""
	if len(publicOrigin) > 0 {
		origin = strings.TrimRight(publicOrigin[0], "/")
	}
	return &Handler{database: database, keys: keyService, providers: providerService, usage: usageService, wake: make(chan struct{}, 1), epoch: epoch, active: map[string]context.CancelFunc{}, uploads: make(chan struct{}, 1), fileTransfers: make(chan struct{}, 1), publicOrigin: origin, masterKey: append([]byte(nil), masterKey...)}
}

func (handler *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/openai/v1/models", handler.openAIModels)
	mux.HandleFunc("GET /api/openai/v1/models/{model}", handler.openAIModel)
	mux.HandleFunc("POST /api/openai/v1/files", handler.createFile)
	mux.HandleFunc("GET /api/openai/v1/files", handler.listFiles)
	mux.HandleFunc("GET /api/openai/v1/files/{file_id}", handler.getFile)
	mux.HandleFunc("DELETE /api/openai/v1/files/{file_id}", handler.deleteFile)
	mux.HandleFunc("GET /api/openai/v1/files/{file_id}/content", handler.fileContent)
	mux.HandleFunc("POST /api/openai/v1/vector_stores", handler.createVectorStore)
	mux.HandleFunc("GET /api/openai/v1/vector_stores", handler.listVectorStores)
	mux.HandleFunc("GET /api/openai/v1/vector_stores/{vector_store_id}", handler.getVectorStore)
	mux.HandleFunc("POST /api/openai/v1/vector_stores/{vector_store_id}", handler.updateVectorStore)
	mux.HandleFunc("DELETE /api/openai/v1/vector_stores/{vector_store_id}", handler.deleteVectorStore)
	mux.HandleFunc("POST /api/openai/v1/vector_stores/{vector_store_id}/files", handler.createVectorStoreFile)
	mux.HandleFunc("GET /api/openai/v1/vector_stores/{vector_store_id}/files", handler.listVectorStoreFiles)
	mux.HandleFunc("GET /api/openai/v1/vector_stores/{vector_store_id}/files/{file_id}", handler.getVectorStoreFile)
	mux.HandleFunc("POST /api/openai/v1/vector_stores/{vector_store_id}/files/{file_id}", handler.updateVectorStoreFile)
	mux.HandleFunc("DELETE /api/openai/v1/vector_stores/{vector_store_id}/files/{file_id}", handler.deleteVectorStoreFile)
	mux.HandleFunc("GET /api/openai/v1/vector_stores/{vector_store_id}/files/{file_id}/content", handler.vectorStoreFileContent)
	mux.HandleFunc("POST /api/openai/v1/uploads", handler.createOpenAIUpload)
	mux.HandleFunc("POST /api/openai/v1/uploads/{upload_id}/parts", handler.addOpenAIUploadPart)
	mux.HandleFunc("POST /api/openai/v1/uploads/{upload_id}/complete", handler.completeOpenAIUpload)
	mux.HandleFunc("POST /api/openai/v1/uploads/{upload_id}/cancel", handler.cancelOpenAIUpload)
	mux.HandleFunc("POST /api/openai/v1/batches", handler.createOpenAIBatch)
	mux.HandleFunc("GET /api/openai/v1/batches", handler.listOpenAIBatches)
	mux.HandleFunc("GET /api/openai/v1/batches/{batch_id}", handler.getOpenAIBatch)
	mux.HandleFunc("POST /api/openai/v1/batches/{batch_id}/cancel", handler.cancelOpenAIBatch)
	mux.HandleFunc("POST /api/openai/v1/completions", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "openai", "completions:generate", "completions", "", nil)
	})
	mux.HandleFunc("POST /api/openai/v1/chat/completions", handler.chatCompletions)
	mux.HandleFunc("GET /api/openai/v1/chat/completions", handler.listChatCompletions)
	mux.HandleFunc("GET /api/openai/v1/chat/completions/{completion_id}", handler.getChatCompletion)
	mux.HandleFunc("POST /api/openai/v1/chat/completions/{completion_id}", handler.updateChatCompletion)
	mux.HandleFunc("DELETE /api/openai/v1/chat/completions/{completion_id}", handler.deleteChatCompletion)
	mux.HandleFunc("GET /api/openai/v1/chat/completions/{completion_id}/messages", handler.listChatCompletionMessages)
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
	mux.HandleFunc("POST /api/anthropic/v1/messages/batches", handler.createMessageBatch)
	mux.HandleFunc("GET /api/anthropic/v1/messages/batches", handler.listMessageBatches)
	mux.HandleFunc("GET /api/anthropic/v1/messages/batches/{message_batch_id}", handler.getMessageBatch)
	mux.HandleFunc("POST /api/anthropic/v1/messages/batches/{message_batch_id}/cancel", handler.cancelMessageBatch)
	mux.HandleFunc("DELETE /api/anthropic/v1/messages/batches/{message_batch_id}", handler.deleteMessageBatch)
	mux.HandleFunc("GET /api/anthropic/v1/messages/batches/{message_batch_id}/results", handler.messageBatchResults)
	mux.HandleFunc("GET /api/gemini/v1beta/models", handler.geminiModels)
	mux.HandleFunc("GET /api/gemini/v1beta/models/{model}", handler.geminiModel)
	mux.HandleFunc("POST /api/gemini/v1beta/interactions", func(w http.ResponseWriter, r *http.Request) {
		handler.forward(w, r, "gemini", "chat:generate", "interactions", "", nil)
	})
	mux.HandleFunc("POST /api/gemini/v1beta/models/{action...}", handler.geminiAction)
}
