CREATE TABLE provider_adapter_scripts (
    connection_id TEXT PRIMARY KEY REFERENCES provider_connections(id) ON DELETE CASCADE,
    request_script TEXT NOT NULL DEFAULT '' CHECK (length(request_script) <= 65536),
    response_script TEXT NOT NULL DEFAULT '' CHECK (length(response_script) <= 65536),
    updated_at INTEGER NOT NULL,
    CHECK (request_script <> '' OR response_script <> '')
);
