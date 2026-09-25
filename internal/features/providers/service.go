package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
)

var (
	ErrNotFound = errors.New("provider resource not found")
	ErrDenied   = errors.New("provider operation is not permitted")
	ErrConflict = errors.New("provider resource changed")
)

var adapters = map[string][]string{
	"openai":            {"chat", "completions", "web_search", "embeddings", "moderations", "count_tokens", "images", "image_edit", "image_variation", "audio_speech", "audio_transcription", "audio_translation", "realtime"},
	"anthropic":         {"chat", "web_search", "web_search_dynamic", "web_search_response_inclusion", "web_fetch", "web_fetch_dynamic", "web_fetch_cache_bypass", "web_fetch_response_inclusion", "count_tokens", "prompt_cache"},
	"gemini":            {"chat", "count_tokens", "embeddings", "interactions", "realtime", "media_jobs"},
	"openai_compatible": {"chat", "completions", "embeddings", "moderations", "count_tokens", "images", "image_edit", "image_variation", "audio_speech", "audio_transcription", "audio_translation", "media_jobs", "decisions"},
}

var adapterLabels = map[string]string{
	"openai":            "OpenAI",
	"anthropic":         "Anthropic",
	"gemini":            "Google Gemini",
	"openai_compatible": "OpenAI compatible",
}

var capabilityLabels = map[string]string{
	"audio_speech":                  "Text to speech",
	"audio_transcription":           "Audio transcription",
	"audio_translation":             "Audio translation",
	"chat":                          "Chat",
	"completions":                   "Legacy completions",
	"count_tokens":                  "Token counting",
	"decisions":                     "Typed decisions (System One)",
	"embeddings":                    "Embeddings",
	"image_edit":                    "Image editing",
	"image_variation":               "Image variation",
	"images":                        "Image generation",
	"interactions":                  "Interactions",
	"moderations":                   "Moderation",
	"media_jobs":                    "Asynchronous media jobs",
	"prompt_cache":                  "Prompt caching",
	"realtime":                      "Realtime audio and text",
	"web_fetch":                     "Web fetch",
	"web_fetch_cache_bypass":        "Web fetch cache bypass",
	"web_fetch_dynamic":             "Dynamic web fetch",
	"web_fetch_response_inclusion":  "Web fetch response inclusion",
	"web_search":                    "Web search",
	"web_search_dynamic":            "Dynamic web search",
	"web_search_response_inclusion": "Web search response inclusion",
}

var scopeCapabilities = map[string]string{
	"audio:speech":          "audio_speech",
	"audio:transcribe":      "audio_transcription",
	"audio:translate":       "audio_translation",
	"chat:generate":         "chat",
	"completions:generate":  "completions",
	"decisions:generate":    "decisions",
	"embeddings:generate":   "embeddings",
	"images:edit":           "image_edit",
	"images:generate":       "images",
	"images:variation":      "image_variation",
	"interactions:generate": "interactions",
	"moderations:classify":  "moderations",
	"media:generate":        "media_jobs",
	"realtime:connect":      "realtime",
	"responses:generate":    "chat",
	"tokens:count":          "count_tokens",
}

type Service struct {
	database *sql.DB
	key      []byte
	dispatch sync.RWMutex
}

type ProviderType struct {
	ID                string             `json:"id"`
	Label             string             `json:"label"`
	Default           bool               `json:"default"`
	Capabilities      []string           `json:"capabilities"`
	CapabilityDetails []CapabilityDetail `json:"capability_details"`
}

type CapabilityDetail struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type Connection struct {
	ID                  string             `json:"id"`
	Name                string             `json:"name"`
	Adapter             string             `json:"adapter"`
	AdapterLabel        string             `json:"adapter_label"`
	BaseURL             string             `json:"base_url"`
	Enabled             bool               `json:"enabled"`
	AllowPrivateNetwork bool               `json:"allow_private_network"`
	TimeoutMS           int64              `json:"timeout_ms"`
	Preset              string             `json:"preset"`
	Capabilities        []string           `json:"capabilities"`
	CapabilityDetails   []CapabilityDetail `json:"capability_details"`
	CredentialRequired  bool               `json:"credential_required"`
	CredentialState     string             `json:"credential_state"`
	Revision            int64              `json:"revision"`
	CreatedAt           string             `json:"created_at"`
	UpdatedAt           string             `json:"updated_at"`
}

