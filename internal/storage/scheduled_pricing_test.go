package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestScheduledPricingMigrationAddsConstrainedColumns(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	columns := map[string]bool{}
	rows, err := store.SystemDB().QueryContext(ctx, "PRAGMA table_info(price_versions)")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		columns[name] = true
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cache_read_nanos_per_million", "weekly_start_minute_utc", "weekly_end_minute_utc"} {
		if !columns[name] {
			t.Fatalf("price_versions.%s is missing", name)
		}
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO price_versions(id,connection_id,model_id,input_nanos_per_million,output_nanos_per_million,source,effective_from,weekly_start_minute_utc,created_at) VALUES('bad','connection','model',1,1,'test',1,0,1)`); err == nil {
		t.Fatal("migration accepted an incomplete weekly window")
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO price_versions(id,connection_id,model_id,input_nanos_per_million,output_nanos_per_million,cache_read_nanos_per_million,source,effective_from,created_at) VALUES('bad_cache','connection','model',1,1,-1,'test',1,1)`); err == nil {
		t.Fatal("migration accepted a negative cache-read rate")
	}
	var attemptsColumn int
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('attempts') WHERE name='price_quoted_at'`).Scan(&attemptsColumn); err != nil || attemptsColumn != 1 {
		t.Fatalf("attempts.price_quoted_at count=%d err=%v", attemptsColumn, err)
	}
}
