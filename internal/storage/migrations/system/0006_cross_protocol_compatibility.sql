ALTER TABLE attempts ADD COLUMN target_dialect TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN target_operation TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN translation_applied INTEGER NOT NULL DEFAULT 0 CHECK (translation_applied IN (0, 1));
ALTER TABLE attempts ADD COLUMN request_tool_count INTEGER NOT NULL DEFAULT 0 CHECK (request_tool_count >= 0);
ALTER TABLE attempts ADD COLUMN response_tool_call_count INTEGER NOT NULL DEFAULT 0 CHECK (response_tool_call_count >= 0);
ALTER TABLE attempts ADD COLUMN tool_call_status TEXT NOT NULL DEFAULT 'none' CHECK (tool_call_status IN ('none', 'completed', 'incomplete'));
