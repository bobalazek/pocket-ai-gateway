ALTER TABLE stored_responses ADD COLUMN conversation_id TEXT REFERENCES conversations(id) ON DELETE SET NULL;
ALTER TABLE stored_responses ADD COLUMN conversation_revision INTEGER;
ALTER TABLE stored_responses ADD COLUMN conversation_items_json BLOB;
