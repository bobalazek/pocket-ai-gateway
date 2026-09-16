ALTER TABLE openai_files ADD COLUMN client_purpose TEXT
    CHECK (client_purpose IS NULL OR client_purpose IN ('assistants', 'batch', 'batch_output', 'fine-tune', 'vision', 'user_data', 'evals'));
