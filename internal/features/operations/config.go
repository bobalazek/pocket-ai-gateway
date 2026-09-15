package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
)

const configFormat = 1

type ConfigBundle struct {
	Format         int                `json:"format"`
	ExportedAt     string             `json:"exported_at,omitempty"`
	Settings       Settings           `json:"settings"`
	Connections    []ConfigConnection `json:"connections"`
	UpstreamModels []ConfigUpstream   `json:"upstream_models"`
	PublicModels   []ConfigModel      `json:"public_models"`
	Targets        []ConfigTarget     `json:"targets"`
	Policies       []ConfigPolicy     `json:"policies"`
	Prices         []ConfigPrice      `json:"prices"`
	Catalog        ConfigCatalog      `json:"catalog"`
}

type ConfigConnection struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Adapter             string `json:"adapter"`
	BaseURL             string `json:"base_url"`
	Enabled             bool   `json:"enabled"`
	AllowPrivateNetwork bool   `json:"allow_private_network"`
	TimeoutMS           int64  `json:"timeout_ms"`
	Preset              string `json:"preset"`
}

type ConfigUpstream struct {
	ID           string   `json:"id"`
	ConnectionID string   `json:"connection_id"`
	UpstreamID   string   `json:"upstream_id"`
	Capabilities []string `json:"capabilities"`
	Active       bool     `json:"active"`
}

type ConfigModel struct {
	ID                 string   `json:"id"`
	Label              string   `json:"label"`
	Description        string   `json:"description"`
	TargetConnectionID string   `json:"target_connection_id"`
	TargetModelID      string   `json:"target_model_id"`
	Capabilities       []string `json:"capabilities"`
	Active             bool     `json:"active"`
	RoutingStrategy    string   `json:"routing_strategy"`
	FreeOnly           bool     `json:"free_only"`
}

type ConfigTarget struct {
	PublicModelID   string `json:"public_model_id"`
	UpstreamModelID string `json:"upstream_model_id"`
	Priority        int64  `json:"priority"`
	Weight          int64  `json:"weight"`
	Enabled         bool   `json:"enabled"`
}

type ConfigPolicy struct {
	ID               string `json:"id"`
	ScopeKind        string `json:"scope_kind"`
	ScopeID          string `json:"scope_id"`
	Metric           string `json:"metric"`
	Algorithm        string `json:"algorithm"`
	Period           string `json:"period"`
	WindowSeconds    int64  `json:"window_seconds"`
	LimitUnits       int64  `json:"limit_units"`
	RefillUnits      int64  `json:"refill_units"`
	RefillIntervalMS int64  `json:"refill_interval_ms"`
	Enabled          bool   `json:"enabled"`
}

type ConfigPrice struct {
	ID                    string `json:"id"`
	ConnectionID          string `json:"connection_id"`
	ModelID               string `json:"model_id"`
	InputNanosPerMillion  int64  `json:"input_nanos_per_million"`
	OutputNanosPerMillion int64  `json:"output_nanos_per_million"`
	Source                string `json:"source"`
	EffectiveFrom         int64  `json:"effective_from"`
	EffectiveTo           *int64 `json:"effective_to,omitempty"`
}

type ConfigCatalog struct {
	SourceURL            string `json:"source_url"`
	RefreshEnabled       bool   `json:"refresh_enabled"`
	RefreshIntervalHours int64  `json:"refresh_interval_hours"`
}

type ConfigPreview struct {
	Connections    int      `json:"connections"`
	UpstreamModels int      `json:"upstream_models"`
	PublicModels   int      `json:"public_models"`
	Targets        int      `json:"targets"`
	Policies       int      `json:"policies"`
	Prices         int      `json:"prices"`
	Warnings       []string `json:"warnings"`
}

