CREATE TABLE openai_vector_store_files (
    vector_store_id TEXT NOT NULL REFERENCES openai_vector_stores(id) ON DELETE CASCADE,
    file_id TEXT NOT NULL REFERENCES openai_files(id) ON DELETE CASCADE,
    attributes_json BLOB NOT NULL DEFAULT 'null'
        CHECK (length(attributes_json) BETWEEN 4 AND 16384 AND json_valid(attributes_json) AND json_type(attributes_json) IN ('null', 'object')),
    chunking_strategy_json BLOB NOT NULL DEFAULT '{"type":"other"}'
        CHECK (length(chunking_strategy_json) BETWEEN 2 AND 1024 AND json_valid(chunking_strategy_json) AND json_type(chunking_strategy_json) = 'object'),
    created_at INTEGER NOT NULL,
    PRIMARY KEY (vector_store_id, file_id)
);

CREATE INDEX openai_vector_store_files_list ON openai_vector_store_files(vector_store_id, created_at DESC, file_id DESC);
