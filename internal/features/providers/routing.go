package providers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

const (
	latencyMinimumSamples = 10
	latencyFreshness      = 15 * time.Minute
	circuitDuration       = 30 * time.Second
)

var routeStrategyOrder = []string{"fixed", "ordered_fallback", "weighted", "lowest_cost", "lowest_latency"}
var routeStrategies = map[string]bool{"fixed": true, "ordered_fallback": true, "weighted": true, "lowest_cost": true, "lowest_latency": true}
var routeStrategyTargetLimits = map[string]int{"fixed": 1, "ordered_fallback": 32, "weighted": 32, "lowest_cost": 32, "lowest_latency": 32}
var routeStrategyLabels = map[string]string{"fixed": "Fixed target", "ordered_fallback": "Ordered fallback", "weighted": "Weighted", "lowest_cost": "Lowest estimated cost", "lowest_latency": "Lowest observed latency"}
var routePriorityField = NumberFieldPolicy{Label: "Priority", Minimum: 1, Maximum: 1000, Default: 1}
var routeWeightField = NumberFieldPolicy{Label: "Weight", Minimum: 1, Maximum: 10000, Default: 1}

func ValidRouteStrategy(value string) bool { return routeStrategies[value] }

type RoutingPolicy struct {
	AllowedStrategies    []string              `json:"allowed_strategies"`
	MaxTargetsByStrategy map[string]int        `json:"max_targets_by_strategy"`
	FreeOnlyAllowed      bool                  `json:"free_only_allowed"`
	FreeOnlyLabel        string                `json:"free_only_label"`
	PriorityField        NumberFieldPolicy     `json:"priority_field"`
	WeightField          NumberFieldPolicy     `json:"weight_field"`
	Strategies           []RouteStrategyOption `json:"strategies"`
}

type RouteStrategyOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type NumberFieldPolicy struct {
	Label   string `json:"label"`
	Minimum int64  `json:"min"`
	Maximum int64  `json:"max"`
	Default int64  `json:"default"`
}

func routingPolicy(capabilities []string) RoutingPolicy {
	allowed := routeStrategyOrder
	if containsString(capabilities, "embeddings") {
		allowed = []string{"fixed"}
	}
	limits := make(map[string]int, len(allowed))
	strategies := make([]RouteStrategyOption, 0, len(allowed))
	for _, strategy := range allowed {
		limits[strategy] = routeStrategyTargetLimits[strategy]
		strategies = append(strategies, RouteStrategyOption{ID: strategy, Label: routeStrategyLabels[strategy]})
	}
	return RoutingPolicy{
		AllowedStrategies: append([]string(nil), allowed...), MaxTargetsByStrategy: limits, FreeOnlyAllowed: true,
		FreeOnlyLabel: "Require verified zero provider pricing",
		PriorityField: routePriorityField,
		WeightField:   routeWeightField,
		Strategies:    strategies,
	}
}

type RouteTarget struct {
	UpstreamModelID    string   `json:"upstream_model_id"`
	ConnectionID       string   `json:"connection_id"`
	UpstreamID         string   `json:"upstream_id"`
	Adapter            string   `json:"adapter"`
	Capabilities       []string `json:"capabilities"`
	Priority           int64    `json:"priority"`
	Weight             int64    `json:"weight"`
	Enabled            bool     `json:"enabled"`
	EstimatedCostUSD   *string  `json:"estimated_cost_usd,omitempty"`
	LatencyMS          *int64   `json:"latency_ms,omitempty"`
	SampleCount        int64    `json:"sample_count"`
	CircuitOpenUntil   *string  `json:"circuit_open_until,omitempty"`
	target             Target
	estimatedCostNanos *int64
	priceVersionID     string
	circuitUntil       *int64
}

type RouteRejection struct {
	UpstreamModelID string `json:"upstream_model_id"`
	ConnectionID    string `json:"connection_id"`
	Reason          string `json:"reason"`
	Message         string `json:"message"`
}

type RoutePlan struct {
	ModelID          string           `json:"model_id"`
	Strategy         string           `json:"strategy"`
	FreeOnly         bool             `json:"free_only"`
	SelectionReason  string           `json:"selection_reason"`
	SelectionMessage string           `json:"selection_message"`
	Targets          []RouteTarget    `json:"targets"`
	Rejected         []RouteRejection `json:"rejected"`
}

