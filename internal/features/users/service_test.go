package users

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestRolesSuspensionAndOwnerTransfer(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authService := auth.New(store.SystemDB())
	owner, _, err := authService.Claim(ctx, auth.ClaimInput{Email: "owner@example.test", DisplayName: "Owner", Password: "correct-horse-battery"})
	if err != nil {
		t.Fatal(err)
	}
	service := New(store.SystemDB())
	adminGrants := Grants{Scopes: []string{"chat:generate"}, ModelPatterns: []string{"gpt-*"}, ConnectionIDs: []string{"conn_main"}}
	admin, adminCode, err := service.Create(ctx, owner, CreateInput{Email: "admin@example.test", DisplayName: "Admin", Role: "admin", Grants: adminGrants})
	if err != nil {
		t.Fatal(err)
	}
	activeAdmin, adminSession, err := authService.Activate(ctx, auth.ActivateInput{Code: adminCode, Password: "admin-secure-password"})
	if err != nil {
		t.Fatal(err)
	}
	member, memberCode, err := service.Create(ctx, activeAdmin, CreateInput{Email: "member@example.test", DisplayName: "Member", Role: "member", Grants: adminGrants})
	if err != nil {
		t.Fatal(err)
	}
	activeMember, _, err := authService.Activate(ctx, auth.ActivateInput{Code: memberCode, Password: "member-secure-password"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.List(ctx, activeMember, 50, ""); !errors.Is(err, ErrDenied) {
		t.Fatalf("member list error = %v", err)
	}
	if _, _, err := service.Create(ctx, activeAdmin, CreateInput{Email: "escalation@example.test", DisplayName: "Escalation", Role: "member", Grants: Grants{Scopes: []string{"tokens:count"}}}); !errors.Is(err, ErrDenied) {
		t.Fatalf("admin grant escalation = %v", err)
	}
	items, _, err := service.List(ctx, activeAdmin, 50, "")
	if err != nil || len(items) != 1 || items[0].ID != member.ID {
		t.Fatalf("admin list = %#v, %v", items, err)
	}
	if _, err := service.Get(ctx, activeAdmin, admin.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("admin peer access = %v", err)
	}
	displayName := "Changed"
	if _, err := service.Update(ctx, activeAdmin, admin.ID, 2, UpdateInput{DisplayName: &displayName}); !errors.Is(err, ErrDenied) {
		t.Fatalf("admin peer mutation = %v", err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO api_keys (id, owner_user_id, label, state, scopes_json, model_patterns_json, connection_ids_json, created_at, updated_at) VALUES ('key_member', ?, 'Existing', 'active', '["chat:generate"]', '["gpt-*"]', '["conn_main"]', 1, 1)`, member.ID); err != nil {
		t.Fatal(err)
	}
	member, err = service.UpdateGrants(ctx, owner, member.ID, member.Revision+1, Grants{})
	if err != nil {
		t.Fatal(err)
	}
	var scopesJSON string
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT scopes_json FROM api_keys WHERE id = 'key_member'").Scan(&scopesJSON); err != nil || scopesJSON != "[]" {
		t.Fatalf("stored key grants = %q, %v", scopesJSON, err)
	}
	_, memberSession, err := authService.Login(ctx, auth.LoginInput{Email: activeMember.Email, Password: "member-secure-password"})
	if err != nil {
		t.Fatal(err)
	}
	suspended := "suspended"
	member, err = service.Update(ctx, owner, member.ID, member.Revision, UpdateInput{Status: &suspended})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authService.Session(ctx, memberSession); !errors.Is(err, auth.ErrInvalidSession) {
		t.Fatalf("suspended session = %v", err)
	}
	if _, err := service.IssueCode(ctx, owner, member.ID, "recovery"); err == nil {
		t.Fatal("suspended user received recovery code")
	}
	recoveryCode, err := service.IssueCode(ctx, owner, admin.ID, "recovery")
	if err != nil {
		t.Fatal(err)
	}
	admin, err = service.Get(ctx, owner, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.TransferOwner(ctx, owner, admin.ID, admin.Revision); err != nil {
		t.Fatal(err)
	}
	if _, _, err := authService.Activate(ctx, auth.ActivateInput{Code: recoveryCode, Password: "replacement-password"}); !errors.Is(err, auth.ErrInvalidActivation) {
		t.Fatalf("recovery survived owner transfer = %v", err)
	}
	if _, _, err := service.Create(ctx, owner, CreateInput{Email: "stale@example.test", DisplayName: "Stale owner", Role: "admin"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("stale owner mutation = %v", err)
	}
	if _, err := authService.Session(ctx, adminSession); !errors.Is(err, auth.ErrInvalidSession) {
		t.Fatalf("owner transfer session = %v", err)
	}
	var owners int
	if err := store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role = 'owner' AND status = 'active'").Scan(&owners); err != nil || owners != 1 {
		t.Fatalf("active owners = %d, %v", owners, err)
	}
}
