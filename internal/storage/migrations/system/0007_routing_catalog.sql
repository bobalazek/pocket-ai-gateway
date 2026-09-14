ALTER TABLE provider_connections ADD COLUMN preset TEXT NOT NULL DEFAULT 'custom';
ALTER TABLE public_models ADD COLUMN routing_strategy TEXT NOT NULL DEFAULT 'fixed' CHECK (routing_strategy IN ('fixed', 'ordered_fallback', 'weighted', 'lowest_cost', 'lowest_latency'));
ALTER TABLE public_models ADD COLUMN free_only INTEGER NOT NULL DEFAULT 0 CHECK (free_only IN (0, 1));

CREATE TABLE public_model_targets (
    public_model_id TEXT NOT NULL REFERENCES public_models(id) ON DELETE CASCADE,
    upstream_model_id TEXT NOT NULL REFERENCES upstream_models(id) ON DELETE RESTRICT,
    priority INTEGER NOT NULL CHECK (priority BETWEEN 1 AND 1000),
    weight INTEGER NOT NULL DEFAULT 1 CHECK (weight BETWEEN 1 AND 10000),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (public_model_id, upstream_model_id),
    UNIQUE (public_model_id, priority)
);

INSERT INTO public_model_targets (public_model_id, upstream_model_id, priority, weight, enabled, created_at, updated_at)
SELECT id, target_model_id, 1, 1, active, created_at, updated_at FROM public_models;

CREATE INDEX public_model_targets_order ON public_model_targets(public_model_id, enabled, priority);

CREATE TABLE route_observations (
    upstream_model_id TEXT NOT NULL REFERENCES upstream_models(id) ON DELETE CASCADE,
    operation TEXT NOT NULL,
    streaming INTEGER NOT NULL CHECK (streaming IN (0, 1)),
    success_count INTEGER NOT NULL DEFAULT 0 CHECK (success_count >= 0),
    failure_count INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    ewma_first_byte_ms INTEGER,
    ewma_total_ms INTEGER,
    circuit_open_until INTEGER,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (upstream_model_id, operation, streaming)
);

ALTER TABLE attempts ADD COLUMN selection_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN rejected_candidates_json TEXT NOT NULL DEFAULT '[]';

CREATE TABLE catalog_candidates (
    provider TEXT NOT NULL,
    model_id TEXT NOT NULL,
    label TEXT NOT NULL,
    capabilities_json TEXT NOT NULL,
    input_nanos_per_million INTEGER,
    output_nanos_per_million INTEGER,
    free INTEGER NOT NULL DEFAULT 0 CHECK (free IN (0, 1)),
    source TEXT NOT NULL,
    source_version TEXT NOT NULL,
    discovered_at INTEGER NOT NULL,
    PRIMARY KEY (provider, model_id)
);

CREATE TABLE catalog_refresh_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    source_url TEXT NOT NULL DEFAULT '',
    source_version TEXT NOT NULL DEFAULT '',
    last_checked_at INTEGER,
    last_error TEXT NOT NULL DEFAULT '',
    refresh_enabled INTEGER NOT NULL DEFAULT 0 CHECK (refresh_enabled IN (0, 1)),
    refresh_interval_hours INTEGER NOT NULL DEFAULT 24 CHECK (refresh_interval_hours BETWEEN 1 AND 720)
);

INSERT INTO catalog_refresh_state (singleton) VALUES (1);