type RouteOptions struct {
	Operation             string
	Streaming             bool
	EstimatedInputTokens  int64
	EstimatedOutputTokens int64
	QuoteAt               int64
	Seed                  string
	AllowsConnection      func(string) bool
	Eligibility           func(Target) (bool, string)
}

type StaticEligibilityInput struct {
	Dialect        string
	Capability     string
	Operation      string
	Streaming      bool
	OpaqueMedia    bool
	ImageStreaming bool
}

func StaticTargetEligibility(target Target, input StaticEligibilityInput) (bool, string) {
	native := NativeTarget(input.Dialect, target.Adapter)
	if input.Operation == "interactions" {
		if !native || target.Preset != "gemini" {
			return false, "interactions_native_gemini_required"
		}
	}
	if input.Capability == "" || !containsString(target.Capabilities, input.Capability) || !containsString(target.UpstreamCapabilities, input.Capability) {
		return false, "unsupported_capability"
	}
	if input.ImageStreaming && (target.Adapter != "openai" || target.Preset != "openai" || !strings.HasPrefix(target.UpstreamID, "gpt-image-") && target.UpstreamID != "chatgpt-image-latest") {
		return false, "image_stream_native_gpt_required"
	}
	if input.OpaqueMedia && target.RoutingStrategy == "lowest_cost" {
		return false, "cost_estimate_unavailable"
	}
	if input.OpaqueMedia && target.FreeOnly {
		return false, "free_price_contract_unavailable"
	}
	if !native && input.Capability != "chat" {
		return false, "translation_unsupported"
	}
	if input.Dialect == "anthropic" && input.Streaming && !native {
		return false, "anthropic_stream_usage_unavailable"
	}
	if target.Preset == "mistral" && input.Operation == "audio/transcriptions" && input.Streaming {
		return false, "preset_streaming_unsupported"
	}
	if input.Dialect == "responses_compact" && target.Preset != "openai" {
		return false, "preset_operation_unsupported"
	}
	return true, ""
}

func PreviewRouteEligibility(operation string, streaming bool) func(Target) (bool, string) {
	operation = strings.TrimLeft(strings.SplitN(operation, "?", 2)[0], "/")
	if separator := strings.LastIndex(operation, ":"); separator >= 0 {
		operation = operation[separator+1:]
	}
	capability := operationCapability(operation)
	dialect := "openai"
	switch operation {
	case "messages", "messages/count_tokens":
		dialect = "anthropic"
	case "generateContent", "streamGenerateContent", "countTokens", "embedContent", "batchEmbedContents", "interactions", "BidiGenerateContent":
		dialect = "gemini"
	case "responses":
		dialect = "responses"
	case "responses/compact":
		dialect = "responses_compact"
	case "systemone":
		dialect = "systemone"
	}
	opaqueMedia := capability == "images" || capability == "image_edit" || capability == "image_variation" || strings.HasPrefix(capability, "audio_")
	input := StaticEligibilityInput{Dialect: dialect, Capability: capability, Operation: operation, Streaming: streaming, OpaqueMedia: opaqueMedia, ImageStreaming: (operation == "images/generations" || operation == "images/edits") && streaming}
	return func(target Target) (bool, string) {
		if eligible, reason := StaticTargetEligibility(target, input); !eligible {
			return false, reason
		}
		targetOperation := operation
		if !NativeTarget(dialect, target.Adapter) {
			targetOperation = map[string]string{"anthropic": "messages", "gemini": "generateContent", "openai": "chat/completions", "openai_compatible": "chat/completions"}[target.Adapter]
		}
		if targetOperation == "" || !PresetSupports(target.Preset, targetOperation) {
			return false, "preset_operation_unsupported"
		}
		return true, ""
	}
}

func NativeTarget(dialect, adapter string) bool {
	return dialect == adapter || (dialect == "openai" || dialect == "systemone") && adapter == "openai_compatible" || (dialect == "responses" || dialect == "responses_compact") && (adapter == "openai" || adapter == "openai_compatible")
}

type RouteTargetInput struct {
	UpstreamModelID string `json:"upstream_model_id"`
	Priority        int64  `json:"priority"`
	Weight          int64  `json:"weight"`
	Enabled         bool   `json:"enabled"`
}

type RouteConfigInput struct {
	Strategy string             `json:"strategy"`
	FreeOnly bool               `json:"free_only"`
	Targets  []RouteTargetInput `json:"targets"`
}

