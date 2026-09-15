ALTER TABLE attempts ADD COLUMN web_search_max_calls INTEGER CHECK (web_search_max_calls IS NULL OR web_search_max_calls BETWEEN 1 AND 4);
ALTER TABLE attempts ADD COLUMN web_search_call_count INTEGER CHECK (web_search_call_count IS NULL OR web_search_call_count BETWEEN 0 AND 4);