type ConnectionInput struct {
	Name                string `json:"name"`
	Adapter             string `json:"adapter"`
	BaseURL             string `json:"base_url"`
	Enabled             bool   `json:"enabled"`
	AllowPrivateNetwork bool   `json:"allow_private_network"`
	TimeoutMS           int64  `json:"timeout_ms"`
	Preset              string `json:"preset"`
}

type UpstreamModel struct {
	ID                string             `json:"id"`
	ConnectionID      string             `json:"connection_id"`
	UpstreamID        string             `json:"upstream_id"`
	Capabilities      []string           `json:"capabilities"`
	CapabilityDetails []CapabilityDetail `json:"capability_details"`
	Active            bool               `json:"active"`
}

type PublicModel struct {
	ID                 string             `json:"id"`
	Label              string             `json:"label"`
	Description        string             `json:"description"`
	TargetConnectionID string             `json:"target_connection_id"`
	TargetModelID      string             `json:"target_model_id"`
	UpstreamID         string             `json:"upstream_id"`
	Adapter            string             `json:"adapter"`
	AdapterLabel       string             `json:"adapter_label"`
	Capabilities       []string           `json:"capabilities"`
	CapabilityDetails  []CapabilityDetail `json:"capability_details"`
	Active             bool               `json:"active"`
	Revision           int64              `json:"revision"`
	RoutingStrategy    string             `json:"routing_strategy"`
	FreeOnly           bool               `json:"free_only"`
	RoutingPolicy      RoutingPolicy      `json:"routing_policy"`
}

type VisibleModel struct {
	ID                string             `json:"id"`
	Label             string             `json:"label"`
	Description       string             `json:"description"`
	Adapter           string             `json:"adapter"`
	AdapterLabel      string             `json:"adapter_label"`
	Capabilities      []string           `json:"capabilities"`
	CapabilityDetails []CapabilityDetail `json:"capability_details"`
}

type Target struct {
	PublicModel
	UpstreamCapabilities  []string
	BaseURL               string
	AllowPrivateNetwork   bool
	TimeoutMS             int64
	ConnectionRevision    int64
	Credential            string
	BearerCredential      bool
	Preset                string
	AdapterRequestScript  string
	AdapterResponseScript string
}

func New(database *sql.DB, key []byte) *Service {
	return &Service{database: database, key: append([]byte(nil), key...)}
}

func (service *Service) LockConfiguration() func() {
	service.dispatch.Lock()
	return service.dispatch.Unlock
}

func ProviderTypes() []ProviderType {
	names := make([]string, 0, len(adapters))
	for name := range adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]ProviderType, 0, len(names))
	for _, name := range names {
		capabilities := append([]string(nil), adapters[name]...)
		items = append(items, ProviderType{ID: name, Label: adapterLabels[name], Default: name == "openai", Capabilities: capabilities, CapabilityDetails: capabilityDetails(capabilities)})
	}
	return items
}

func (service *Service) ListConnections(ctx context.Context, actor auth.User) ([]Connection, error) {
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return nil, err
	}
	rows, err := service.database.QueryContext(ctx, `SELECT provider_connections.id, name, adapter, base_url, enabled, allow_private_network, timeout_ms, preset,
		CASE WHEN provider_credentials.external_ref IS NOT NULL THEN 'external' WHEN provider_credentials.ciphertext IS NOT NULL THEN 'stored' ELSE 'missing' END,
		revision, provider_connections.created_at, provider_connections.updated_at FROM provider_connections LEFT JOIN provider_credentials ON provider_credentials.connection_id = provider_connections.id ORDER BY created_at DESC, provider_connections.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Connection, 0)
	for rows.Next() {
		item, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (service *Service) GetConnection(ctx context.Context, actor auth.User, id string) (Connection, error) {
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return Connection{}, err
	}
	return service.getConnection(ctx, id)
}

func (service *Service) getConnection(ctx context.Context, id string) (Connection, error) {
	item, err := scanConnection(service.database.QueryRowContext(ctx, `SELECT provider_connections.id, name, adapter, base_url, enabled, allow_private_network, timeout_ms, preset,
		CASE WHEN provider_credentials.external_ref IS NOT NULL THEN 'external' WHEN provider_credentials.ciphertext IS NOT NULL THEN 'stored' ELSE 'missing' END,
		revision, provider_connections.created_at, provider_connections.updated_at FROM provider_connections LEFT JOIN provider_credentials ON provider_credentials.connection_id = provider_connections.id WHERE provider_connections.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	return item, err
}

