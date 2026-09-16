package providers

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestManagedModelsSearchReturnsRouteView(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner := auth.User{ID: "usr_owner", Role: "owner", Status: "active"}
	now := time.Now().UnixMilli()
	if _, err = store.SystemDB().ExecContext(ctx, "INSERT INTO users (id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES (?,?,?,'hash','owner','active',1,?,?)", owner.ID, "owner@example.test", "Owner", now, now); err != nil {
		t.Fatal(err)
	}
	service := New(store.SystemDB(), make([]byte, 32))
	chat := routeFixture(t, ctx, service, owner, "chat")
	embeddings := routeFixture(t, ctx, service, owner, "vectors")
	if _, err = service.CreatePublicModel(ctx, owner, "assistant", "Assistant", "General chat", chat.ID, []string{"chat"}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.CreatePublicModel(ctx, owner, "semantic-search", "Semantic search", "Vector model", embeddings.ID, []string{"embeddings"}); err != nil {
		t.Fatal(err)
	}

	view, err := service.ManagedModels(ctx, owner, "VECTORS-MODEL")
	if err != nil || len(view.Models) != 1 || view.Models[0].ID != "semantic-search" {
		t.Fatalf("models=%#v err=%v", view.Models, err)
	}
	if len(view.PublishTargets) != 2 || len(view.Routes["semantic-search"]) != 1 || len(view.AvailableTargets["semantic-search"]) != 2 {
		t.Fatalf("view=%#v", view)
	}
}

func TestModelSearchRejectsOversizedQuery(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/v1/admin/models?q="+strings.Repeat("x", 101), nil)
	response := httptest.NewRecorder()
	if _, ok := modelSearch(response, request); ok || response.Code != 400 {
		t.Fatalf("ok=%v status=%d", ok, response.Code)
	}
}
