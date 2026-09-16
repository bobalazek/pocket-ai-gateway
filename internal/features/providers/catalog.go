package providers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
)

const (
	maxCatalogBytes = 1 << 20
	catalogPageSize = 50
)

type CatalogCandidate struct {
	Provider              string             `json:"provider"`
	ModelID               string             `json:"model_id"`
	Label                 string             `json:"label"`
	Capabilities          []string           `json:"capabilities"`
	CapabilityDetails     []CapabilityDetail `json:"capability_details,omitempty"`
	InputNanosPerMillion  *int64             `json:"input_nanos_per_million,omitempty"`
	OutputNanosPerMillion *int64             `json:"output_nanos_per_million,omitempty"`
	Free                  bool               `json:"free"`
	Source                string             `json:"source"`
	SourceVersion         string             `json:"source_version"`
	DiscoveredAt          string             `json:"discovered_at"`
}

type CatalogState struct {
	SourceURL            string  `json:"source_url"`
	SourceVersion        string  `json:"source_version"`
	LastCheckedAt        *string `json:"last_checked_at"`
	LastError            string  `json:"last_error"`
	RefreshEnabled       bool    `json:"refresh_enabled"`
	RefreshIntervalHours int64   `json:"refresh_interval_hours"`
}

type catalogDocument struct {
	Version string             `json:"version"`
	Models  []CatalogCandidate `json:"models"`
}

func (service *Service) Catalog(ctx context.Context, actor auth.User, cursor string) ([]CatalogCandidate, CatalogState, string, error) {
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return nil, CatalogState{}, "", err
	}
	afterProvider, afterModel, err := decodeCatalogCursor(cursor)
	if err != nil {
		return nil, CatalogState{}, "", errors.New("cursor must be valid")
	}
	rows, err := service.database.QueryContext(ctx, `SELECT provider,model_id,label,capabilities_json,input_nanos_per_million,output_nanos_per_million,free,source,source_version,discovered_at
		FROM catalog_candidates
		WHERE (? = '' OR provider > ? OR (provider = ? AND model_id > ?))
		ORDER BY provider,model_id LIMIT ?`, afterProvider, afterProvider, afterProvider, afterModel, catalogPageSize+1)
	if err != nil {
		return nil, CatalogState{}, "", err
	}
	items := make([]CatalogCandidate, 0, catalogPageSize)
	next := ""
	for rows.Next() {
		var item CatalogCandidate
		var raw string
		var input, output sql.NullInt64
		var discovered int64
		if err := rows.Scan(&item.Provider, &item.ModelID, &item.Label, &raw, &input, &output, &item.Free, &item.Source, &item.SourceVersion, &discovered); err != nil {
			return nil, CatalogState{}, "", err
		}
		if len(items) == catalogPageSize {
			last := items[len(items)-1]
			next = encodeCatalogCursor(last.Provider, last.ModelID)
			break
		}
		_ = json.Unmarshal([]byte(raw), &item.Capabilities)
		item.CapabilityDetails = capabilityDetails(item.Capabilities)
		if input.Valid {
			item.InputNanosPerMillion = &input.Int64
		}
		if output.Valid {
			item.OutputNanosPerMillion = &output.Int64
		}
		item.DiscoveredAt = time.UnixMilli(discovered).UTC().Format(time.RFC3339Nano)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, CatalogState{}, "", err
	}
	if err := rows.Close(); err != nil {
		return nil, CatalogState{}, "", err
	}
	state, err := service.catalogState(ctx)
	return items, state, next, err
}

