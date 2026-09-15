ALTER TABLE usage_daily ADD COLUMN cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_creation_input_tokens >= 0);
ALTER TABLE usage_daily ADD COLUMN cache_read_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_read_input_tokens >= 0);
ALTER TABLE usage_daily ADD COLUMN cache_creation_5m_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_creation_5m_input_tokens >= 0);
ALTER TABLE usage_daily ADD COLUMN cache_creation_1h_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_creation_1h_input_tokens >= 0);