func (service *Service) ExportConfig(ctx context.Context) (ConfigBundle, error) {
	settings, err := service.Settings(ctx)
	if err != nil {
		return ConfigBundle{}, err
	}
	settings.BackupKeyConfigured, settings.Revision, settings.UpdatedAt = false, 0, ""
	bundle := ConfigBundle{Format: configFormat, ExportedAt: time.Now().UTC().Format(time.RFC3339Nano), Settings: settings}
	queries := []struct {
		query string
		scan  func(*sql.Rows) error
	}{
		{`SELECT id,name,adapter,base_url,enabled,allow_private_network,timeout_ms,preset FROM provider_connections ORDER BY id`, func(rows *sql.Rows) error {
			var item ConfigConnection
			if err := rows.Scan(&item.ID, &item.Name, &item.Adapter, &item.BaseURL, &item.Enabled, &item.AllowPrivateNetwork, &item.TimeoutMS, &item.Preset); err != nil {
				return err
			}
			bundle.Connections = append(bundle.Connections, item)
			return nil
		}},
		{`SELECT id,connection_id,upstream_id,capabilities_json,active FROM upstream_models ORDER BY id`, func(rows *sql.Rows) error {
			var item ConfigUpstream
			var capabilities string
			if err := rows.Scan(&item.ID, &item.ConnectionID, &item.UpstreamID, &capabilities, &item.Active); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(capabilities), &item.Capabilities); err != nil {
				return err
			}
			bundle.UpstreamModels = append(bundle.UpstreamModels, item)
			return nil
		}},
		{`SELECT id,label,description,target_connection_id,target_model_id,capabilities_json,active,routing_strategy,free_only FROM public_models ORDER BY id`, func(rows *sql.Rows) error {
			var item ConfigModel
			var capabilities string
			if err := rows.Scan(&item.ID, &item.Label, &item.Description, &item.TargetConnectionID, &item.TargetModelID, &capabilities, &item.Active, &item.RoutingStrategy, &item.FreeOnly); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(capabilities), &item.Capabilities); err != nil {
				return err
			}
			bundle.PublicModels = append(bundle.PublicModels, item)
			return nil
		}},
		{`SELECT public_model_id,upstream_model_id,priority,weight,enabled FROM public_model_targets ORDER BY public_model_id,priority`, func(rows *sql.Rows) error {
			var item ConfigTarget
			if err := rows.Scan(&item.PublicModelID, &item.UpstreamModelID, &item.Priority, &item.Weight, &item.Enabled); err != nil {
				return err
			}
			bundle.Targets = append(bundle.Targets, item)
			return nil
		}},
		{`SELECT id,scope_kind,scope_id,metric,algorithm,period,window_seconds,limit_units,refill_units,refill_interval_ms,enabled FROM limit_policies ORDER BY id`, func(rows *sql.Rows) error {
			var item ConfigPolicy
			if err := rows.Scan(&item.ID, &item.ScopeKind, &item.ScopeID, &item.Metric, &item.Algorithm, &item.Period, &item.WindowSeconds, &item.LimitUnits, &item.RefillUnits, &item.RefillIntervalMS, &item.Enabled); err != nil {
				return err
			}
			bundle.Policies = append(bundle.Policies, item)
			return nil
		}},
		{`SELECT id,connection_id,model_id,input_nanos_per_million,output_nanos_per_million,source,effective_from,effective_to FROM price_versions ORDER BY id`, func(rows *sql.Rows) error {
			var item ConfigPrice
			var effective sql.NullInt64
			if err := rows.Scan(&item.ID, &item.ConnectionID, &item.ModelID, &item.InputNanosPerMillion, &item.OutputNanosPerMillion, &item.Source, &item.EffectiveFrom, &effective); err != nil {
				return err
			}
			if effective.Valid {
				item.EffectiveTo = &effective.Int64
			}
			bundle.Prices = append(bundle.Prices, item)
			return nil
		}},
	}
	for _, query := range queries {
		rows, err := service.store.SystemDB().QueryContext(ctx, query.query)
		if err != nil {
			return ConfigBundle{}, err
		}
		for rows.Next() {
			if err := query.scan(rows); err != nil {
				rows.Close()
				return ConfigBundle{}, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return ConfigBundle{}, err
		}
	}
	if err := service.store.SystemDB().QueryRowContext(ctx, `SELECT source_url,refresh_enabled,refresh_interval_hours FROM catalog_refresh_state WHERE singleton=1`).Scan(&bundle.Catalog.SourceURL, &bundle.Catalog.RefreshEnabled, &bundle.Catalog.RefreshIntervalHours); err != nil {
		return ConfigBundle{}, err
	}
	return bundle, nil
}

