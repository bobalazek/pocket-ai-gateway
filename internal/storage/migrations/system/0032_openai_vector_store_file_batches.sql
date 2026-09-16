CREATE TABLE openai_vector_store_file_batches (
    id TEXT PRIMARY KEY,
    vector_store_id TEXT NOT NULL REFERENCES openai_vector_stores(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    file_count INTEGER NOT NULL CHECK (file_count BETWEEN 1 AND 2000)
);

ALTER TABLE openai_vector_store_files
    ADD COLUMN file_batch_id TEXT REFERENCES openai_vector_store_file_batches(id) ON DELETE SET NULL;

CREATE INDEX openai_vector_store_file_batches_store ON openai_vector_store_file_batches(vector_store_id, created_at DESC, id DESC);
CREATE INDEX openai_vector_store_files_batch ON openai_vector_store_files(file_batch_id, created_at DESC, file_id DESC);