func encodeCatalogCursor(provider, modelID string) string {
	raw, _ := json.Marshal([2]string{provider, modelID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCatalogCursor(value string) (string, string, error) {
	if value == "" {
		return "", "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", "", err
	}
	var cursor [2]string
	if err = json.Unmarshal(raw, &cursor); err != nil || cursor[0] == "" || cursor[1] == "" {
		return "", "", errors.New("invalid cursor")
	}
	return cursor[0], cursor[1], nil
}

func (service *Service) ConfigureCatalog(ctx context.Context, actor auth.User, sourceURL string, enabled bool, interval int64) (CatalogState, error) {
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return CatalogState{}, err
	}
	if sourceURL != "" {
		if _, err := catalogURL(sourceURL); err != nil {
			return CatalogState{}, err
		}
	}
	if interval < 1 || interval > 720 {
		return CatalogState{}, errors.New("refresh_interval_hours must be between 1 and 720")
	}
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return CatalogState{}, err
	}
	defer tx.Rollback()
	if err = requireManager(ctx, tx, &actor); err != nil {
		return CatalogState{}, err
	}
	var currentSource string
	if err = tx.QueryRowContext(ctx, "SELECT source_url FROM catalog_refresh_state WHERE singleton=1").Scan(&currentSource); err != nil {
		return CatalogState{}, err
	}
	if currentSource != sourceURL {
		if _, err = tx.ExecContext(ctx, "DELETE FROM catalog_candidates"); err != nil {
			return CatalogState{}, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE catalog_refresh_state SET source_url=?,source_version='',last_checked_at=NULL,last_error='',refresh_enabled=?,refresh_interval_hours=? WHERE singleton=1", sourceURL, enabled, interval)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE catalog_refresh_state SET refresh_enabled=?,refresh_interval_hours=? WHERE singleton=1", enabled, interval)
	}
	if err != nil {
		return CatalogState{}, err
	}
	if err = audit(ctx, tx, actor.ID, "catalog.configure", "catalog", "source"); err != nil {
		return CatalogState{}, err
	}
	if err = tx.Commit(); err != nil {
		return CatalogState{}, err
	}
	return service.catalogState(ctx)
}

func (service *Service) RefreshCatalog(ctx context.Context, actor auth.User) (CatalogState, error) {
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return CatalogState{}, err
	}
	state, err := service.catalogState(ctx)
	if err != nil {
		return CatalogState{}, err
	}
	if state.SourceURL == "" {
		return state, errors.New("catalog source_url is required")
	}
	return service.refreshCatalog(ctx, state, &actor)
}

