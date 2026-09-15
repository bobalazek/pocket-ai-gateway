package server

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/gateway"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/operations"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/users"
	dashboard "github.com/bobalazek/pocket-ai-gateway/web"
)

func New(systemDatabase *sql.DB) http.Handler {
	return NewWithOrigin(systemDatabase, "")
}

func NewWithOrigin(systemDatabase *sql.DB, publicOrigin string) http.Handler {
	return NewWithUsage(systemDatabase, publicOrigin, usage.New(systemDatabase))
}

func NewWithUsage(systemDatabase *sql.DB, publicOrigin string, usageService *usage.Service) http.Handler {
	return NewWithServices(systemDatabase, publicOrigin, usageService, providers.New(systemDatabase, make([]byte, 32)))
}

func NewWithServices(systemDatabase *sql.DB, publicOrigin string, usageService *usage.Service, providerService *providers.Service) http.Handler {
	return newHandler(systemDatabase, publicOrigin, usageService, providerService, nil)
}

func NewRuntime(systemDatabase *sql.DB, publicOrigin string, usageService *usage.Service, providerService *providers.Service, operationService *operations.Service) http.Handler {
	return newHandler(systemDatabase, publicOrigin, usageService, providerService, operationService)
}

func newHandler(systemDatabase *sql.DB, publicOrigin string, usageService *usage.Service, providerService *providers.Service, operationService *operations.Service) http.Handler {
	mux := http.NewServeMux()
	authHandler := auth.NewHandler(auth.New(systemDatabase), publicOrigin)
	keyService := keys.New(systemDatabase)
	authHandler.Register(mux)
	users.NewHandler(users.New(systemDatabase), authHandler).Register(mux)
	keys.NewHandler(keyService, authHandler).Register(mux)
	usage.NewHandler(usageService, authHandler).Register(mux)
	providers.NewHandler(providerService, authHandler).Register(mux)
	if operationService != nil {
		operations.NewHandler(operationService, authHandler).Register(mux)
		mux.HandleFunc("GET /readyz", func(response http.ResponseWriter, request *http.Request) {
			if err := operationService.Ready(request.Context()); err != nil {
				writeJSON(response, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
				return
			}
			writeJSON(response, http.StatusOK, map[string]string{"status": "ready"})
		})
	}
	gateway.New(keyService, providerService, usageService).Register(mux)
	mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /llms.txt", func(response http.ResponseWriter, _ *http.Request) {
		content, err := fs.ReadFile(dashboard.Assets, "out/llms.txt")
		if err != nil {
			http.Error(response, "Help text unavailable", http.StatusServiceUnavailable)
			return
		}
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		response.Header().Set("Cache-Control", "no-cache")
		_, _ = response.Write(content)
	})
	mux.Handle("/_/", dashboardHandler())
	mux.HandleFunc("/", func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/" {
			http.Redirect(response, request, "/_/", http.StatusPermanentRedirect)
			return
		}
		if isAPIPath(request.URL.Path) {
			writeJSON(response, http.StatusNotFound, map[string]any{
				"error": map[string]string{"code": "not_found", "message": "Resource not found"},
			})
			return
		}
		http.NotFound(response, request)
	})
	handler := requestIDs(securityHeaders(mux))
	if publicOrigin != "" {
		parsed, err := url.Parse(publicOrigin)
		if err != nil {
			panic(err)
		}
		handler = trustedHost(handler, parsed.Host)
	}
	return handler
}

func requestIDs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		id, err := credentials.RandomToken(12)
		if err == nil {
			response.Header().Set("X-Request-ID", "req_"+id)
		}
		next.ServeHTTP(response, request)
	})
}

func trustedHost(next http.Handler, host string) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Host != host {
			http.Error(response, "Misdirected request", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func dashboardHandler() http.Handler {
	assets, err := fs.Sub(dashboard.Assets, "out")
	if err != nil {
		panic(err)
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.Header().Set("Allow", "GET, HEAD")
			http.Error(response, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(request.URL.Path, "/_/")
		name = strings.TrimPrefix(path.Clean("/"+name), "/")
		if name == "." || name == "" {
			name = "index.html"
		} else if path.Ext(name) == "" {
			name = path.Join(name, "index.html")
		}

		actualName := name
		content, readErr := fs.ReadFile(assets, actualName)
		status := http.StatusOK
		if readErr != nil {
			actualName = "404.html"
			content, readErr = fs.ReadFile(assets, actualName)
			status = http.StatusNotFound
		}
		if readErr != nil {
			http.Error(response, "Dashboard assets unavailable", http.StatusServiceUnavailable)
			return
		}

		if contentType := mime.TypeByExtension(path.Ext(actualName)); contentType != "" {
			response.Header().Set("Content-Type", contentType)
		}
		if status == http.StatusOK && (strings.Contains(actualName, "/_next/static/") || strings.HasPrefix(actualName, "_next/static/")) {
			response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			response.Header().Set("Cache-Control", "no-cache")
		}
		if path.Ext(actualName) == ".html" {
			response.Header().Set("Content-Security-Policy", dashboardCSP(content))
		}
		response.WriteHeader(status)
		if request.Method == http.MethodGet {
			_, _ = bytes.NewReader(content).WriteTo(response)
		}
	})
}

func isAPIPath(value string) bool {
	return value == "/api" || strings.HasPrefix(value, "/api/") || value == "/v1" || strings.HasPrefix(value, "/v1/") || value == "/readyz"
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/api/v1/") {
			response.Header().Set("Cache-Control", "no-store")
		}
		response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'none'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	})
}

func dashboardCSP(content []byte) string {
	scriptSource := "'self'"
	for _, hash := range inlineScriptHashes(content) {
		scriptSource += " 'sha256-" + hash + "'"
	}
	return "default-src 'self'; script-src " + scriptSource + "; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"
}

func inlineScriptHashes(content []byte) []string {
	const closingTag = "</script>"
	remaining := string(content)
	var hashes []string
	for {
		start := strings.Index(remaining, "<script")
		if start < 0 {
			return hashes
		}
		remaining = remaining[start+len("<script"):]
		tagEnd := strings.IndexByte(remaining, '>')
		if tagEnd < 0 {
			return hashes
		}
		attributes := remaining[:tagEnd]
		remaining = remaining[tagEnd+1:]
		end := strings.Index(remaining, closingTag)
		if end < 0 {
			return hashes
		}
		if !strings.Contains(strings.ToLower(attributes), "src=") {
			sum := sha256.Sum256([]byte(remaining[:end]))
			hashes = append(hashes, base64.StdEncoding.EncodeToString(sum[:]))
		}
		remaining = remaining[end+len(closingTag):]
	}
}
