ALTER TABLE price_versions ADD COLUMN web_search_nanos_per_call INTEGER CHECK (web_search_nanos_per_call IS NULL OR web_search_nanos_per_call >= 0);
ALTER TABLE attempts ADD COLUMN web_fetch_present INTEGER NOT NULL DEFAULT 0 CHECK (web_fetch_present IN (0, 1));
-- Older Anthropic attempts with multiple tools may include web fetch. Leave them
-- unpriced until an operator can verify the full tool mix and cost.
UPDATE attempts SET web_fetch_present=1 WHERE target_dialect='anthropic' AND web_search_max_calls IS NOT NULL AND request_tool_count>1;
