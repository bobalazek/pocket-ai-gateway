ALTER TABLE requests ADD COLUMN streaming INTEGER NOT NULL DEFAULT 0 CHECK (streaming IN (0, 1));
ALTER TABLE attempts ADD COLUMN first_byte_at INTEGER CHECK (first_byte_at IS NULL OR first_byte_at >= 0);
