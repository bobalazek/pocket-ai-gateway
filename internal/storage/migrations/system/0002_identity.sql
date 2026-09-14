CREATE TABLE users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL UNIQUE COLLATE NOCASE,
    display_name TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
    status TEXT NOT NULL CHECK (status IN ('active', 'suspended', 'archived')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX one_active_owner ON users ((1)) WHERE role = 'owner' AND status = 'active';

CREATE TABLE setup_tokens (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    verifier BLOB NOT NULL,
    expires_at INTEGER NOT NULL,
    claimed_user_id TEXT REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE sessions (
    verifier BLOB PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX sessions_user_id ON sessions(user_id);
