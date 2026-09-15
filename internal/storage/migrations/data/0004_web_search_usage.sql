ALTER TABLE usage_daily ADD COLUMN web_search_calls INTEGER NOT NULL DEFAULT 0 CHECK (web_search_calls >= 0);