type ManagedModelsView struct {
	Models           []PublicModel                 `json:"data"`
	Routes           map[string][]RouteTargetInput `json:"routes"`
	AvailableTargets map[string][]UpstreamModel    `json:"available_targets"`
	PublishTargets   []UpstreamModel               `json:"publish_targets"`
}

func (service *Service) ManagedModels(ctx context.Context, actor auth.User, search string) (ManagedModelsView, error) {
	view := ManagedModelsView{
		Routes:           map[string][]RouteTargetInput{},
		AvailableTargets: map[string][]UpstreamModel{},
	}
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return view, err
	}
	models, err := service.listManagedPublicModels(ctx, search)
	if err != nil {
		return view, err
	}
	targets, err := service.availableRouteTargets(ctx, nil)
	if err != nil {
		return view, err
	}
	modelByID := make(map[string]PublicModel, len(models))
	for _, model := range models {
		modelByID[model.ID] = model
		view.Routes[model.ID] = []RouteTargetInput{}
		view.AvailableTargets[model.ID] = []UpstreamModel{}
		for _, target := range targets {
			if subset(model.Capabilities, target.Capabilities) {
				view.AvailableTargets[model.ID] = append(view.AvailableTargets[model.ID], target)
			}
		}
	}
	rows, err := service.database.QueryContext(ctx, "SELECT public_model_id,upstream_model_id,priority,weight,enabled FROM public_model_targets ORDER BY public_model_id,priority")
	if err != nil {
		return view, err
	}
	defer rows.Close()
	for rows.Next() {
		var publicID string
		var target RouteTargetInput
		if err := rows.Scan(&publicID, &target.UpstreamModelID, &target.Priority, &target.Weight, &target.Enabled); err != nil {
			return view, err
		}
		if _, ok := modelByID[publicID]; ok {
			view.Routes[publicID] = append(view.Routes[publicID], target)
		}
	}
	if err := rows.Err(); err != nil {
		return view, err
	}
	view.Models = models
	view.PublishTargets = targets
	return view, nil
}

func (service *Service) RouteConfig(ctx context.Context, actor auth.User, publicID string) (PublicModel, []RouteTargetInput, []UpstreamModel, error) {
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return PublicModel{}, nil, nil, err
	}
	model, err := service.ResolvePublicModel(ctx, publicID)
	if err != nil {
		return PublicModel{}, nil, nil, err
	}
	available, err := service.availableRouteTargets(ctx, model.Capabilities)
	if err != nil {
		return PublicModel{}, nil, nil, err
	}
	rows, err := service.database.QueryContext(ctx, "SELECT upstream_model_id,priority,weight,enabled FROM public_model_targets WHERE public_model_id=? ORDER BY priority", publicID)
	if err != nil {
		return PublicModel{}, nil, nil, err
	}
	defer rows.Close()
	var targets []RouteTargetInput
	for rows.Next() {
		var target RouteTargetInput
		if err := rows.Scan(&target.UpstreamModelID, &target.Priority, &target.Weight, &target.Enabled); err != nil {
			return PublicModel{}, nil, nil, err
		}
		targets = append(targets, target)
	}
	return model, targets, available, rows.Err()
}

