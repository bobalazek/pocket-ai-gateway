package providers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestAdapterScriptLifecycleAndTargetLoading(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner := auth.User{ID: "usr_owner"}
	now := time.Now().UnixMilli()
	if _, err = store.SystemDB().ExecContext(ctx, "INSERT INTO users (id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES (?,?,?,'hash','owner','active',1,?,?)", owner.ID, "owner@example.test", "Owner", now, now); err != nil {
		t.Fatal(err)
	}
	service := New(store.SystemDB(), make([]byte, 32))
	member := auth.User{ID: "usr_member"}
	if _, err = store.SystemDB().ExecContext(ctx, "INSERT INTO users (id,email,display_name,password_hash,role,status,created_at,updated_at) VALUES (?,?,?,'hash','member','active',?,?)", member.ID, "member@example.test", "Member", now, now); err != nil {
		t.Fatal(err)
	}
	connection, err := service.CreateConnection(ctx, owner, ConnectionInput{Name: "Custom", Adapter: "openai_compatible", BaseURL: "https://provider.example/v1", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.PutCredential(ctx, owner, connection.ID, "secret", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = service.GetAdapterScript(ctx, member, connection.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("member read error = %v", err)
	}
	connection, err = service.getConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := AdapterScriptInput{RequestScript: `(input) => ({path: "predictions", body: {model: input.model}})`, ResponseScript: `(input) => ({body: input.body})`}
	script, err := service.PutAdapterScript(ctx, owner, connection.ID, connection.Revision, input)
	if err != nil || script.Revision != connection.Revision+1 {
		t.Fatalf("script=%#v error=%v", script, err)
	}
	if _, err = service.PutAdapterScript(ctx, owner, connection.ID, connection.Revision, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	upstream, err := service.CreateUpstreamModel(ctx, owner, connection.ID, "provider-model", []string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	model, err := service.CreatePublicModel(ctx, owner, "assistant", "Assistant", "", upstream.ID, []string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.Target(ctx, model.ID)
	if err != nil || target.AdapterRequestScript != input.RequestScript || target.AdapterResponseScript != input.ResponseScript {
		t.Fatalf("target scripts = %#v error=%v", target, err)
	}
	if err = service.DeleteAdapterScript(ctx, owner, connection.ID, script.Revision); err != nil {
		t.Fatal(err)
	}
	script, err = service.GetAdapterScript(ctx, owner, connection.ID)
	if err != nil || script.RequestScript != "" || script.ResponseScript != "" || script.Revision != target.ConnectionRevision+1 {
		t.Fatalf("deleted script=%#v error=%v", script, err)
	}
	script, err = service.PutAdapterScript(ctx, owner, connection.ID, script.Revision, input)
	if err != nil {
		t.Fatal(err)
	}
	connection, err = service.getConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.UpdateConnection(ctx, owner, connection.ID, connection.Revision, ConnectionInput{Name: connection.Name, Preset: "openrouter", Enabled: true, TimeoutMS: connection.TimeoutMS}); err != nil {
		t.Fatal(err)
	}
	var stored int
	if err = store.SystemDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM provider_adapter_scripts WHERE connection_id=?", connection.ID).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("script survived preset change: count=%d error=%v", stored, err)
	}
}

func TestAdapterScriptsRequireCustomOpenAICompatibleConnection(t *testing.T) {
	if _, err := validateAdapterScriptInput(AdapterScriptInput{RequestScript: `42`}); err == nil {
		t.Fatal("non-function script was accepted")
	}
	if _, err := validateAdapterScriptInput(AdapterScriptInput{RequestScript: `(input) => ({body: input.body})`}); err != nil {
		t.Fatal(err)
	}
}
