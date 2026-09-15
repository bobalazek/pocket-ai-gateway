ALTER TABLE attempts ADD COLUMN cache_creation_input_tokens INTEGER CHECK (cache_creation_input_tokens IS NULL OR cache_creation_input_tokens >= 0);
ALTER TABLE attempts ADD COLUMN cache_read_input_tokens INTEGER CHECK (cache_read_input_tokens IS NULL OR cache_read_input_tokens >= 0);
ALTER TABLE attempts ADD COLUMN cache_creation_5m_input_tokens INTEGER CHECK (cache_creation_5m_input_tokens IS NULL OR cache_creation_5m_input_tokens >= 0);
ALTER TABLE attempts ADD COLUMN cache_creation_1h_input_tokens INTEGER CHECK (cache_creation_1h_input_tokens IS NULL OR cache_creation_1h_input_tokens >= 0);