func PreviewConfig(bundle ConfigBundle) (ConfigPreview, error) {
	if bundle.Format != configFormat || len(bundle.Connections) > 1000 || len(bundle.UpstreamModels) > 10000 || len(bundle.PublicModels) > 10000 || len(bundle.Targets) > 50000 || len(bundle.Policies) > 10000 || len(bundle.Prices) > 100000 {
		return ConfigPreview{}, ErrInvalid
	}
	normalizeSettings(&bundle.Settings)
	if err := validateSettings(bundle.Settings); err != nil {
		return ConfigPreview{}, err
	}
	seen := map[string]bool{}
	for _, group := range [][]string{idsConnections(bundle.Connections), idsUpstream(bundle.UpstreamModels), idsModels(bundle.PublicModels), idsPolicies(bundle.Policies), idsPrices(bundle.Prices)} {
		for _, id := range group {
			if !validID(id) || seen[id] {
				return ConfigPreview{}, ErrInvalid
			}
			seen[id] = true
		}
	}
	connections, connectionPresets, upstream, models := map[string]bool{}, map[string]string{}, map[string]ConfigUpstream{}, map[string]ConfigModel{}
	for _, item := range bundle.Connections {
		connections[item.ID] = true
		validated, err := providers.ValidatePortableConnection(providers.ConnectionInput{Name: item.Name, Adapter: item.Adapter, BaseURL: item.BaseURL, Enabled: item.Enabled, AllowPrivateNetwork: item.AllowPrivateNetwork, TimeoutMS: item.TimeoutMS, Preset: item.Preset})
		if err != nil {
			return ConfigPreview{}, ErrInvalid
		}
		connectionPresets[item.ID] = validated.Preset
	}
	for _, item := range bundle.UpstreamModels {
		upstream[item.ID] = item
		capabilities, ok := providers.NormalizePortableCapabilities(item.Capabilities)
		if !connections[item.ConnectionID] || item.UpstreamID == "" || len(item.UpstreamID) > 300 || !ok || !providers.PresetSupportsModelCapabilities(connectionPresets[item.ConnectionID], item.UpstreamID, capabilities) {
			return ConfigPreview{}, ErrInvalid
		}
	}
	for _, item := range bundle.PublicModels {
		models[item.ID] = item
		target, targetExists := upstream[item.TargetModelID]
		capabilities, capabilitiesValid := providers.NormalizePortableCapabilities(item.Capabilities)
		if !providers.ValidPortableModelID(item.ID) || !connections[item.TargetConnectionID] || !targetExists || target.ConnectionID != item.TargetConnectionID || item.Label == "" || len(item.Label) > 200 || len(item.Description) > 1000 || !capabilitiesValid || !providers.ValidRouteStrategy(item.RoutingStrategy) || !capabilitySubset(capabilities, target.Capabilities) {
			return ConfigPreview{}, ErrInvalid
		}
	}
	targetCounts, enabledCounts := map[string]int{}, map[string]int{}
	primaryTargets := map[string]ConfigTarget{}
	targetModels, priorities := map[string]map[string]bool{}, map[string]map[int64]bool{}
	for _, item := range bundle.Targets {
		model, modelExists := models[item.PublicModelID]
		target, targetExists := upstream[item.UpstreamModelID]
		if !modelExists || !targetExists || item.Priority < 1 || item.Priority > 1000 || item.Weight < 1 || item.Weight > 10000 || !capabilitySubset(model.Capabilities, target.Capabilities) {
			return ConfigPreview{}, ErrInvalid
		}
		if targetModels[item.PublicModelID] == nil {
			targetModels[item.PublicModelID] = map[string]bool{}
			priorities[item.PublicModelID] = map[int64]bool{}
		}
		if targetModels[item.PublicModelID][item.UpstreamModelID] || priorities[item.PublicModelID][item.Priority] {
			return ConfigPreview{}, ErrInvalid
		}
		targetModels[item.PublicModelID][item.UpstreamModelID], priorities[item.PublicModelID][item.Priority] = true, true
		targetCounts[item.PublicModelID]++
		if item.Enabled {
			enabledCounts[item.PublicModelID]++
			if current, ok := primaryTargets[item.PublicModelID]; !ok || item.Priority < current.Priority {
				primaryTargets[item.PublicModelID] = item
			}
		}
	}
	for id, model := range models {
		primary := primaryTargets[id]
		if targetCounts[id] == 0 || targetCounts[id] > 32 || enabledCounts[id] == 0 || primary.UpstreamModelID != model.TargetModelID || upstream[primary.UpstreamModelID].ConnectionID != model.TargetConnectionID || model.RoutingStrategy == "fixed" && (targetCounts[id] != 1 || enabledCounts[id] != 1) || contains(model.Capabilities, "embeddings") && model.RoutingStrategy != "fixed" {
			return ConfigPreview{}, ErrInvalid
		}
	}
	for _, item := range bundle.Policies {
		if !validPolicy(item) {
			return ConfigPreview{}, ErrInvalid
		}
	}
	for _, item := range bundle.Prices {
		if !connections[item.ConnectionID] || models[item.ModelID].ID == "" || item.InputNanosPerMillion < 0 || item.OutputNanosPerMillion < 0 || strings.TrimSpace(item.Source) == "" || item.EffectiveFrom <= 0 || item.EffectiveTo != nil && *item.EffectiveTo <= item.EffectiveFrom {
			return ConfigPreview{}, ErrInvalid
		}
	}
	if bundle.Catalog.RefreshIntervalHours < 1 || bundle.Catalog.RefreshIntervalHours > 720 || providers.ValidateCatalogURL(bundle.Catalog.SourceURL) != nil {
		return ConfigPreview{}, ErrInvalid
	}
	warnings := []string{"Provider credentials, users, sessions, and API keys are intentionally excluded."}
	return ConfigPreview{len(bundle.Connections), len(bundle.UpstreamModels), len(bundle.PublicModels), len(bundle.Targets), len(bundle.Policies), len(bundle.Prices), warnings}, nil
}

