ALTER TABLE requests ADD COLUMN retained_at INTEGER;

CREATE INDEX requests_visible_history ON requests(retained_at, started_at DESC, id DESC);