func (service *Service) availableRouteTargets(ctx context.Context, capabilities []string) ([]UpstreamModel, error) {
	rows, err := service.database.QueryContext(ctx, "SELECT upstream_models.id,upstream_models.connection_id,upstream_models.upstream_id,upstream_models.capabilities_json,upstream_models.active FROM upstream_models JOIN provider_connections ON provider_connections.id=upstream_models.connection_id WHERE upstream_models.active=1 AND provider_connections.enabled=1 ORDER BY upstream_models.upstream_id,upstream_models.id")
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
		if json.Unmarshal([]byte(raw), &item.Capabilities) != nil || !subset(capabilities, item.Capabilities) {
			continue
		}
		item.CapabilityDetails = capabilityDetails(item.Capabilities)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (service *Service) ConfigureRoute(ctx context.Context, actor auth.User, publicID string, revision int64, input RouteConfigInput) (PublicModel, error) {
	service.dispatch.Lock()
	defer service.dispatch.Unlock()
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return PublicModel{}, err
	}
	if !routeStrategies[input.Strategy] || len(input.Targets) == 0 || len(input.Targets) > 32 {
		return PublicModel{}, errors.New("a supported strategy and 1-32 targets are required")
	}
	seenModels, seenPriorities := map[string]bool{}, map[int64]bool{}
	enabled := 0
	for index := range input.Targets {
		target := &input.Targets[index]
		if target.Weight == 0 {
			target.Weight = 1
		}
		if target.Priority < routePriorityField.Minimum || target.Priority > routePriorityField.Maximum || target.Weight < routeWeightField.Minimum || target.Weight > routeWeightField.Maximum || target.UpstreamModelID == "" || seenModels[target.UpstreamModelID] || seenPriorities[target.Priority] {
			return PublicModel{}, errors.New("route targets require unique models and priorities with valid weights")
		}
		seenModels[target.UpstreamModelID], seenPriorities[target.Priority] = true, true
		if target.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return PublicModel{}, errors.New("at least one route target must be enabled")
	}
	var publicCapabilitiesJSON string
	if err := service.database.QueryRowContext(ctx, "SELECT capabilities_json FROM public_models WHERE id=? AND revision=?", publicID, revision).Scan(&publicCapabilitiesJSON); errors.Is(err, sql.ErrNoRows) {
		return PublicModel{}, ErrConflict
	} else if err != nil {
		return PublicModel{}, err
	}
	var publicCapabilities []string
	if err := json.Unmarshal([]byte(publicCapabilitiesJSON), &publicCapabilities); err != nil {
		return PublicModel{}, err
	}
	policy := routingPolicy(publicCapabilities)
	maximum, allowed := policy.MaxTargetsByStrategy[input.Strategy]
	if !allowed || len(input.Targets) > maximum || enabled > maximum {
		return PublicModel{}, errors.New("the selected routing strategy does not allow this target configuration")
	}
	if input.FreeOnly && !policy.FreeOnlyAllowed {
		return PublicModel{}, errors.New("verified-free routing is unavailable for this model capability set")
	}
	for _, target := range input.Targets {
		var capabilitiesJSON string
		if err := service.database.QueryRowContext(ctx, "SELECT capabilities_json FROM upstream_models WHERE id=? AND active=1", target.UpstreamModelID).Scan(&capabilitiesJSON); errors.Is(err, sql.ErrNoRows) {
			return PublicModel{}, ErrNotFound
		} else if err != nil {
			return PublicModel{}, err
		}
		var capabilities []string
		if json.Unmarshal([]byte(capabilitiesJSON), &capabilities) != nil || !subset(publicCapabilities, capabilities) {
			return PublicModel{}, errors.New("every route target must support the public model capabilities")
		}
	}
	sort.Slice(input.Targets, func(i, j int) bool { return input.Targets[i].Priority < input.Targets[j].Priority })
	primary := input.Targets[0].UpstreamModelID
	for _, target := range input.Targets {
		if target.Enabled {
			primary = target.UpstreamModelID
			break
		}
	}
	now := time.Now().UnixMilli()
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return PublicModel{}, err
	}
	defer tx.Rollback()
	if err = requireManager(ctx, tx, &actor); err != nil {
		return PublicModel{}, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE public_models SET routing_strategy=?,free_only=?,target_model_id=?,target_connection_id=(SELECT connection_id FROM upstream_models WHERE id=?),revision=revision+1,updated_at=? WHERE id=? AND revision=?", input.Strategy, input.FreeOnly, primary, primary, now, publicID, revision)
	if err != nil {
		return PublicModel{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return PublicModel{}, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM public_model_targets WHERE public_model_id=?", publicID); err != nil {
		return PublicModel{}, err
	}
	for _, target := range input.Targets {
		if _, err = tx.ExecContext(ctx, "INSERT INTO public_model_targets (public_model_id,upstream_model_id,priority,weight,enabled,created_at,updated_at) VALUES (?,?,?,?,?,?,?)", publicID, target.UpstreamModelID, target.Priority, target.Weight, target.Enabled, now, now); err != nil {
			return PublicModel{}, mutationError(err)
		}
	}
	if err = audit(ctx, tx, actor.ID, "model.route.update", "public_model", publicID); err != nil {
		return PublicModel{}, err
	}
	if err = tx.Commit(); err != nil {
		return PublicModel{}, err
	}
	return service.ResolvePublicModel(ctx, publicID)
}

func (service *Service) Route(ctx context.Context, publicID string, options RouteOptions) (RoutePlan, error) {
	var strategy string
	var freeOnly bool
	if err := service.database.QueryRowContext(ctx, "SELECT routing_strategy,free_only FROM public_models WHERE id=? AND active=1", publicID).Scan(&strategy, &freeOnly); errors.Is(err, sql.ErrNoRows) {
		return RoutePlan{}, ErrNotFound
	} else if err != nil {
		return RoutePlan{}, err
	}
	now := options.QuoteAt
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	weekMinute := usage.UTCMinuteOfWeek(time.UnixMilli(now))
	rows, err := service.database.QueryContext(ctx, `SELECT public_model_targets.upstream_model_id,upstream_models.connection_id,upstream_models.upstream_id,provider_connections.adapter,upstream_models.capabilities_json,public_model_targets.priority,public_model_targets.weight,public_model_targets.enabled,provider_connections.base_url,provider_connections.allow_private_network,provider_connections.timeout_ms,provider_connections.revision,provider_connections.preset,public_models.label,public_models.description,public_models.capabilities_json,public_models.revision,provider_credentials.ciphertext,provider_credentials.nonce,provider_credentials.external_ref,COALESCE(CASE WHEN provider_connections.preset='custom' AND provider_connections.adapter='openai_compatible' THEN provider_adapter_scripts.request_script END,''),COALESCE(CASE WHEN provider_connections.preset='custom' AND provider_connections.adapter='openai_compatible' THEN provider_adapter_scripts.response_script END,''),price_versions.id,price_versions.input_nanos_per_million,price_versions.output_nanos_per_million,price_versions.cache_read_nanos_per_million,price_versions.source,price_versions.created_at,route_observations.success_count,route_observations.ewma_first_byte_ms,route_observations.ewma_total_ms,route_observations.circuit_open_until,route_observations.updated_at
		FROM public_model_targets JOIN public_models ON public_models.id=public_model_targets.public_model_id JOIN upstream_models ON upstream_models.id=public_model_targets.upstream_model_id JOIN provider_connections ON provider_connections.id=upstream_models.connection_id LEFT JOIN provider_credentials ON provider_credentials.connection_id=provider_connections.id
		LEFT JOIN provider_adapter_scripts ON provider_adapter_scripts.connection_id=provider_connections.id
		LEFT JOIN price_versions ON price_versions.id=(SELECT id FROM price_versions candidate_price WHERE candidate_price.connection_id=provider_connections.id AND candidate_price.model_id=public_models.id AND candidate_price.effective_from<=? AND (candidate_price.effective_to IS NULL OR candidate_price.effective_to>?) AND (candidate_price.weekly_start_minute_utc IS NULL OR candidate_price.weekly_start_minute_utc<=? AND candidate_price.weekly_end_minute_utc>?) ORDER BY candidate_price.effective_from DESC,candidate_price.id DESC LIMIT 1)
		LEFT JOIN route_observations ON route_observations.upstream_model_id=upstream_models.id AND route_observations.operation=? AND route_observations.streaming=?
		WHERE public_model_targets.public_model_id=? AND public_model_targets.enabled=1 AND upstream_models.active=1 AND provider_connections.enabled=1 ORDER BY public_model_targets.priority`, now, now, weekMinute, weekMinute, options.Operation, options.Streaming, publicID)
	if err != nil {
		return RoutePlan{}, err
	}
	defer rows.Close()
	plan := RoutePlan{ModelID: publicID, Strategy: strategy, FreeOnly: freeOnly}
	for rows.Next() {
		var item RouteTarget
		var upstreamCaps, publicCaps string
		var ciphertext, nonce []byte
		var external sql.NullString
		var baseURL, preset, label, description string
		var allowPrivate bool
		var timeout, connectionRevision, modelRevision int64
		var inputRate, outputRate, cacheReadRate, sampleCount, first, total, until, observedAt sql.NullInt64
		var priceVersion, priceSource sql.NullString
		var priceCreated sql.NullInt64
		var requestScript, responseScript string
		if err := rows.Scan(&item.UpstreamModelID, &item.ConnectionID, &item.UpstreamID, &item.Adapter, &upstreamCaps, &item.Priority, &item.Weight, &item.Enabled, &baseURL, &allowPrivate, &timeout, &connectionRevision, &preset, &label, &description, &publicCaps, &modelRevision, &ciphertext, &nonce, &external, &requestScript, &responseScript, &priceVersion, &inputRate, &outputRate, &cacheReadRate, &priceSource, &priceCreated, &sampleCount, &first, &total, &until, &observedAt); err != nil {
			return RoutePlan{}, err
		}
		item.SampleCount = sampleCount.Int64
		_ = json.Unmarshal([]byte(upstreamCaps), &item.Capabilities)
		var publicCapabilities []string
		_ = json.Unmarshal([]byte(publicCaps), &publicCapabilities)
		item.target = Target{PublicModel: PublicModel{ID: publicID, Label: label, Description: description, TargetConnectionID: item.ConnectionID, TargetModelID: item.UpstreamModelID, UpstreamID: item.UpstreamID, Adapter: item.Adapter, Capabilities: publicCapabilities, Active: true, Revision: modelRevision, RoutingStrategy: strategy, FreeOnly: freeOnly}, UpstreamCapabilities: item.Capabilities, BaseURL: baseURL, AllowPrivateNetwork: allowPrivate, TimeoutMS: timeout, ConnectionRevision: connectionRevision, Preset: preset, AdapterRequestScript: requestScript, AdapterResponseScript: responseScript}
		if options.AllowsConnection != nil && !options.AllowsConnection(item.ConnectionID) {
			plan.Rejected = append(plan.Rejected, routeRejection(item.UpstreamModelID, item.ConnectionID, "connection_not_granted"))
			continue
		}
		if options.Eligibility != nil {
			if eligible, reason := options.Eligibility(item.target); !eligible {
				plan.Rejected = append(plan.Rejected, routeRejection(item.UpstreamModelID, item.ConnectionID, reason))
				continue
			}
		}
		if external.Valid {
			item.target.Credential, item.target.BearerCredential, err = resolveExternalCredential(external.String)
		} else if len(ciphertext) > 0 {
			item.target.Credential, err = openSecret(service.key, item.ConnectionID, ciphertext, nonce)
		}
		if err != nil || item.target.Credential == "" && preset != "ollama" {
			plan.Rejected = append(plan.Rejected, routeRejection(item.UpstreamModelID, item.ConnectionID, "credential_unavailable"))
			continue
		}
		if inputRate.Valid && outputRate.Valid {
			item.priceVersionID = priceVersion.String
			cost, calcErr := usage.CalculateCost(options.EstimatedInputTokens, options.EstimatedOutputTokens, usage.ConservativeInputRate(inputRate.Int64, nullableInt64(cacheReadRate)), outputRate.Int64)
			if calcErr != nil {
				return RoutePlan{}, calcErr
			}
			item.estimatedCostNanos = &cost
			value := usage.FormatUSD(cost)
			item.EstimatedCostUSD = &value
		}
		if freeOnly && (!inputRate.Valid || !outputRate.Valid || !priceSource.Valid || !priceCreated.Valid || !usage.VerifiedFreePrice(inputRate.Int64, outputRate.Int64, nullableInt64(cacheReadRate), priceSource.String, priceCreated.Int64, now)) {
			plan.Rejected = append(plan.Rejected, routeRejection(item.UpstreamModelID, item.ConnectionID, "not_verified_free"))
			continue
		}
		fresh := observedAt.Valid && observedAt.Int64 >= now-latencyFreshness.Milliseconds()
		if fresh && options.Streaming && first.Valid {
			value := first.Int64
			item.LatencyMS = &value
		} else if fresh && total.Valid {
			value := total.Int64
			item.LatencyMS = &value
		}
		if until.Valid {
			item.circuitUntil = &until.Int64
			value := time.UnixMilli(until.Int64).UTC().Format(time.RFC3339Nano)
			item.CircuitOpenUntil = &value
			if until.Int64 > now {
				plan.Rejected = append(plan.Rejected, routeRejection(item.UpstreamModelID, item.ConnectionID, "circuit_open"))
				continue
			}
		}
		plan.Targets = append(plan.Targets, item)
	}
	if err := rows.Err(); err != nil {
		return RoutePlan{}, err
	}
	plan.Targets, plan.Rejected, plan.SelectionReason = orderRoute(plan.Targets, plan.Rejected, strategy, options.Seed)
	if len(plan.Targets) == 0 {
		plan.SelectionMessage = "No eligible target"
		return plan, ErrNotFound
	}
	plan.SelectionMessage = routeSelectionMessage(plan.SelectionReason)
	return plan, nil
}

func routeSelectionMessage(reason string) string {
	strategy := strings.SplitN(reason, ":", 2)[0]
	return map[string]string{
		"fixed":                      "Fixed target selected",
		"ordered_fallback":           "First available target selected",
		"weighted":                   "Weighted target selected",
		"lowest_cost":                "Lowest estimated cost selected",
		"lowest_latency":             "Lowest observed latency selected",
		"lowest_latency_exploration": "Latency exploration target selected",
	}[strategy]
}

func routeRejectionMessage(reason string) string {
	if message := map[string]string{
		"anthropic_stream_usage_unavailable":    "Streaming usage is unavailable for this target",
		"cache_price_contract_unavailable":      "Cache pricing is unavailable for this route",
		"circuit_open":                          "Target is temporarily unavailable",
		"connection_not_granted":                "API key does not grant this connection",
		"cost_estimate_unavailable":             "Cost cannot be estimated for this operation",
		"credential_unavailable":                "Provider credential is unavailable",
		"free_price_contract_unavailable":       "Verified-free pricing is unavailable",
		"image_stream_native_gpt_required":      "Streaming images require a native GPT Image target",
		"interactions_native_gemini_required":   "Interactions require the built-in Gemini preset",
		"not_verified_free":                     "Target does not have verified zero pricing",
		"preset_operation_unsupported":          "Provider preset does not support this operation",
		"price_unknown":                         "Target price is unknown",
		"prompt_cache_native_required":          "Prompt caching requires the built-in Anthropic preset",
		"request_translation_unsupported":       "Request cannot be translated for this target",
		"translation_unsupported":               "This operation requires a native target",
		"unsupported_capability":                "Target does not support the required capability",
		"web_fetch_native_required":             "Web fetch requires the built-in Anthropic preset",
		"web_fetch_price_contract_unavailable":  "Web fetch pricing is unavailable for this route",
		"web_search_native_required":            "Web search requires a supported native preset",
		"web_search_price_contract_unavailable": "Web search pricing is unavailable for this route",
	}[reason]; message != "" {
		return message
	}
	return "Target is unavailable for this request"
}

func routeRejection(upstreamModelID, connectionID, reason string) RouteRejection {
	return RouteRejection{UpstreamModelID: upstreamModelID, ConnectionID: connectionID, Reason: reason, Message: routeRejectionMessage(reason)}
}

func orderRoute(items []RouteTarget, rejected []RouteRejection, strategy, seed string) ([]RouteTarget, []RouteRejection, string) {
	reason := strategy
	switch strategy {
	case "lowest_cost":
		kept := items[:0]
		for _, item := range items {
			if item.estimatedCostNanos == nil {
				rejected = append(rejected, routeRejection(item.UpstreamModelID, item.ConnectionID, "price_unknown"))
			} else {
				kept = append(kept, item)
			}
		}
		items = kept
		sort.SliceStable(items, func(i, j int) bool { return *items[i].estimatedCostNanos < *items[j].estimatedCostNanos })
	case "lowest_latency":
		sort.SliceStable(items, func(i, j int) bool {
			ai, aj := items[i].LatencyMS != nil && items[i].SampleCount >= latencyMinimumSamples, items[j].LatencyMS != nil && items[j].SampleCount >= latencyMinimumSamples
			if ai != aj {
				return ai
			}
			if ai && *items[i].LatencyMS != *items[j].LatencyMS {
				return *items[i].LatencyMS < *items[j].LatencyMS
			}
			return items[i].Priority < items[j].Priority
		})
		if len(items) > 1 {
			sum := sha256.Sum256([]byte(seed))
			if binary.BigEndian.Uint64(sum[:8])%20 == 0 {
				items[0], items[1] = items[1], items[0]
				reason = "lowest_latency_exploration"
			}
		}
	case "weighted":
		if len(items) > 1 {
			total := uint64(0)
			for _, item := range items {
				total += uint64(item.Weight)
			}
			sum := sha256.Sum256([]byte(seed))
			pick := binary.BigEndian.Uint64(sum[:8]) % total
			selected := 0
			for i, item := range items {
				if pick < uint64(item.Weight) {
					selected = i
					break
				}
				pick -= uint64(item.Weight)
			}
			items[0], items[selected] = items[selected], items[0]
		}
	}
	if strategy == "fixed" && len(items) > 1 {
		items = items[:1]
	}
	if len(items) > 0 {
		reason += ":" + items[0].UpstreamModelID
	}
	return items, rejected, reason
}

func (item RouteTarget) Target() Target         { return item.target }
func (item RouteTarget) PriceVersionID() string { return item.priceVersionID }

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func (service *Service) HasAvailableRouteTarget(ctx context.Context, publicID string, allowed func(string) bool, eligible func(Target) bool) bool {
	rows, err := service.database.QueryContext(ctx, `SELECT DISTINCT provider_connections.id,provider_connections.preset,provider_connections.adapter,public_models.capabilities_json,upstream_models.capabilities_json,public_models.routing_strategy,public_models.free_only,provider_credentials.ciphertext,provider_credentials.external_ref FROM public_model_targets JOIN public_models ON public_models.id=public_model_targets.public_model_id JOIN upstream_models ON upstream_models.id=public_model_targets.upstream_model_id JOIN provider_connections ON provider_connections.id=upstream_models.connection_id LEFT JOIN provider_credentials ON provider_credentials.connection_id=provider_connections.id WHERE public_model_targets.public_model_id=? AND public_models.active=1 AND public_model_targets.enabled=1 AND upstream_models.active=1 AND provider_connections.enabled=1`, publicID)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var id, preset, adapter, publicCapabilitiesJSON, upstreamCapabilitiesJSON, routingStrategy string
		var freeOnly bool
		var ciphertext []byte
		var external sql.NullString
		if rows.Scan(&id, &preset, &adapter, &publicCapabilitiesJSON, &upstreamCapabilitiesJSON, &routingStrategy, &freeOnly, &ciphertext, &external) != nil || !allowed(id) {
			continue
		}
		var publicCapabilities, upstreamCapabilities []string
		_ = json.Unmarshal([]byte(publicCapabilitiesJSON), &publicCapabilities)
		_ = json.Unmarshal([]byte(upstreamCapabilitiesJSON), &upstreamCapabilities)
		if eligible != nil && !eligible(Target{PublicModel: PublicModel{TargetConnectionID: id, Adapter: adapter, Capabilities: publicCapabilities, RoutingStrategy: routingStrategy, FreeOnly: freeOnly}, UpstreamCapabilities: upstreamCapabilities, Preset: preset}) {
			continue
		}
		credential, _, _ := resolveExternalCredential(external.String)
		if preset == "ollama" || len(ciphertext) > 0 || external.Valid && credential != "" {
			return true
		}
	}
	return false
}