func (service *Service) ImportConfig(ctx context.Context, actor string, bundle ConfigBundle) (ConfigPreview, error) {
	preview, err := PreviewConfig(bundle)
	if err != nil {
		return ConfigPreview{}, err
	}
	if service.providers != nil {
		unlock := service.providers.LockConfiguration()
		defer unlock()
	}
	tx, err := service.store.SystemDB().BeginTx(ctx, nil)
	if err != nil {
		return ConfigPreview{}, err
	}
	defer tx.Rollback()
	if err := requireOwner(ctx, tx, actor); err != nil {
		return ConfigPreview{}, err
	}
	now := time.Now().UnixMilli()
	for index, item := range bundle.Connections {
		validated, _ := providers.ValidatePortableConnection(providers.ConnectionInput{Name: item.Name, Adapter: item.Adapter, BaseURL: item.BaseURL, Enabled: item.Enabled, AllowPrivateNetwork: item.AllowPrivateNetwork, TimeoutMS: item.TimeoutMS, Preset: item.Preset})
		item.Name, item.Adapter, item.BaseURL, item.Enabled, item.AllowPrivateNetwork, item.TimeoutMS, item.Preset = validated.Name, validated.Adapter, validated.BaseURL, validated.Enabled, validated.AllowPrivateNetwork, validated.TimeoutMS, validated.Preset
		bundle.Connections[index] = item
		if _, err = tx.ExecContext(ctx, `DELETE FROM provider_credentials WHERE connection_id=? AND EXISTS (SELECT 1 FROM provider_connections WHERE id=? AND (adapter<>? OR base_url<>? OR preset<>?))`, item.ID, item.ID, item.Adapter, item.BaseURL, item.Preset); err != nil {
			return ConfigPreview{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO provider_connections(id,name,adapter,base_url,enabled,allow_private_network,timeout_ms,preset,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,adapter=excluded.adapter,base_url=excluded.base_url,enabled=excluded.enabled,allow_private_network=excluded.allow_private_network,timeout_ms=excluded.timeout_ms,preset=excluded.preset,revision=provider_connections.revision+1,updated_at=excluded.updated_at`, item.ID, item.Name, item.Adapter, item.BaseURL, item.Enabled, item.AllowPrivateNetwork, item.TimeoutMS, item.Preset, now, now); err != nil {
			return ConfigPreview{}, err
		}
	}
	for _, item := range bundle.UpstreamModels {
		normalized, _ := providers.NormalizePortableCapabilities(item.Capabilities)
		capabilities, _ := json.Marshal(normalized)
		if _, err = tx.ExecContext(ctx, `INSERT INTO upstream_models(id,connection_id,upstream_id,capabilities_json,active,created_at,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET connection_id=excluded.connection_id,upstream_id=excluded.upstream_id,capabilities_json=excluded.capabilities_json,active=excluded.active,updated_at=excluded.updated_at`, item.ID, item.ConnectionID, item.UpstreamID, string(capabilities), item.Active, now, now); err != nil {
			return ConfigPreview{}, err
		}
	}
	for _, connection := range bundle.Connections {
		rows, queryErr := tx.QueryContext(ctx, "SELECT upstream_id,capabilities_json FROM upstream_models WHERE connection_id=?", connection.ID)
		if queryErr != nil {
			return ConfigPreview{}, queryErr
		}
		for rows.Next() {
			var upstreamID, raw string
			var capabilities []string
			if queryErr = rows.Scan(&upstreamID, &raw); queryErr == nil {
				queryErr = json.Unmarshal([]byte(raw), &capabilities)
			}
			if queryErr != nil || !providers.PresetSupportsModelCapabilities(connection.Preset, upstreamID, capabilities) {
				rows.Close()
				return ConfigPreview{}, ErrInvalid
			}
		}
		queryErr = rows.Err()
		rows.Close()
		if queryErr != nil {
			return ConfigPreview{}, queryErr
		}
	}
	for _, item := range bundle.PublicModels {
		normalized, _ := providers.NormalizePortableCapabilities(item.Capabilities)
		capabilities, _ := json.Marshal(normalized)
		if _, err = tx.ExecContext(ctx, `INSERT INTO public_models(id,label,description,target_connection_id,target_model_id,capabilities_json,active,routing_strategy,free_only,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET label=excluded.label,description=excluded.description,target_connection_id=excluded.target_connection_id,target_model_id=excluded.target_model_id,capabilities_json=excluded.capabilities_json,active=excluded.active,routing_strategy=excluded.routing_strategy,free_only=excluded.free_only,revision=public_models.revision+1,updated_at=excluded.updated_at`, item.ID, item.Label, item.Description, item.TargetConnectionID, item.TargetModelID, string(capabilities), item.Active, item.RoutingStrategy, item.FreeOnly, now, now); err != nil {
			return ConfigPreview{}, err
		}
	}
	for _, item := range bundle.PublicModels {
		if _, err = tx.ExecContext(ctx, `DELETE FROM public_model_targets WHERE public_model_id=?`, item.ID); err != nil {
			return ConfigPreview{}, err
		}
	}
	for _, item := range bundle.Targets {
		if _, err = tx.ExecContext(ctx, `INSERT INTO public_model_targets(public_model_id,upstream_model_id,priority,weight,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, item.PublicModelID, item.UpstreamModelID, item.Priority, item.Weight, item.Enabled, now, now); err != nil {
			return ConfigPreview{}, err
		}
	}
	for _, item := range bundle.Policies {
		if _, err = tx.ExecContext(ctx, `INSERT INTO limit_policies(id,scope_kind,scope_id,metric,algorithm,period,window_seconds,limit_units,refill_units,refill_interval_ms,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET scope_kind=excluded.scope_kind,scope_id=excluded.scope_id,metric=excluded.metric,algorithm=excluded.algorithm,period=excluded.period,window_seconds=excluded.window_seconds,limit_units=excluded.limit_units,refill_units=excluded.refill_units,refill_interval_ms=excluded.refill_interval_ms,enabled=excluded.enabled,revision=limit_policies.revision+1,updated_at=excluded.updated_at`, item.ID, item.ScopeKind, item.ScopeID, item.Metric, item.Algorithm, item.Period, item.WindowSeconds, item.LimitUnits, item.RefillUnits, item.RefillIntervalMS, item.Enabled, now, now); err != nil {
			return ConfigPreview{}, err
		}
	}
	for _, item := range bundle.Prices {
		if _, err = tx.ExecContext(ctx, `INSERT INTO price_versions(id,connection_id,model_id,input_nanos_per_million,output_nanos_per_million,source,effective_from,effective_to,created_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, item.ID, item.ConnectionID, item.ModelID, item.InputNanosPerMillion, item.OutputNanosPerMillion, item.Source, item.EffectiveFrom, item.EffectiveTo, now); err != nil {
			return ConfigPreview{}, err
		}
	}
	normalizeSettings(&bundle.Settings)
	if _, err = tx.ExecContext(ctx, `UPDATE operation_settings SET backup_enabled=?,backup_interval_hours=?,backup_retention_count=?,backup_destination=?,local_directory=?,s3_endpoint=?,s3_region=?,s3_bucket=?,s3_prefix=?,s3_access_key_env=?,s3_secret_key_env=?,request_retention_days=?,audit_retention_days=?,revision=revision+1,updated_at=? WHERE singleton=1`, bundle.Settings.BackupEnabled, bundle.Settings.BackupIntervalHours, bundle.Settings.BackupRetention, bundle.Settings.BackupDestination, bundle.Settings.LocalDirectory, bundle.Settings.S3Endpoint, bundle.Settings.S3Region, bundle.Settings.S3Bucket, bundle.Settings.S3Prefix, bundle.Settings.S3AccessKeyEnv, bundle.Settings.S3SecretKeyEnv, bundle.Settings.RequestRetention, bundle.Settings.AuditRetention, now); err != nil {
		return ConfigPreview{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_refresh_state SET source_url=?,refresh_enabled=?,refresh_interval_hours=? WHERE singleton=1`, bundle.Catalog.SourceURL, bundle.Catalog.RefreshEnabled, bundle.Catalog.RefreshIntervalHours); err != nil {
		return ConfigPreview{}, err
	}
	if err = insertAudit(ctx, tx, actor, "config.import", "operations", "configuration", `{}`); err != nil {
		return ConfigPreview{}, err
	}
	return preview, tx.Commit()
}

func validID(value string) bool {
	return value != "" && len(value) <= 160 && !strings.ContainsAny(value, "\x00\r\n")
}

func capabilitySubset(required, available []string) bool {
	set := map[string]bool{}
	for _, value := range available {
		set[value] = true
	}
	for _, value := range required {
		if !set[value] {
			return false
		}
	}
	return true
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func validPolicy(item ConfigPolicy) bool {
	if item.LimitUnits <= 0 || item.WindowSeconds < 0 || item.RefillUnits < 0 || item.RefillIntervalMS < 0 {
		return false
	}
	if item.ScopeKind == "instance" && item.ScopeID != "" || item.ScopeKind != "instance" && item.ScopeID == "" {
		return false
	}
	if item.ScopeKind != "instance" && item.ScopeKind != "user" && item.ScopeKind != "key" && item.ScopeKind != "connection" {
		return false
	}
	switch item.Algorithm {
	case "token_bucket":
		return item.Period == "" && item.WindowSeconds == 0 && item.RefillUnits > 0 && item.RefillIntervalMS > 0 && (item.Metric == "requests" || item.Metric == "tokens")
	case "fixed_window":
		return item.Period == "" && item.WindowSeconds > 0 && item.RefillUnits == 0 && item.RefillIntervalMS == 0 && (item.Metric == "requests" || item.Metric == "tokens")
	case "quota":
		return contains([]string{"hour", "day", "week", "month", "lifetime"}, item.Period) && item.WindowSeconds == 0 && item.RefillUnits == 0 && item.RefillIntervalMS == 0 && contains([]string{"requests", "tokens", "spend"}, item.Metric)
	case "concurrency":
		return item.Period == "" && item.WindowSeconds == 0 && item.RefillUnits == 0 && item.RefillIntervalMS == 0 && item.Metric == "concurrency"
	case "ceiling":
		return item.Period == "" && item.WindowSeconds == 0 && item.RefillUnits == 0 && item.RefillIntervalMS == 0 && contains([]string{"body_bytes", "output_tokens", "batch_items"}, item.Metric)
	default:
		return false
	}
}
func idsConnections(items []ConfigConnection) []string {
	values := make([]string, len(items))
	for i, item := range items {
		values[i] = item.ID
	}
	return values
}
func idsUpstream(items []ConfigUpstream) []string {
	values := make([]string, len(items))
	for i, item := range items {
		values[i] = item.ID
	}
	return values
}
func idsModels(items []ConfigModel) []string {
	values := make([]string, len(items))
	for i, item := range items {
		values[i] = item.ID
	}
	return values
}
func idsPolicies(items []ConfigPolicy) []string {
	values := make([]string, len(items))
	for i, item := range items {
		values[i] = item.ID
	}
	return values
}
func idsPrices(items []ConfigPrice) []string {
	values := make([]string, len(items))
	for i, item := range items {
		values[i] = item.ID
	}
	return values
}