func (service *Service) CreateConnection(ctx context.Context, actor auth.User, input ConnectionInput) (Connection, error) {
	service.dispatch.Lock()
	defer service.dispatch.Unlock()
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return Connection{}, err
	}
	input, err := validateConnection(input)
	if err != nil {
		return Connection{}, err
	}
	idPart, err := credentials.RandomToken(12)
	if err != nil {
		return Connection{}, err
	}
	id, now := "con_"+idPart, time.Now().UnixMilli()
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return Connection{}, err
	}
	defer tx.Rollback()
	if err = requireManager(ctx, tx, &actor); err != nil {
		return Connection{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO provider_connections (id,name,adapter,base_url,enabled,allow_private_network,timeout_ms,preset,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, id, input.Name, input.Adapter, input.BaseURL, input.Enabled, input.AllowPrivateNetwork, input.TimeoutMS, input.Preset, now, now); err != nil {
		return Connection{}, mutationError(err)
	}
	if err = audit(ctx, tx, actor.ID, "provider.connection.create", "provider_connection", id); err != nil {
		return Connection{}, err
	}
	if err = tx.Commit(); err != nil {
		return Connection{}, err
	}
	return service.getConnection(ctx, id)
}

func (service *Service) UpdateConnection(ctx context.Context, actor auth.User, id string, revision int64, input ConnectionInput) (Connection, error) {
	service.dispatch.Lock()
	defer service.dispatch.Unlock()
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return Connection{}, err
	}
	input, err := validateConnection(input)
	if err != nil {
		return Connection{}, err
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return Connection{}, err
	}
	defer tx.Rollback()
	if err = requireManager(ctx, tx, &actor); err != nil {
		return Connection{}, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT upstream_id,capabilities_json FROM upstream_models WHERE connection_id=?", id)
	if err != nil {
		return Connection{}, err
	}
	for rows.Next() {
		var upstreamID, raw string
		var capabilities []string
		if err = rows.Scan(&upstreamID, &raw); err == nil {
			err = json.Unmarshal([]byte(raw), &capabilities)
		}
		if err != nil || !PresetSupportsModelCapabilities(input.Preset, upstreamID, capabilities) {
			rows.Close()
			if err != nil {
				return Connection{}, err
			}
			return Connection{}, errors.New("existing model capabilities exceed the selected provider preset")
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return Connection{}, err
	}
	if err = rows.Close(); err != nil {
		return Connection{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM provider_credentials WHERE connection_id=? AND EXISTS (SELECT 1 FROM provider_connections WHERE id=? AND (adapter<>? OR base_url<>? OR preset<>?))`, id, id, input.Adapter, input.BaseURL, input.Preset); err != nil {
		return Connection{}, err
	}
	if input.Preset != "custom" || input.Adapter != "openai_compatible" {
		if _, err = tx.ExecContext(ctx, "DELETE FROM provider_adapter_scripts WHERE connection_id=?", id); err != nil {
			return Connection{}, err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE provider_connections SET name=?,adapter=?,base_url=?,enabled=?,allow_private_network=?,timeout_ms=?,preset=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, input.Name, input.Adapter, input.BaseURL, input.Enabled, input.AllowPrivateNetwork, input.TimeoutMS, input.Preset, time.Now().UnixMilli(), id, revision)
	if err != nil {
		return Connection{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Connection{}, ErrConflict
	}
	if err = audit(ctx, tx, actor.ID, "provider.connection.update", "provider_connection", id); err != nil {
		return Connection{}, err
	}
	if err = tx.Commit(); err != nil {
		return Connection{}, err
	}
	return service.getConnection(ctx, id)
}

func (service *Service) PutCredential(ctx context.Context, actor auth.User, id, secret, externalRef string) error {
	service.dispatch.Lock()
	defer service.dispatch.Unlock()
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return err
	}
	if _, err := service.getConnection(ctx, id); err != nil {
		return err
	}
	if (secret == "") == (externalRef == "") {
		return errors.New("provide exactly one credential or external_ref")
	}
	var ciphertext, nonce []byte
	if secret != "" {
		if len(secret) > 16_384 {
			return errors.New("credential is too large")
		}
		var err error
		ciphertext, nonce, err = seal(service.key, id, secret)
		if err != nil {
			return err
		}
	}
	if externalRef != "" && !validExternalRef(externalRef) {
		return errors.New("external_ref must use env:NAME, file:/absolute/path, bearer-env:NAME, or bearer-file:/absolute/path")
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = requireManager(ctx, tx, &actor); err != nil {
		return err
	}
	// External references read host environment variables and files, which can hold owner-only secrets.
	if externalRef != "" && actor.Role != "owner" {
		return ErrDenied
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO provider_credentials (connection_id,ciphertext,nonce,external_ref,updated_at) VALUES (?,?,?,?,?) ON CONFLICT(connection_id) DO UPDATE SET ciphertext=excluded.ciphertext,nonce=excluded.nonce,external_ref=excluded.external_ref,updated_at=excluded.updated_at`, id, ciphertext, nonce, nullableString(externalRef), time.Now().UnixMilli()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE provider_connections SET revision=revision+1,updated_at=? WHERE id=?", time.Now().UnixMilli(), id); err != nil {
		return err
	}
	if err = audit(ctx, tx, actor.ID, "provider.credential.replace", "provider_connection", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (service *Service) CreateUpstreamModel(ctx context.Context, actor auth.User, connectionID, upstreamID string, capabilities []string) (UpstreamModel, error) {
	service.dispatch.Lock()
	defer service.dispatch.Unlock()
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return UpstreamModel{}, err
	}
	connection, err := service.getConnection(ctx, connectionID)
	if err != nil {
		return UpstreamModel{}, err
	}
	upstreamID = strings.TrimSpace(upstreamID)
	capabilities = normalizeCapabilities(capabilities)
	if upstreamID == "" || len(upstreamID) > 300 || len(capabilities) == 0 || !validCapabilities(capabilities) {
		return UpstreamModel{}, errors.New("upstream_id and capabilities are required")
	}
	if !PresetSupportsModelCapabilities(connection.Preset, upstreamID, capabilities) {
		return UpstreamModel{}, errors.New("capabilities exceed the selected provider preset")
	}
	idPart, err := credentials.RandomToken(12)
	if err != nil {
		return UpstreamModel{}, err
	}
	id := "upm_" + idPart
	encoded, _ := json.Marshal(capabilities)
	now := time.Now().UnixMilli()
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return UpstreamModel{}, err
	}
	defer tx.Rollback()
	if err = requireManager(ctx, tx, &actor); err != nil {
		return UpstreamModel{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO upstream_models (id,connection_id,upstream_id,capabilities_json,created_at,updated_at) VALUES (?,?,?,?,?,?)", id, connectionID, upstreamID, string(encoded), now, now); err != nil {
		return UpstreamModel{}, mutationError(err)
	}
	if err = audit(ctx, tx, actor.ID, "provider.model.create", "upstream_model", id); err != nil {
		return UpstreamModel{}, err
	}
	if err = tx.Commit(); err != nil {
		return UpstreamModel{}, err
	}
	return UpstreamModel{ID: id, ConnectionID: connectionID, UpstreamID: upstreamID, Capabilities: capabilities, CapabilityDetails: capabilityDetails(capabilities), Active: true}, nil
}

func (service *Service) ListUpstreamModels(ctx context.Context, actor auth.User, connectionID string) ([]UpstreamModel, error) {
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return nil, err
	}
	rows, err := service.database.QueryContext(ctx, "SELECT id,connection_id,upstream_id,capabilities_json,active FROM upstream_models WHERE connection_id=? ORDER BY upstream_id", connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]UpstreamModel, 0)
	for rows.Next() {
		var item UpstreamModel
		var raw string
		if err := rows.Scan(&item.ID, &item.ConnectionID, &item.UpstreamID, &raw, &item.Active); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &item.Capabilities)
		item.CapabilityDetails = capabilityDetails(item.Capabilities)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (service *Service) CreatePublicModel(ctx context.Context, actor auth.User, id, label, description, targetModelID string, capabilities []string) (PublicModel, error) {
	service.dispatch.Lock()
	defer service.dispatch.Unlock()
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return PublicModel{}, err
	}
	id = strings.TrimSpace(id)
	label = strings.TrimSpace(label)
	capabilities = normalizeCapabilities(capabilities)
	if !validPublicID(id) || label == "" || len(label) > 200 || len(description) > 1000 || len(capabilities) == 0 || !validCapabilities(capabilities) {
		return PublicModel{}, errors.New("id, label, target_model_id, and capabilities are required")
	}
	var connectionID, upstreamCapabilitiesJSON string
	if err := service.database.QueryRowContext(ctx, "SELECT connection_id, capabilities_json FROM upstream_models WHERE id=? AND active=1", targetModelID).Scan(&connectionID, &upstreamCapabilitiesJSON); errors.Is(err, sql.ErrNoRows) {
		return PublicModel{}, ErrNotFound
	} else if err != nil {
		return PublicModel{}, err
	}
	var upstreamCapabilities []string
	if err := json.Unmarshal([]byte(upstreamCapabilitiesJSON), &upstreamCapabilities); err != nil {
		return PublicModel{}, err
	}
	if !subset(capabilities, upstreamCapabilities) {
		return PublicModel{}, errors.New("public capabilities must be supported by the upstream model")
	}
	raw, _ := json.Marshal(capabilities)
	now := time.Now().UnixMilli()
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return PublicModel{}, err
	}
	defer tx.Rollback()
	if err = requireManager(ctx, tx, &actor); err != nil {
		return PublicModel{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO public_models (id,label,description,target_connection_id,target_model_id,capabilities_json,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)", id, label, description, connectionID, targetModelID, string(raw), now, now); err != nil {
		return PublicModel{}, mutationError(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO public_model_targets (public_model_id,upstream_model_id,priority,weight,enabled,created_at,updated_at) VALUES (?,?,1,1,1,?,?)", id, targetModelID, now, now); err != nil {
		return PublicModel{}, err
	}
	if err = audit(ctx, tx, actor.ID, "model.publish", "public_model", id); err != nil {
		return PublicModel{}, err
	}
	if err = tx.Commit(); err != nil {
		return PublicModel{}, err
	}
	return service.ResolvePublicModel(ctx, id)
}

func (service *Service) ListPublicModels(ctx context.Context) ([]PublicModel, error) {
	return service.listPublicModels(ctx, publicModelSelect+` WHERE public_models.active=1 AND EXISTS(
		SELECT 1 FROM public_model_targets available
		JOIN upstream_models available_model ON available_model.id=available.upstream_model_id
		JOIN provider_connections available_connection ON available_connection.id=available_model.connection_id
		WHERE available.public_model_id=public_models.id AND available.enabled=1 AND available_model.active=1 AND available_connection.enabled=1
	) ORDER BY public_models.id`)
}

func (service *Service) listPublicModels(ctx context.Context, query string, arguments ...any) ([]PublicModel, error) {
	rows, err := service.database.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]PublicModel, 0)
	for rows.Next() {
		item, err := scanPublicModel(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (service *Service) ListManagedPublicModels(ctx context.Context, actor auth.User) ([]PublicModel, error) {
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return nil, err
	}
	return service.listManagedPublicModels(ctx, "")
}

func (service *Service) listManagedPublicModels(ctx context.Context, search string) ([]PublicModel, error) {
	query := publicModelSelect + " WHERE public_models.active=1"
	var arguments []any
	if search != "" {
		query += " AND (instr(lower(public_models.id),lower(?))>0 OR instr(lower(public_models.label),lower(?))>0 OR instr(lower(public_models.description),lower(?))>0 OR instr(lower(upstream_models.upstream_id),lower(?))>0 OR instr(lower(provider_connections.adapter),lower(?))>0)"
		arguments = []any{search, search, search, search, search}
	}
	return service.listPublicModels(ctx, query+" ORDER BY public_models.id", arguments...)
}

func (service *Service) ListVisibleModels(ctx context.Context, actor auth.User) ([]VisibleModel, error) {
	return service.ListVisibleModelsSearch(ctx, actor, "")
}

func (service *Service) ListVisibleModelsSearch(ctx context.Context, actor auth.User, search string) ([]VisibleModel, error) {
	var modelJSON, connectionJSON string
	if err := service.database.QueryRowContext(ctx, "SELECT role,status,inference_unrestricted,model_patterns_json,connection_ids_json FROM users WHERE id=?", actor.ID).Scan(&actor.Role, &actor.Status, &actor.Grants.Unrestricted, &modelJSON, &connectionJSON); err != nil {
		return nil, err
	}
	if actor.Status != "active" {
		return nil, ErrDenied
	}
	_ = json.Unmarshal([]byte(modelJSON), &actor.Grants.ModelPatterns)
	_ = json.Unmarshal([]byte(connectionJSON), &actor.Grants.ConnectionIDs)
	items, err := service.listManagedPublicModels(ctx, search)
	if err != nil {
		return nil, err
	}
	visible := make([]VisibleModel, 0, len(items))
	for _, item := range items {
		if !matchesModel(actor.Grants.ModelPatterns, item.ID) && !actor.Grants.Unrestricted {
			continue
		}
		if !service.HasAvailableRouteTarget(ctx, item.ID, func(id string) bool {
			return actor.Grants.Unrestricted || containsString(actor.Grants.ConnectionIDs, id)
		}, nil) {
			continue
		}
		visible = append(visible, VisibleModel{ID: item.ID, Label: item.Label, Description: item.Description, Adapter: item.Adapter, AdapterLabel: item.AdapterLabel, Capabilities: item.Capabilities, CapabilityDetails: item.CapabilityDetails})
	}
	return visible, nil
}

const publicModelSelect = `SELECT public_models.id,public_models.label,public_models.description,public_models.target_connection_id,public_models.target_model_id,upstream_models.upstream_id,provider_connections.adapter,public_models.capabilities_json,public_models.active,public_models.revision,public_models.routing_strategy,public_models.free_only FROM public_models JOIN upstream_models ON upstream_models.id=public_models.target_model_id JOIN provider_connections ON provider_connections.id=public_models.target_connection_id`

func (service *Service) ResolvePublicModel(ctx context.Context, id string) (PublicModel, error) {
	item, err := scanPublicModel(service.database.QueryRowContext(ctx, publicModelSelect+" WHERE public_models.id=? AND public_models.active=1", id))
	if errors.Is(err, sql.ErrNoRows) {
		return PublicModel{}, ErrNotFound
	}
	return item, err
}

func (service *Service) Target(ctx context.Context, id string) (Target, error) {
	model, err := service.ResolvePublicModel(ctx, id)
	if err != nil {
		return Target{}, err
	}
	var target Target
	target.PublicModel = model
	var ciphertext, nonce []byte
	var external sql.NullString
	err = service.database.QueryRowContext(ctx, `SELECT base_url,allow_private_network,timeout_ms,revision,preset,provider_credentials.ciphertext,provider_credentials.nonce,provider_credentials.external_ref,COALESCE(CASE WHEN provider_connections.preset='custom' AND provider_connections.adapter='openai_compatible' THEN provider_adapter_scripts.request_script END,''),COALESCE(CASE WHEN provider_connections.preset='custom' AND provider_connections.adapter='openai_compatible' THEN provider_adapter_scripts.response_script END,'') FROM provider_connections JOIN provider_credentials ON provider_credentials.connection_id=provider_connections.id LEFT JOIN provider_adapter_scripts ON provider_adapter_scripts.connection_id=provider_connections.id WHERE provider_connections.id=? AND enabled=1`, model.TargetConnectionID).Scan(&target.BaseURL, &target.AllowPrivateNetwork, &target.TimeoutMS, &target.ConnectionRevision, &target.Preset, &ciphertext, &nonce, &external, &target.AdapterRequestScript, &target.AdapterResponseScript)
	if errors.Is(err, sql.ErrNoRows) {
		return Target{}, ErrNotFound
	}
	if err != nil {
		return Target{}, err
	}
	if external.Valid {
		target.Credential, target.BearerCredential, err = resolveExternalCredential(external.String)
		if err != nil {
			return Target{}, err
		}
	} else {
		target.Credential, err = openSecret(service.key, model.TargetConnectionID, ciphertext, nonce)
		if err != nil {
			return Target{}, err
		}
	}
	return target, nil
}

func (service *Service) TargetIsCurrent(ctx context.Context, target Target) bool {
	var exists bool
	err := service.database.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM public_models JOIN public_model_targets ON public_model_targets.public_model_id=public_models.id JOIN upstream_models ON upstream_models.id=public_model_targets.upstream_model_id JOIN provider_connections ON provider_connections.id=upstream_models.connection_id WHERE public_models.id=? AND upstream_models.id=? AND provider_connections.id=? AND public_models.revision=? AND upstream_models.upstream_id=? AND public_models.active=1 AND public_model_targets.enabled=1 AND upstream_models.active=1 AND provider_connections.enabled=1 AND provider_connections.revision=?)`, target.ID, target.TargetModelID, target.TargetConnectionID, target.Revision, target.UpstreamID, target.ConnectionRevision).Scan(&exists)
	return err == nil && exists
}

// BeginDispatch holds configuration writes until the caller has sent its upstream request.
// Callers must release as soon as the request is written, not when the response ends:
// a waiting writer blocks every new dispatch. The returned release is idempotent.
func (service *Service) BeginDispatch(ctx context.Context, target Target) (func(), bool) {
	service.dispatch.RLock()
	if !service.TargetIsCurrent(ctx, target) {
		service.dispatch.RUnlock()
		return func() {}, false
	}
	return sync.OnceFunc(service.dispatch.RUnlock), true
}

// Shared (CGNAT, including Tailscale) and benchmarking ranges are global unicast but not the public internet.
var nonPublicPrefixes = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("198.18.0.0/15")}

// PublicAddress reports whether a provider destination may be dialed without allow_private_network.
func PublicAddress(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	address = address.Unmap()
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func validateConnection(input ConnectionInput) (ConnectionInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Adapter = strings.TrimSpace(input.Adapter)
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	input.Preset = strings.TrimSpace(input.Preset)
	if input.Preset == "" {
		input.Preset = "custom"
	}
	input = applyPreset(input)
	if input.TimeoutMS == 0 {
		input.TimeoutMS = 60000
	}
	if input.Name == "" || len(input.Name) > 200 || adapters[input.Adapter] == nil || !presetAllowed(input.Preset, input.Adapter) {
		return ConnectionInput{}, errors.New("name and supported adapter are required")
	}
	parsed, err := url.Parse(input.BaseURL)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return ConnectionInput{}, errors.New("base_url must be an absolute URL without credentials, query, or fragment")
	}
	if !presetBaseURLAllowed(input.Preset, parsed) {
		return ConnectionInput{}, errors.New("base_url does not match the selected cloud provider endpoint")
	}
	if parsed.Scheme != "https" && !(input.AllowPrivateNetwork && parsed.Scheme == "http") {
		return ConnectionInput{}, errors.New("base_url must use HTTPS unless private-network HTTP is enabled")
	}
	if !input.AllowPrivateNetwork && isPrivateHost(parsed.Hostname()) {
		return ConnectionInput{}, errors.New("base_url cannot target a private network")
	}
	if input.TimeoutMS < 1000 || input.TimeoutMS > 600000 {
		return ConnectionInput{}, errors.New("timeout_ms must be between 1000 and 600000")
	}
	return input, nil
}

func presetBaseURLAllowed(preset string, parsed *url.URL) bool {
	host := strings.ToLower(parsed.Hostname())
	endpointPath := strings.TrimRight(parsed.EscapedPath(), "/")
	switch preset {
	case "azure-openai":
		return (strings.HasSuffix(host, ".openai.azure.com") || strings.HasSuffix(host, ".services.ai.azure.com")) && endpointPath == "/openai/v1"
	case "bedrock":
		region := strings.TrimSuffix(strings.TrimPrefix(host, "bedrock-runtime."), ".amazonaws.com")
		mantleRegion := strings.TrimSuffix(strings.TrimPrefix(host, "bedrock-mantle."), ".api.aws")
		return strings.HasPrefix(host, "bedrock-runtime.") && strings.HasSuffix(host, ".amazonaws.com") && region != "" && !strings.Contains(region, ".") && endpointPath == "/openai/v1" ||
			strings.HasPrefix(host, "bedrock-mantle.") && strings.HasSuffix(host, ".api.aws") && mantleRegion != "" && !strings.Contains(mantleRegion, ".") && (endpointPath == "/v1" || endpointPath == "/openai/v1")
	case "vertex":
		segments := strings.Split(strings.Trim(endpointPath, "/"), "/")
		if len(segments) != 7 || segments[0] != "v1" && segments[0] != "v1beta1" || segments[1] != "projects" || segments[2] == "" || segments[3] != "locations" || segments[4] == "" || segments[5] != "endpoints" || segments[6] != "openapi" {
			return false
		}
		return host == "aiplatform.googleapis.com" || host == segments[4]+"-aiplatform.googleapis.com"
	default:
		return true
	}
}

func ValidatePortableConnection(input ConnectionInput) (ConnectionInput, error) {
	return validateConnection(input)
}
func NormalizePortableCapabilities(values []string) ([]string, bool) {
	values = normalizeCapabilities(values)
	return values, len(values) > 0 && validCapabilities(values)
}
func ValidPortableModelID(value string) bool { return validPublicID(value) }

func isPrivateHost(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified())
}
func normalizeCapabilities(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
func capabilityDetails(values []string) []CapabilityDetail {
	details := make([]CapabilityDetail, 0, len(values))
	for _, value := range values {
		details = append(details, CapabilityDetail{ID: value, Label: capabilityLabels[value]})
	}
	return details
}
func CapabilityForScope(scope string) string { return scopeCapabilities[scope] }
func SupportsScope(values []string, scope string) bool {
	wanted := CapabilityForScope(scope)
	return wanted != "" && containsString(values, wanted)
}
func validCapabilities(values []string) bool {
	seen := make(map[string]bool, len(values))
	hosted := false
	for _, value := range values {
		if _, valid := capabilityLabels[value]; !valid {
			return false
		}
		seen[value] = true
		hosted = hosted || value == "prompt_cache" || strings.HasPrefix(value, "web_search") || strings.HasPrefix(value, "web_fetch")
	}
	if hosted && !seen["chat"] {
		return false
	}
	for capability, parent := range map[string]string{
		"web_search_dynamic":            "web_search",
		"web_search_response_inclusion": "web_search_dynamic",
		"web_fetch_dynamic":             "web_fetch",
		"web_fetch_cache_bypass":        "web_fetch_dynamic",
		"web_fetch_response_inclusion":  "web_fetch_cache_bypass",
	} {
		if seen[capability] && !seen[parent] {
			return false
		}
	}
	return true
}
func validPublicID(value string) bool {
	if value == "" || len(value) > 200 {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || index > 0 && (character == '.' || character == '_' || character == '-') {
			continue
		}
		return false
	}
	return true
}
func subset(values, allowed []string) bool {
	set := map[string]bool{}
	for _, value := range allowed {
		set[value] = true
	}
	for _, value := range values {
		if !set[value] {
			return false
		}
	}
	return true
}
func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
func matchesModel(patterns []string, model string) bool {
	for _, pattern := range patterns {
		if matched, _ := path.Match(pattern, model); matched {
			return true
		}
	}
	return false
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func requireManager(ctx context.Context, db queryer, actor *auth.User) error {
	if err := db.QueryRowContext(ctx, "SELECT role,status FROM users WHERE id=?", actor.ID).Scan(&actor.Role, &actor.Status); err != nil {
		return err
	}
	if actor.Status != "active" || (actor.Role != "owner" && actor.Role != "admin") {
		return ErrDenied
	}
	return nil
}
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func mutationError(err error) error {
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrConflict
	}
	return err
}
func audit(ctx context.Context, tx *sql.Tx, actorID, action, resourceType, resourceID string) error {
	id, err := credentials.RandomToken(16)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO audit_events (id,actor_user_id,action,resource_type,resource_id,created_at) VALUES (?,?,?,?,?,?)", "aud_"+id, nullableString(actorID), action, resourceType, resourceID, time.Now().UnixMilli())
	return err
}
func formatTime(value int64) string { return time.UnixMilli(value).UTC().Format(time.RFC3339Nano) }

type scanner interface{ Scan(...any) error }

func scanConnection(row scanner) (Connection, error) {
	var item Connection
	var created, updated int64
	err := row.Scan(&item.ID, &item.Name, &item.Adapter, &item.BaseURL, &item.Enabled, &item.AllowPrivateNetwork, &item.TimeoutMS, &item.Preset, &item.CredentialState, &item.Revision, &created, &updated)
	item.Capabilities = availableCapabilities(item.Preset, item.Adapter)
	item.CapabilityDetails = capabilityDetails(item.Capabilities)
	item.AdapterLabel = adapterLabels[item.Adapter]
	item.CredentialRequired = presetCredentialRequired(item.Preset)
	item.CreatedAt, item.UpdatedAt = formatTime(created), formatTime(updated)
	return item, err
}
func scanPublicModel(row scanner) (PublicModel, error) {
	var item PublicModel
	var raw string
	err := row.Scan(&item.ID, &item.Label, &item.Description, &item.TargetConnectionID, &item.TargetModelID, &item.UpstreamID, &item.Adapter, &raw, &item.Active, &item.Revision, &item.RoutingStrategy, &item.FreeOnly)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &item.Capabilities)
		item.AdapterLabel = adapterLabels[item.Adapter]
		item.CapabilityDetails = capabilityDetails(item.Capabilities)
		item.RoutingPolicy = routingPolicy(item.Capabilities)
	}
	return item, err
}