func (service *Service) RecordRouteOutcome(ctx context.Context, target Target, operation string, streaming, success bool, firstByte, total time.Duration) error {
	now := time.Now().UnixMilli()
	firstMS, totalMS := firstByte.Milliseconds(), total.Milliseconds()
	succeeded := 0
	failed := 1
	consecutive := 1
	if success {
		succeeded, failed, consecutive = 1, 0, 0
	}
	_, err := service.database.ExecContext(ctx, `INSERT INTO route_observations (upstream_model_id,operation,streaming,success_count,failure_count,consecutive_failures,ewma_first_byte_ms,ewma_total_ms,circuit_open_until,updated_at) VALUES (?,?,?,?,?,?,?,?,NULL,?)
		ON CONFLICT(upstream_model_id,operation,streaming) DO UPDATE SET success_count=success_count+excluded.success_count,failure_count=failure_count+excluded.failure_count,consecutive_failures=CASE WHEN excluded.success_count=1 THEN 0 ELSE consecutive_failures+1 END,ewma_first_byte_ms=CASE WHEN excluded.success_count=1 THEN COALESCE((ewma_first_byte_ms*4+excluded.ewma_first_byte_ms)/5,excluded.ewma_first_byte_ms) ELSE ewma_first_byte_ms END,ewma_total_ms=CASE WHEN excluded.success_count=1 THEN COALESCE((ewma_total_ms*4+excluded.ewma_total_ms)/5,excluded.ewma_total_ms) ELSE ewma_total_ms END,circuit_open_until=CASE WHEN excluded.success_count=1 THEN NULL WHEN consecutive_failures+1>=3 THEN ? ELSE circuit_open_until END,updated_at=excluded.updated_at`, target.TargetModelID, operation, streaming, succeeded, failed, consecutive, firstMS, totalMS, now, now+circuitDuration.Milliseconds())
	return err
}
