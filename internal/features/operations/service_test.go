package operations

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

func TestLocalBackupSettingsAndDiagnostics(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := storage.Open(ctx, filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES('usr_owner','owner@example.test','Owner','hash','owner','active',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{"POCKET_AI_GATEWAY_BACKUP_KEY": base64.StdEncoding.EncodeToString(key)}
	service := New(store, providers.New(store.SystemDB(), make([]byte, 32)), "v0.1.0-test", func(name string) string { return environment[name] })
	settings, err := service.Settings(ctx)
	if err != nil || !settings.BackupKeyConfigured {
		t.Fatalf("settings = %+v, error = %v", settings, err)
	}
	settings.BackupEnabled = true
	settings.LocalDirectory = filepath.Join(root, "backups")
	settings, err = service.UpdateSettings(ctx, "usr_owner", settings.Revision, settings)
	if err != nil || settings.Revision != 2 {
		t.Fatalf("updated settings = %+v, error = %v", settings, err)
	}
	job, err := service.RunBackup(ctx, "usr_owner")
	if err != nil || job.State != "succeeded" || len(job.Checksum) != 64 {
		t.Fatalf("backup = %+v, error = %v", job, err)
	}
	archive := filepath.Join(settings.LocalDirectory, job.ArchiveName)
	if err := storage.RestoreEncryptedSnapshot(ctx, archive, filepath.Join(root, "restored"), key); err != nil {
		t.Fatal(err)
	}
	jobs, err := service.ListBackups(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Fatalf("jobs = %+v, error = %v", jobs, err)
	}
	diagnostics, err := service.Diagnostics(ctx)
	if err != nil || diagnostics.Version != "v0.1.0-test" || diagnostics.SQLiteVersion == "" || diagnostics.LatestBackupState != "succeeded" {
		t.Fatalf("diagnostics = %+v, error = %v", diagnostics, err)
	}
	bundle, err := service.ExportConfig(ctx)
	if err != nil || bundle.Format != 3 {
		t.Fatalf("export = %+v, error = %v", bundle, err)
	}
	preview, err := PreviewConfig(bundle)
	if err != nil || len(preview.Warnings) == 0 {
		t.Fatalf("preview = %+v, error = %v", preview, err)
	}
	if _, err := service.ImportConfig(ctx, "usr_owner", bundle); err != nil {
		t.Fatal(err)
	}
}

func TestConfigV3ScheduledCachePriceRoundTripAndOlderCompatibility(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES('usr_owner','owner@example.test','Owner','hash','owner','active',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	cache, start, end := int64(500_000_000), int64(0), int64(60)
	bundle := ConfigBundle{
		Format:         2,
		Settings:       Settings{BackupIntervalHours: 24, BackupRetention: 14, BackupDestination: "local", S3Region: "us-east-1", S3AccessKeyEnv: "AWS_ACCESS_KEY_ID", S3SecretKeyEnv: "AWS_SECRET_ACCESS_KEY", RequestRetention: 90, AuditRetention: 365},
		Catalog:        ConfigCatalog{RefreshIntervalHours: 24},
		Connections:    []ConfigConnection{{ID: "con_test", Name: "OpenAI", Adapter: "openai", BaseURL: "https://api.openai.com/v1", Enabled: true, TimeoutMS: 60000, Preset: "openai"}},
		UpstreamModels: []ConfigUpstream{{ID: "up_test", ConnectionID: "con_test", UpstreamID: "gpt-4o", Capabilities: []string{"chat"}, Active: true}},
		PublicModels:   []ConfigModel{{ID: "model-test", Label: "Test", TargetConnectionID: "con_test", TargetModelID: "up_test", Capabilities: []string{"chat"}, Active: true, RoutingStrategy: "fixed"}},
		Targets:        []ConfigTarget{{PublicModelID: "model-test", UpstreamModelID: "up_test", Priority: 1, Weight: 1, Enabled: true}},
		Prices:         []ConfigPrice{{ID: "prc_test", ConnectionID: "con_test", ModelID: "model-test", InputNanosPerMillion: 2_000_000_000, OutputNanosPerMillion: 4_000_000_000, CacheReadNanosPerMillion: &cache, Source: "test", EffectiveFrom: now - 1000, WeeklyStartMinuteUTC: &start, WeeklyEndMinuteUTC: &end}},
	}
	if _, err := PreviewConfig(bundle); err != nil {
		t.Fatalf("v2 preview=%v", err)
	}
	service := New(store, providers.New(store.SystemDB(), make([]byte, 32)), "test", func(string) string { return "" })
	if _, err := service.ImportConfig(ctx, "usr_owner", bundle); err != nil {
		t.Fatal(err)
	}
	exported, err := service.ExportConfig(ctx)
	if err != nil || exported.Format != 3 || len(exported.Prices) != 1 || exported.Prices[0].CacheReadNanosPerMillion == nil || *exported.Prices[0].CacheReadNanosPerMillion != cache || exported.Prices[0].WeeklyStartMinuteUTC == nil || *exported.Prices[0].WeeklyStartMinuteUTC != start || exported.Prices[0].WeeklyEndMinuteUTC == nil || *exported.Prices[0].WeeklyEndMinuteUTC != end {
		t.Fatalf("exported=%#v err=%v", exported.Prices, err)
	}
	existingOverlap := exported
	existingOverlap.Prices = append([]ConfigPrice(nil), exported.Prices...)
	existingOverlap.Prices[0].ID = "prc_existing_overlap"
	if _, err := service.ImportConfig(ctx, "usr_owner", existingOverlap); !errors.Is(err, ErrConflict) {
		t.Fatalf("existing price overlap=%v", err)
	}
	conflicting := exported
	conflicting.Prices = append([]ConfigPrice(nil), exported.Prices...)
	conflicting.Prices[0].Source = "forged"
	if _, err := service.ImportConfig(ctx, "usr_owner", conflicting); !errors.Is(err, ErrConflict) {
		t.Fatalf("immutable price conflict=%v", err)
	}
	overlapping := bundle.Prices[0]
	overlapping.ID = "prc_overlap"
	overlapping.WeeklyStartMinuteUTC, overlapping.WeeklyEndMinuteUTC = nil, nil
	bundle.Prices = append(bundle.Prices, overlapping)
	if _, err := PreviewConfig(bundle); err == nil {
		t.Fatal("overlapping imported prices were accepted")
	}
	bundle.Prices = bundle.Prices[:1]
	bundle.Format = 1
	bundle.Prices[0].CacheReadNanosPerMillion, bundle.Prices[0].WeeklyStartMinuteUTC, bundle.Prices[0].WeeklyEndMinuteUTC = nil, nil, nil
	if _, err := PreviewConfig(bundle); err != nil {
		t.Fatalf("v1 preview=%v", err)
	}
	bundle.Prices[0].CacheReadNanosPerMillion = &cache
	if _, err := PreviewConfig(bundle); err == nil {
		t.Fatal("v1 config accepted v2 price fields")
	}
}

func TestConfigV3AdapterScriptRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UnixMilli()
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES('usr_owner','owner@example.test','Owner','hash','owner','active',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	bundle := ConfigBundle{
		Format:         3,
		Settings:       Settings{BackupIntervalHours: 24, BackupRetention: 14, BackupDestination: "local", S3Region: "us-east-1", S3AccessKeyEnv: "AWS_ACCESS_KEY_ID", S3SecretKeyEnv: "AWS_SECRET_ACCESS_KEY", RequestRetention: 90, AuditRetention: 365},
		Catalog:        ConfigCatalog{RefreshIntervalHours: 24},
		Connections:    []ConfigConnection{{ID: "con_script", Name: "Scripted", Adapter: "openai_compatible", BaseURL: "https://api.example.test/v1", Enabled: true, TimeoutMS: 60000, Preset: "custom"}},
		AdapterScripts: []ConfigAdapterScript{{ConnectionID: "con_script", RequestScript: `(input) => ({body: input.body})`, ResponseScript: `(input) => ({body: input.body})`}},
	}
	service := New(store, providers.New(store.SystemDB(), make([]byte, 32)), "test", func(string) string { return "" })
	if _, err = service.ImportConfig(ctx, "usr_owner", bundle); err != nil {
		t.Fatal(err)
	}
	exported, err := service.ExportConfig(ctx)
	if err != nil || len(exported.AdapterScripts) != 1 || exported.AdapterScripts[0] != bundle.AdapterScripts[0] {
		t.Fatalf("adapter scripts=%#v err=%v", exported.AdapterScripts, err)
	}
	bundle.AdapterScripts[0].RequestScript = `() => {`
	if _, err = PreviewConfig(bundle); err == nil {
		t.Fatal("invalid adapter script was accepted")
	}
}

func TestConfigPreviewRejectsUnsafeProviderAndRetentionPreservesEnforcement(t *testing.T) {
	bundle := ConfigBundle{Format: 1, Settings: Settings{BackupIntervalHours: 24, BackupRetention: 14, BackupDestination: "local", S3Region: "us-east-1", S3AccessKeyEnv: "AWS_ACCESS_KEY_ID", S3SecretKeyEnv: "AWS_SECRET_ACCESS_KEY", RequestRetention: 90, AuditRetention: 365}, Catalog: ConfigCatalog{RefreshIntervalHours: 24}, Connections: []ConfigConnection{{ID: "con_test", Name: "Unsafe", Adapter: "openai", BaseURL: "http://169.254.169.254", Enabled: true, TimeoutMS: 60000, Preset: "custom"}}}
	if _, err := PreviewConfig(bundle); err == nil {
		t.Fatal("unsafe provider URL was accepted")
	}
	bundle = ConfigBundle{Format: 1, Settings: Settings{BackupIntervalHours: 24, BackupRetention: 14, BackupDestination: "local", S3Region: "us-east-1", S3AccessKeyEnv: "AWS_ACCESS_KEY_ID", S3SecretKeyEnv: "AWS_SECRET_ACCESS_KEY", RequestRetention: 90, AuditRetention: 365}, Catalog: ConfigCatalog{RefreshIntervalHours: 24}, Connections: []ConfigConnection{{ID: "con_test", Name: "OpenRouter", Adapter: "openai_compatible", BaseURL: "https://openrouter.ai/api/v1", Enabled: true, TimeoutMS: 60000, Preset: "openrouter"}}, UpstreamModels: []ConfigUpstream{{ID: "up_test", ConnectionID: "con_test", UpstreamID: "moderation", Capabilities: []string{"moderations"}, Active: true}}}
	if _, err := PreviewConfig(bundle); err == nil {
		t.Fatal("config import accepted capabilities outside the provider preset")
	}
	bundle = ConfigBundle{Format: 1, Settings: Settings{BackupIntervalHours: 24, BackupRetention: 14, BackupDestination: "local", S3Region: "us-east-1", S3AccessKeyEnv: "AWS_ACCESS_KEY_ID", S3SecretKeyEnv: "AWS_SECRET_ACCESS_KEY", RequestRetention: 90, AuditRetention: 365}, Catalog: ConfigCatalog{RefreshIntervalHours: 24}, Connections: []ConfigConnection{{ID: "con_test", Name: "OpenAI", Adapter: "openai", BaseURL: "https://api.openai.com/v1", Enabled: true, TimeoutMS: 60000, Preset: "openai"}}, UpstreamModels: []ConfigUpstream{{ID: "up_test", ConnectionID: "con_test", UpstreamID: "gpt-4o-transcribe", Capabilities: []string{"audio_translation"}, Active: true}}}
	if _, err := PreviewConfig(bundle); err == nil {
		t.Fatal("config import accepted a non-whisper OpenAI translation model")
	}
	bundle.UpstreamModels[0] = ConfigUpstream{ID: "up_test", ConnectionID: "con_test", UpstreamID: "gpt-image-1", Capabilities: []string{"image_variation"}, Active: true}
	if _, err := PreviewConfig(bundle); err == nil {
		t.Fatal("config import accepted a non-dall-e-2 OpenAI variation model")
	}

	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UnixMilli()
	oldRequest := time.Now().AddDate(0, 0, -45).UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES('usr_owner','owner@example.test','Owner','hash','owner','active',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO api_keys(id,owner_user_id,label,state,scopes_json,created_at,updated_at) VALUES('key_old','usr_owner','Old','active','[]',0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO requests(id,owner_user_id,key_id,operation,dialect,model_id,state,started_at,finished_at) VALUES('req_old','usr_owner','key_old','chat/completions','openai','public-model','succeeded',?,?)`, oldRequest, oldRequest+1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO attempts(id,request_id,ordinal,connection_id,model_id,state,usage_status,started_at,finished_at,upstream_model_id,target_dialect,target_operation,web_search_max_calls,web_search_call_count,selection_reason,rejected_candidates_json) VALUES('att_old','req_old',1,'con_old','provider-model','succeeded','provider_reported',?,?,'secret-upstream','anthropic','messages',2,1,'lowest_latency','[{"id":"candidate"}]')`, oldRequest, oldRequest+1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO limit_policies(id,scope_kind,scope_id,metric,algorithm,period,window_seconds,limit_units,refill_units,refill_interval_ms,enabled,created_at,updated_at) VALUES('pol_quota','instance','','requests','quota','day',0,100,0,0,1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO quota_periods(policy_id,period_start,period_end,consumed_units,reserved_units,last_effective_at) VALUES('pol_quota',0,?,20,3,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO audit_events(id,actor_user_id,action,resource_type,resource_id,created_at) VALUES('aud_old','usr_owner','test.old','test','old',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO stored_responses(id,owner_user_id,key_id,model_id,body_json,created_at,expires_at) VALUES('resp_old','usr_owner','key_old','public-model','{}',0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO stored_chat_completions(id,owner_user_id,key_id,model_id,body_json,request_json,metadata_json,created_at,expires_at) VALUES('chatcmpl_old','usr_owner','key_old','public-model','{}','{}','{}',0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO message_batches(id,owner_user_id,key_id,created_at,expires_at) VALUES('msgbatch_old','usr_owner','key_old',0,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO message_batch_items(batch_id,ordinal,custom_id,params_json,reserved_result_bytes) VALUES('msgbatch_old',1,'old_item','{"messages":[]}',1024)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO openai_uploads(id,owner_user_id,key_id,filename,purpose,mime_type,expected_bytes,file_expiry_seconds,created_at,expires_at) VALUES('upload_old','usr_owner','key_old','old.jsonl','batch','application/jsonl',1,3600,-3600001,-1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO openai_upload_parts(id,upload_id,bytes,ciphertext,nonce,created_at) VALUES('part_old','upload_old',1,?,?,0)`, make([]byte, 17), make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO openai_files(id,owner_user_id,key_id,filename,purpose,bytes,ciphertext,nonce,created_at,expires_at) VALUES('file_old','usr_owner','key_old','old.jsonl','batch',0,?,?,0,3600000)`, make([]byte, 16), make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO openai_vector_stores(id,owner_user_id,key_id,name,metadata_json,created_at,last_active_at,expires_after_days,expires_at) VALUES('vs_old','usr_owner','key_old','Old','{}',0,0,1,86400000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,status,metadata_json,output_expiry_seconds,output_file_id,request_total,request_completed,created_at,in_progress_at,expires_at,terminal_at,retention_expires_at) VALUES('batch_old','usr_owner','key_old','file_old','/v1/responses','24h','public-model','completed','{}',3600,'file_old',1,1,0,0,86400000,1,2592000000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO conversations(id,owner_user_id,key_id,metadata_json,created_at,deleted_at) VALUES('conv_old','usr_owner','key_old','{}',0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO conversation_items(conversation_id,id,ordinal,body_json,created_at) VALUES('conv_old','citem_old',1,'{}',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DataDB().ExecContext(ctx, `INSERT INTO usage_events(event_id,event_type,payload_json,created_at) VALUES('evt_old','test','{"secret":true}',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DataDB().ExecContext(ctx, `INSERT INTO usage_daily(date,owner_user_id,key_id,model_id,connection_id,web_search_calls) VALUES('2026-01-01','usr_owner','key_old','public-model','con_old',1)`); err != nil {
		t.Fatal(err)
	}
	service := New(store, providers.New(store.SystemDB(), make([]byte, 32)), "test", func(string) string { return "" })
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE operation_settings SET request_retention_days=30 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	retentionCounts, err := service.RunRetention(ctx, "usr_owner")
	if err != nil {
		t.Fatal(err)
	}
	if retentionCounts["message_batches"] != 1 {
		t.Fatalf("retained message batch count = %d", retentionCounts["message_batches"])
	}
	if retentionCounts["openai_uploads"] != 1 {
		t.Fatalf("retained OpenAI upload count = %d", retentionCounts["openai_uploads"])
	}
	if retentionCounts["openai_files"] != 1 {
		t.Fatalf("retained OpenAI file count = %d", retentionCounts["openai_files"])
	}
	if retentionCounts["openai_vector_stores"] != 1 {
		t.Fatalf("retained OpenAI Vector Store count = %d", retentionCounts["openai_vector_stores"])
	}
	if retentionCounts["openai_batches"] != 1 {
		t.Fatalf("retained OpenAI batch count = %d", retentionCounts["openai_batches"])
	}
	var consumed, reserved int64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT consumed_units,reserved_units FROM quota_periods WHERE policy_id='pol_quota'`).Scan(&consumed, &reserved); err != nil || consumed != 20 || reserved != 3 {
		t.Fatalf("enforcement state = %d/%d, error = %v", consumed, reserved, err)
	}
	var payload string
	if err := store.DataDB().QueryRowContext(ctx, `SELECT payload_json FROM usage_events WHERE event_id='evt_old'`).Scan(&payload); err != nil || payload != "{}" {
		t.Fatalf("retained usage event payload = %q, error = %v", payload, err)
	}
	var retained int64
	var operation, upstream string
	var webSearchMax, webSearchCount sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT requests.retained_at,requests.operation,attempts.upstream_model_id,attempts.web_search_max_calls,attempts.web_search_call_count FROM requests JOIN attempts ON attempts.request_id=requests.id WHERE requests.id='req_old'`).Scan(&retained, &operation, &upstream, &webSearchMax, &webSearchCount); err != nil || retained == 0 || operation != "" || upstream != "" || !webSearchMax.Valid || webSearchMax.Int64 != 2 || !webSearchCount.Valid || webSearchCount.Int64 != 1 {
		t.Fatalf("retained request = %d/%q/%q web search=%v/%v, error = %v", retained, operation, upstream, webSearchMax, webSearchCount, err)
	}
	summary, err := usage.New(store.SystemDB()).Summary(ctx, auth.User{ID: "usr_owner", Role: "owner"}, usage.UsageQuery{From: time.UnixMilli(oldRequest - 1).UTC().Format(time.RFC3339), To: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil || summary.WebSearchCalls != 1 {
		t.Fatalf("retained web search summary = %#v, error = %v", summary, err)
	}
	var retainedWebSearchCalls int64
	if err := store.DataDB().QueryRowContext(ctx, `SELECT web_search_calls FROM usage_daily WHERE owner_user_id='usr_owner'`).Scan(&retainedWebSearchCalls); err != nil || retainedWebSearchCalls != 1 {
		t.Fatalf("retained web search calls = %d, error = %v", retainedWebSearchCalls, err)
	}
	var storedResponses int
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM stored_responses`).Scan(&storedResponses); err != nil || storedResponses != 0 {
		t.Fatalf("expired stored responses = %d, error = %v", storedResponses, err)
	}
	var storedChats int
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM stored_chat_completions`).Scan(&storedChats); err != nil || storedChats != 0 {
		t.Fatalf("expired stored chats = %d, error = %v", storedChats, err)
	}
	var batches, batchItems int
	_ = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_batches`).Scan(&batches)
	_ = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_batch_items`).Scan(&batchItems)
	if batches != 0 || batchItems != 0 {
		t.Fatalf("expired message batches left batches=%d items=%d", batches, batchItems)
	}
	var uploads, uploadParts int
	_ = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_uploads`).Scan(&uploads)
	_ = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_upload_parts`).Scan(&uploadParts)
	if uploads != 0 || uploadParts != 0 {
		t.Fatalf("expired OpenAI uploads left uploads=%d parts=%d", uploads, uploadParts)
	}
	var files int
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_files`).Scan(&files); err != nil || files != 0 {
		t.Fatalf("expired OpenAI files = %d, error = %v", files, err)
	}
	var vectorStores int
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_vector_stores`).Scan(&vectorStores); err != nil || vectorStores != 0 {
		t.Fatalf("expired OpenAI Vector Stores = %d, error = %v", vectorStores, err)
	}
	var conversations, conversationItems int
	_ = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations WHERE id='conv_old'`).Scan(&conversations)
	_ = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_items WHERE id='citem_old'`).Scan(&conversationItems)
	if conversations != 0 || conversationItems != 0 {
		t.Fatalf("manual conversation retention left conversations=%d items=%d", conversations, conversationItems)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO event_outbox(id,event_type,payload_json,created_at) VALUES('evt_pending_old','test','{"secret":true}',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := usage.ProjectOutbox(ctx, store, 10); err != nil {
		t.Fatal(err)
	}
	if err := store.DataDB().QueryRowContext(ctx, `SELECT payload_json FROM usage_events WHERE event_id='evt_pending_old'`).Scan(&payload); err != nil || payload != "{}" {
		t.Fatalf("late projected payload = %q, error = %v", payload, err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE operation_settings SET request_retention_days=90 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunRetention(ctx, "usr_owner"); err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().Add(-60 * 24 * time.Hour).UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO event_outbox(id,event_type,payload_json,created_at) VALUES('evt_late_after_change','test','{"secret":true}',?)`, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := usage.ProjectOutbox(ctx, store, 10); err != nil {
		t.Fatal(err)
	}
	if err := store.DataDB().QueryRowContext(ctx, `SELECT payload_json FROM usage_events WHERE event_id='evt_late_after_change'`).Scan(&payload); err != nil || payload != "{}" {
		t.Fatalf("late payload after retention increase = %q, error = %v", payload, err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO stored_responses(id,owner_user_id,key_id,model_id,body_json,created_at,expires_at) VALUES('resp_due','usr_owner','key_old','public-model','{}',0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO stored_chat_completions(id,owner_user_id,key_id,model_id,body_json,request_json,metadata_json,created_at,expires_at) VALUES('chatcmpl_due','usr_owner','key_old','public-model','{}','{}','{}',0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO message_batches(id,owner_user_id,key_id,created_at,expires_at) VALUES('msgbatch_due','usr_owner','key_old',0,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO message_batch_items(batch_id,ordinal,custom_id,params_json,reserved_result_bytes) VALUES('msgbatch_due',1,'due_item','{"messages":[]}',1024)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO openai_uploads(id,owner_user_id,key_id,filename,purpose,mime_type,expected_bytes,file_expiry_seconds,created_at,expires_at) VALUES('upload_due','usr_owner','key_old','due.jsonl','batch','application/jsonl',1,3600,-3600001,-1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO openai_upload_parts(id,upload_id,bytes,ciphertext,nonce,created_at) VALUES('part_due','upload_due',1,?,?,0)`, make([]byte, 17), make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO openai_files(id,owner_user_id,key_id,filename,purpose,bytes,ciphertext,nonce,created_at,expires_at) VALUES('file_due','usr_owner','key_old','due.jsonl','batch',0,?,?,0,3600000)`, make([]byte, 16), make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO openai_vector_stores(id,owner_user_id,key_id,name,metadata_json,created_at,last_active_at,expires_after_days,expires_at) VALUES('vs_due','usr_owner','key_old','Due','{}',0,0,1,86400000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO openai_batches(id,owner_user_id,key_id,input_file_id,endpoint,completion_window,model_id,status,metadata_json,output_expiry_seconds,output_file_id,request_total,request_completed,created_at,in_progress_at,expires_at,terminal_at,retention_expires_at) VALUES('batch_due','usr_owner','key_old','file_due','/v1/responses','24h','public-model','completed','{}',3600,'file_due',1,1,0,0,86400000,1,2592000000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO conversations(id,owner_user_id,key_id,metadata_json,created_at,deleted_at) VALUES('conv_due','usr_owner','key_old','{}',0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO conversation_items(conversation_id,id,ordinal,body_json,created_at) VALUES('conv_due','citem_due',1,'{}',0)`); err != nil {
		t.Fatal(err)
	}
	if err := service.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM stored_responses`).Scan(&storedResponses); err != nil || storedResponses != 0 {
		t.Fatalf("scheduled expired responses = %d, error = %v", storedResponses, err)
	}
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM stored_chat_completions`).Scan(&storedChats); err != nil || storedChats != 0 {
		t.Fatalf("scheduled expired chats = %d, error = %v", storedChats, err)
	}
	_ = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_batches`).Scan(&batches)
	_ = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_batch_items`).Scan(&batchItems)
	if batches != 0 || batchItems != 0 {
		t.Fatalf("scheduled message batch retention left batches=%d items=%d", batches, batchItems)
	}
	_ = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_uploads`).Scan(&uploads)
	_ = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_upload_parts`).Scan(&uploadParts)
	if uploads != 0 || uploadParts != 0 {
		t.Fatalf("scheduled OpenAI upload retention left uploads=%d parts=%d", uploads, uploadParts)
	}
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_files`).Scan(&files); err != nil || files != 0 {
		t.Fatalf("scheduled OpenAI file retention left files=%d error=%v", files, err)
	}
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_vector_stores`).Scan(&vectorStores); err != nil || vectorStores != 0 {
		t.Fatalf("scheduled OpenAI Vector Store retention left stores=%d error=%v", vectorStores, err)
	}
	var openAIBatches int
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_batches`).Scan(&openAIBatches); err != nil || openAIBatches != 0 {
		t.Fatalf("scheduled OpenAI batch retention left batches=%d error=%v", openAIBatches, err)
	}
	_ = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations WHERE id='conv_due'`).Scan(&conversations)
	_ = store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_items WHERE id='citem_due'`).Scan(&conversationItems)
	if conversations != 0 || conversationItems != 0 {
		t.Fatalf("scheduled conversation retention left conversations=%d items=%d", conversations, conversationItems)
	}
	items, _, err := usage.New(store.SystemDB()).ListRequests(ctx, auth.User{ID: "usr_owner", Role: "owner"}, usage.UsageQuery{})
	if err != nil || len(items) != 0 {
		t.Fatalf("visible retained requests = %+v, error = %v", items, err)
	}
}

func TestConfigPreviewRejectsPrimaryTargetMismatch(t *testing.T) {
	bundle := ConfigBundle{
		Format:   1,
		Settings: Settings{BackupIntervalHours: 24, BackupRetention: 14, BackupDestination: "local", S3Region: "us-east-1", S3AccessKeyEnv: "AWS_ACCESS_KEY_ID", S3SecretKeyEnv: "AWS_SECRET_ACCESS_KEY", RequestRetention: 90, AuditRetention: 365},
		Catalog:  ConfigCatalog{RefreshIntervalHours: 24},
		Connections: []ConfigConnection{
			{ID: "con_a", Name: "A", Adapter: "openai", BaseURL: "https://api.openai.com/v1", Enabled: true, TimeoutMS: 60000, Preset: "openai"},
			{ID: "con_b", Name: "B", Adapter: "anthropic", BaseURL: "https://api.anthropic.com/v1", Enabled: true, TimeoutMS: 60000, Preset: "anthropic"},
		},
		UpstreamModels: []ConfigUpstream{{ID: "up_a", ConnectionID: "con_a", UpstreamID: "a", Capabilities: []string{"chat"}, Active: true}, {ID: "up_b", ConnectionID: "con_b", UpstreamID: "b", Capabilities: []string{"chat"}, Active: true}},
		PublicModels:   []ConfigModel{{ID: "assistant", Label: "Assistant", TargetConnectionID: "con_a", TargetModelID: "up_a", Capabilities: []string{"chat"}, Active: true, RoutingStrategy: "ordered_fallback"}},
		Targets:        []ConfigTarget{{PublicModelID: "assistant", UpstreamModelID: "up_b", Priority: 1, Weight: 1, Enabled: true}, {PublicModelID: "assistant", UpstreamModelID: "up_a", Priority: 2, Weight: 1, Enabled: true}},
	}
	if _, err := PreviewConfig(bundle); err == nil {
		t.Fatal("primary target mismatch was accepted")
	}
}

func TestConfigImportClearsCredentialWhenEndpointChanges(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES('usr_owner','owner@example.test','Owner','hash','owner','active',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO provider_connections(id,name,adapter,base_url,enabled,allow_private_network,timeout_ms,preset,created_at,updated_at) VALUES('con_test','Test','openai','https://api.openai.com',1,0,60000,'custom',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO provider_credentials(connection_id,external_ref,updated_at) VALUES('con_test','TEST_API_KEY',?)`, now); err != nil {
		t.Fatal(err)
	}
	service := New(store, providers.New(store.SystemDB(), make([]byte, 32)), "test", func(string) string { return "" })
	bundle, err := service.ExportConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Connections[0].BaseURL = "https://example.com/v1"
	if _, err := service.ImportConfig(ctx, "usr_owner", bundle); err != nil {
		t.Fatal(err)
	}
	var credentials int
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_credentials WHERE connection_id='con_test'`).Scan(&credentials); err != nil || credentials != 0 {
		t.Fatalf("credentials = %d, error = %v", credentials, err)
	}
}

func TestConfigImportRejectsIncompatiblePreservedModel(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UnixMilli()
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash,role,status,inference_unrestricted,created_at,updated_at) VALUES('usr_owner','owner@example.test','Owner','hash','owner','active',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO provider_connections(id,name,adapter,base_url,enabled,allow_private_network,timeout_ms,preset,created_at,updated_at) VALUES('con_test','Test','openai_compatible','https://api.example.com/v1',1,0,60000,'custom',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO upstream_models(id,connection_id,upstream_id,capabilities_json,created_at,updated_at) VALUES('up_test','con_test','embedding','["embeddings"]',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SystemDB().ExecContext(ctx, `INSERT INTO upstream_models(id,connection_id,upstream_id,capabilities_json,created_at,updated_at) VALUES('up_translation','con_test','provider-translation','["audio_translation"]',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	service := New(store, providers.New(store.SystemDB(), make([]byte, 32)), "test", func(string) string { return "" })
	bundle, err := service.ExportConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Connections[0].Preset = "openai"
	bundle.Connections[0].BaseURL = "https://api.openai.com/v1"
	bundle.UpstreamModels = nil
	if _, err = service.ImportConfig(ctx, "usr_owner", bundle); err == nil {
		t.Fatal("partial import stranded a preserved non-whisper OpenAI translation model")
	}
}

func TestAuditFiltersAndPaginates(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, values := range [][]any{{"aud_3", "provider.update", "provider_connection", int64(3)}, {"aud_2", "provider.update", "provider_connection", int64(2)}, {"aud_1", "user.create", "user", int64(1)}} {
		if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO audit_events(id,action,resource_type,resource_id,created_at) VALUES(?,?,?,'test',?)`, values...); err != nil {
			t.Fatal(err)
		}
	}
	service := New(store, providers.New(store.SystemDB(), make([]byte, 32)), "test", func(string) string { return "" })
	items, next, err := service.Audit(ctx, AuditQuery{Limit: 1, Action: "provider.update"})
	if err != nil || len(items) != 1 || items[0].ID != "aud_3" || next == "" {
		t.Fatalf("first page = %+v, next = %q, error = %v", items, next, err)
	}
	items, next, err = service.Audit(ctx, AuditQuery{Limit: 1, Action: "provider.update", Cursor: next})
	if err != nil || len(items) != 1 || items[0].ID != "aud_2" || next != "" {
		t.Fatalf("second page = %+v, next = %q, error = %v", items, next, err)
	}
}

func TestS3UploadSignsAndRetries(t *testing.T) {
	var attempts int
	previousClient := s3Client
	t.Cleanup(func() { s3Client = previousClient })
	s3Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		if request.Method != http.MethodPut || request.URL.Path != "/bucket/backups/test.pagbak" || request.ContentLength != int64(len("encrypted-backup")) || !strings.HasPrefix(request.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=access/") || request.Header.Get("x-amz-content-sha256") == "" || request.Header.Get("x-amz-checksum-sha256") == "" {
			t.Errorf("unexpected request: %s %s headers=%v", request.Method, request.URL.Path, request.Header)
		}
		body, _ := io.ReadAll(request.Body)
		if string(body) != "encrypted-backup" {
			t.Errorf("body = %q", body)
		}
		if attempts == 1 {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("retry")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	filename := filepath.Join(t.TempDir(), "test.pagbak")
	if err := os.WriteFile(filename, []byte("encrypted-backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := Settings{S3Endpoint: "http://127.0.0.1:9000", S3Region: "us-east-1", S3Bucket: "bucket", S3Prefix: "backups", S3AccessKeyEnv: "ACCESS", S3SecretKeyEnv: "SECRET"}
	if err := uploadS3(context.Background(), filename, "test.pagbak", settings, func(name string) string { return map[string]string{"ACCESS": "access", "SECRET": "secret"}[name] }); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d", attempts)
	}
}

func TestS3BackupUploadsEncryptedPairedSnapshotThatRestores(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	store, err := storage.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO gateway_metadata(key,value) VALUES('s3_system','system-value')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DataDB().ExecContext(ctx, `INSERT INTO projection_metadata(key,value) VALUES('s3_data','data-value')`); err != nil {
		t.Fatal(err)
	}
	masterKey := []byte("01234567890123456789012345678901")
	if err := os.WriteFile(filepath.Join(dataDir, "master.key"), masterKey, 0o600); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(root, "staging")
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE operation_settings SET backup_destination='s3',local_directory=?,s3_endpoint='http://127.0.0.1:9000',s3_region='us-east-1',s3_bucket='bucket',s3_prefix='backups',s3_access_key_env='ACCESS',s3_secret_key_env='SECRET' WHERE singleton=1`, staging); err != nil {
		t.Fatal(err)
	}

	backupKey := make([]byte, 32)
	if _, err := rand.Read(backupKey); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{
		"POCKET_AI_GATEWAY_BACKUP_KEY": base64.StdEncoding.EncodeToString(backupKey),
		"ACCESS":                       "access",
		"SECRET":                       "secret",
	}
	var uploaded []byte
	var objectPath string
	previousClient := s3Client
	t.Cleanup(func() { s3Client = previousClient })
	s3Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		objectPath = request.URL.Path
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			return nil, readErr
		}
		uploaded = body
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	service := New(store, providers.New(store.SystemDB(), masterKey), "test", func(name string) string { return environment[name] })
	job, err := service.RunBackup(ctx, "")
	if err != nil || job.State != "succeeded" || job.Destination != "s3" || len(uploaded) == 0 || objectPath != "/bucket/backups/"+job.ArchiveName {
		t.Fatalf("backup = %+v, object = %q, uploaded = %d, error = %v", job, objectPath, len(uploaded), err)
	}
	if _, err := os.Stat(filepath.Join(staging, job.ArchiveName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("S3 staging archive remains: %v", err)
	}
	jobs, err := service.ListBackups(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].ID != job.ID || jobs[0].State != "succeeded" || jobs[0].Checksum != job.Checksum {
		t.Fatalf("stored backup jobs = %+v, error = %v", jobs, err)
	}
	archive := filepath.Join(root, job.ArchiveName)
	if err := os.WriteFile(archive, uploaded, 0o600); err != nil {
		t.Fatal(err)
	}
	checksum, size, err := hashArchive(ctx, archive)
	if err != nil || checksum != job.Checksum || size != job.SizeBytes {
		t.Fatalf("uploaded archive = %q/%d, job = %q/%d, error = %v", checksum, size, job.Checksum, job.SizeBytes, err)
	}
	restoredDir := filepath.Join(root, "restored")
	if err := storage.RestoreEncryptedSnapshot(ctx, archive, restoredDir, backupKey); err != nil {
		t.Fatal(err)
	}
	restored, err := storage.Open(ctx, restoredDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var systemValue, dataValue string
	if err := restored.SystemDB().QueryRowContext(ctx, `SELECT value FROM gateway_metadata WHERE key='s3_system'`).Scan(&systemValue); err != nil || systemValue != "system-value" {
		t.Fatalf("restored system value = %q, error = %v", systemValue, err)
	}
	if err := restored.DataDB().QueryRowContext(ctx, `SELECT value FROM projection_metadata WHERE key='s3_data'`).Scan(&dataValue); err != nil || dataValue != "data-value" {
		t.Fatalf("restored data value = %q, error = %v", dataValue, err)
	}
	gotMasterKey, err := os.ReadFile(filepath.Join(restoredDir, "master.key"))
	if err != nil || string(gotMasterKey) != string(masterKey) {
		t.Fatalf("restored master key mismatch: %v", err)
	}
}

func TestArchiveHashHonorsCancellation(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "archive")
	if err := os.WriteFile(filename, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := hashArchive(ctx, filename); !errors.Is(err, context.Canceled) {
		t.Fatalf("hash error = %v", err)
	}
}

func TestBackupFailureIsRecorded(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, providers.New(store.SystemDB(), make([]byte, 32)), "test", func(string) string { return "" })
	if jobs, err := service.ListBackups(ctx); err != nil || jobs == nil {
		t.Fatalf("empty backup list = %#v, error = %v", jobs, err)
	}
	if events, _, err := service.Audit(ctx, AuditQuery{Limit: 10}); err != nil || events == nil {
		t.Fatalf("empty audit list = %#v, error = %v", events, err)
	}
	if _, err := service.RunBackup(ctx, ""); err == nil {
		t.Fatal("backup without an encryption key succeeded")
	}
	jobs, err := service.ListBackups(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].State != "failed" || jobs[0].Error == "" {
		t.Fatalf("jobs = %+v, error = %v", jobs, err)
	}
}

func TestRecoverMarksRunningBackupsInterrupted(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UnixMilli()
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO backup_jobs(id,state,destination,archive_name,started_at) VALUES('bak_running','running','local','running.pagbak',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO backup_jobs(id,state,destination,archive_name,started_at,finished_at) VALUES('bak_succeeded','succeeded','local','succeeded.pagbak',?,?)`, now-1, now); err != nil {
		t.Fatal(err)
	}
	service := New(store, providers.New(store.SystemDB(), make([]byte, 32)), "test", func(string) string { return "" })
	if err := service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	var state, message string
	var finished sql.NullInt64
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state,error,finished_at FROM backup_jobs WHERE id='bak_running'`).Scan(&state, &message, &finished); err != nil || state != "failed" || message != "backup outcome unknown after process restart or restore" || !finished.Valid {
		t.Fatalf("recovered backup = %q/%q/%v, error = %v", state, message, finished, err)
	}
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT state FROM backup_jobs WHERE id='bak_succeeded'`).Scan(&state); err != nil || state != "succeeded" {
		t.Fatalf("completed backup state = %q, error = %v", state, err)
	}
}

func TestScheduledBackupRetriesAfterFailedAttempt(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	if _, err := store.SystemDB().ExecContext(ctx, `UPDATE operation_settings SET backup_enabled=1,backup_interval_hours=24 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO backup_jobs(id,state,destination,archive_name,started_at,finished_at) VALUES('bak_old','succeeded','local','old.pagbak',?,?)`, now.Add(-25*time.Hour).UnixMilli(), now.Add(-25*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SystemDB().ExecContext(ctx, `INSERT INTO backup_jobs(id,state,destination,archive_name,error,started_at,finished_at) VALUES('bak_failed','failed','local','failed.pagbak','failed',?,?)`, now.UnixMilli(), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	service := New(store, providers.New(store.SystemDB(), make([]byte, 32)), "test", func(string) string { return "" })
	if err := service.RunDue(ctx); err == nil {
		t.Fatal("scheduled backup was suppressed by a failed attempt")
	}
	var jobs int
	if err := store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_jobs`).Scan(&jobs); err != nil || jobs != 3 {
		t.Fatalf("backup jobs = %d, error = %v", jobs, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