func (service *Service) RunCatalogRefresh(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		state, err := service.catalogState(ctx)
		if err == nil && state.RefreshEnabled && state.SourceURL != "" && catalogRefreshDue(state) {
			_, _ = service.refreshCatalog(ctx, state, nil)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func catalogRefreshDue(state CatalogState) bool {
	if state.LastCheckedAt == nil {
		return true
	}
	checked, err := time.Parse(time.RFC3339Nano, *state.LastCheckedAt)
	return err != nil || time.Since(checked) >= time.Duration(state.RefreshIntervalHours)*time.Hour
}

func (service *Service) refreshCatalog(ctx context.Context, state CatalogState, actor *auth.User) (CatalogState, error) {
	document, fetchErr := fetchCatalog(ctx, state.SourceURL)
	now := time.Now().UnixMilli()
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return state, err
	}
	defer tx.Rollback()
	if actor != nil {
		if err = requireManager(ctx, tx, actor); err != nil {
			return state, err
		}
	}
	var currentSource string
	if err = tx.QueryRowContext(ctx, "SELECT source_url FROM catalog_refresh_state WHERE singleton=1").Scan(&currentSource); err != nil {
		return state, err
	}
	if currentSource != state.SourceURL {
		return state, ErrConflict
	}
	actorID := ""
	if actor != nil {
		actorID = actor.ID
	}
	if fetchErr != nil {
		if _, err = tx.ExecContext(ctx, "UPDATE catalog_refresh_state SET last_checked_at=?,last_error=? WHERE singleton=1", now, truncate(fetchErr.Error(), 500)); err != nil {
			return state, err
		}
		if err = audit(ctx, tx, actorID, "catalog.refresh.failed", "catalog", "source"); err != nil {
			return state, err
		}
		if err = tx.Commit(); err != nil {
			return state, err
		}
		updated, stateErr := service.catalogState(ctx)
		if stateErr != nil {
			return updated, stateErr
		}
		return updated, fetchErr
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM catalog_candidates"); err != nil {
		return state, err
	}
	for _, item := range document.Models {
		raw, _ := json.Marshal(item.Capabilities)
		if _, err = tx.ExecContext(ctx, "INSERT INTO catalog_candidates (provider,model_id,label,capabilities_json,input_nanos_per_million,output_nanos_per_million,free,source,source_version,discovered_at) VALUES (?,?,?,?,?,?,?,?,?,?)", item.Provider, item.ModelID, item.Label, string(raw), item.InputNanosPerMillion, item.OutputNanosPerMillion, item.Free, state.SourceURL, document.Version, now); err != nil {
			return state, err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE catalog_refresh_state SET source_version=?,last_checked_at=?,last_error='' WHERE singleton=1", document.Version, now); err != nil {
		return state, err
	}
	if err = audit(ctx, tx, actorID, "catalog.refresh", "catalog", document.Version); err != nil {
		return state, err
	}
	if err = tx.Commit(); err != nil {
		return state, err
	}
	return service.catalogState(ctx)
}

func (service *Service) catalogState(ctx context.Context) (CatalogState, error) {
	var item CatalogState
	var checked sql.NullInt64
	if err := service.database.QueryRowContext(ctx, "SELECT source_url,source_version,last_checked_at,last_error,refresh_enabled,refresh_interval_hours FROM catalog_refresh_state WHERE singleton=1").Scan(&item.SourceURL, &item.SourceVersion, &checked, &item.LastError, &item.RefreshEnabled, &item.RefreshIntervalHours); err != nil {
		return item, err
	}
	if checked.Valid {
		value := time.UnixMilli(checked.Int64).UTC().Format(time.RFC3339Nano)
		item.LastCheckedAt = &value
	}
	return item, nil
}

func fetchCatalog(ctx context.Context, source string) (catalogDocument, error) {
	parsed, err := catalogURL(source)
	if err != nil {
		return catalogDocument{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return catalogDocument{}, err
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		_, err := catalogURL(next.URL.String())
		return err
	}}
	response, err := client.Do(request)
	if err != nil {
		return catalogDocument{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return catalogDocument{}, errors.New("catalog source returned " + response.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxCatalogBytes+1))
	if err != nil || len(raw) > maxCatalogBytes {
		return catalogDocument{}, errors.New("catalog response exceeds 1 MiB")
	}
	return parseCatalog(raw)
}

func parseCatalog(raw []byte) (catalogDocument, error) {
	var doc catalogDocument
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return doc, errors.New("invalid catalog document")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return doc, errors.New("invalid catalog document")
	}
	if doc.Version == "" || len(doc.Version) > 100 || len(doc.Models) > 5000 {
		return doc, errors.New("catalog version and at most 5000 models are required")
	}
	seen := map[string]bool{}
	for i := range doc.Models {
		item := &doc.Models[i]
		item.Provider = strings.TrimSpace(item.Provider)
		item.ModelID = strings.TrimSpace(item.ModelID)
		item.Label = strings.TrimSpace(item.Label)
		item.Capabilities = normalizeCapabilities(item.Capabilities)
		key := item.Provider + "\x00" + item.ModelID
		if item.Provider == "" || item.ModelID == "" || item.Label == "" || len(item.Provider) > 100 || len(item.ModelID) > 300 || len(item.Label) > 300 || len(item.Capabilities) == 0 || !validCapabilities(item.Capabilities) || seen[key] || (item.Free && (item.InputNanosPerMillion == nil || item.OutputNanosPerMillion == nil || *item.InputNanosPerMillion != 0 || *item.OutputNanosPerMillion != 0)) {
			return doc, errors.New("catalog model metadata is invalid")
		}
		seen[key] = true
	}
	return doc, nil
}

func catalogURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Hostname() != "raw.githubusercontent.com" && parsed.Hostname() != "github.com") {
		return nil, errors.New("catalog source must be an HTTPS GitHub URL without credentials, query, or fragment")
	}
	return parsed, nil
}

func ValidateCatalogURL(value string) error {
	if value == "" {
		return nil
	}
	_, err := catalogURL(value)
	return err
}
func truncate(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
