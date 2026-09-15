ALTER TABLE price_versions ADD COLUMN cache_read_nanos_per_million INTEGER
    CHECK (cache_read_nanos_per_million IS NULL OR cache_read_nanos_per_million >= 0);

ALTER TABLE price_versions ADD COLUMN weekly_start_minute_utc INTEGER
    CHECK (weekly_start_minute_utc IS NULL OR weekly_start_minute_utc >= 0 AND weekly_start_minute_utc < 10080);

ALTER TABLE price_versions ADD COLUMN weekly_end_minute_utc INTEGER
    CHECK (
        weekly_start_minute_utc IS NULL AND weekly_end_minute_utc IS NULL OR
        weekly_start_minute_utc IS NOT NULL AND weekly_end_minute_utc IS NOT NULL AND
        weekly_start_minute_utc < weekly_end_minute_utc AND weekly_end_minute_utc <= 10080
    );

ALTER TABLE attempts ADD COLUMN price_quoted_at INTEGER
    CHECK (price_quoted_at IS NULL OR price_quoted_at >= 0);

DROP INDEX price_versions_lookup;
CREATE INDEX price_versions_lookup ON price_versions(connection_id, model_id, effective_from DESC, weekly_start_minute_utc, weekly_end_minute_utc);
