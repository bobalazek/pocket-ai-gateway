package providers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestCatalogPagination(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now().UnixMilli()
	owner := auth.User{ID: "usr_owner", Role: "owner", Status: "active"}
	if _, err = store.SystemDB().ExecContext(ctx, "INSERT INTO users (id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES (?,?,?,'hash','owner','active',1,?,?)", owner.ID, "owner@example.test", "Owner", now, now); err != nil {
		t.Fatal(err)
	}
	for index := range 52 {
		modelID := fmt.Sprintf("model-%02d", index)
		if _, err = store.SystemDB().ExecContext(ctx, "INSERT INTO catalog_candidates (provider,model_id,label,capabilities_json,free,source,source_version,discovered_at) VALUES ('provider',?,?, '[\"chat\"]',0,'test','v1',?)", modelID, modelID, now); err != nil {
			t.Fatal(err)
		}
	}

	service := New(store.SystemDB(), make([]byte, 32))
	first, _, next, err := service.Catalog(ctx, owner, "")
	if err != nil || len(first) != 50 || next == "" || first[0].ModelID != "model-00" || first[49].ModelID != "model-49" {
		t.Fatalf("first page len=%d next=%q err=%v", len(first), next, err)
	}
	second, _, next, err := service.Catalog(ctx, owner, next)
	if err != nil || len(second) != 2 || next != "" || second[0].ModelID != "model-50" || second[1].ModelID != "model-51" {
		t.Fatalf("second page=%#v next=%q err=%v", second, next, err)
	}
	if _, _, _, err = service.Catalog(ctx, owner, "invalid"); err == nil {
		t.Fatal("invalid cursor was accepted")
	}
}
