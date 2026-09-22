package operations

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

var (
	ErrDenied   = errors.New("operation is not permitted")
	ErrConflict = errors.New("settings changed")
	ErrInvalid  = errors.New("settings are invalid")
)

type Settings struct {
	BackupEnabled       bool   `json:"backup_enabled"`
	BackupIntervalHours int64  `json:"backup_interval_hours"`
	BackupRetention     int64  `json:"backup_retention_count"`
	BackupDestination   string `json:"backup_destination"`
	LocalDirectory      string `json:"local_directory"`
	S3Endpoint          string `json:"s3_endpoint"`
	S3Region            string `json:"s3_region"`
	S3Bucket            string `json:"s3_bucket"`
	S3Prefix            string `json:"s3_prefix"`
	S3AccessKeyEnv      string `json:"s3_access_key_env"`
	S3SecretKeyEnv      string `json:"s3_secret_key_env"`
	RequestRetention    int64  `json:"request_retention_days"`
	AuditRetention      int64  `json:"audit_retention_days"`
	Revision            int64  `json:"revision"`
	UpdatedAt           string `json:"updated_at"`
	BackupKeyConfigured bool   `json:"backup_key_configured"`
}

type BackupJob struct {
	ID                 string `json:"id"`
	State              string `json:"state"`
	Destination        string `json:"destination"`
	ArchiveName        string `json:"archive_name"`
	Checksum           string `json:"checksum"`
	SizeBytes          int64  `json:"size_bytes"`
	SnapshotGeneration string `json:"snapshot_generation"`
	Error              string `json:"error"`
	StartedAt          string `json:"started_at"`
	FinishedAt         string `json:"finished_at,omitempty"`
}

type AuditEvent struct {
	ID          string         `json:"id"`
	ActorUserID sql.NullString `json:"-"`
	Actor       string         `json:"actor_user_id,omitempty"`
	Action      string         `json:"action"`
	Resource    string         `json:"resource_type"`
	ResourceID  string         `json:"resource_id"`
	DetailJSON  string         `json:"detail_json"`
	CreatedAt   string         `json:"created_at"`
}

type AuditQuery struct {
	Limit                 int
	From, To              int64
	Cursor, Actor, Action string
	Resource              string
}

type Diagnostics struct {
	Version             string `json:"version"`
	SQLiteVersion       string `json:"sqlite_version"`
	UptimeSeconds       int64  `json:"uptime_seconds"`
	Goroutines          int    `json:"goroutines"`
	SystemBytes         int64  `json:"system_database_bytes"`
	DataBytes           int64  `json:"data_database_bytes"`
	PendingOutboxEvents int64  `json:"pending_outbox_events"`
	BackupKeyConfigured bool   `json:"backup_key_configured"`
	LatestBackupState   string `json:"latest_backup_state"`
}

type Service struct {
	store     *storage.Store
	providers *providers.Service
	version   string
	getenv    func(string) string
	started   time.Time
	running   atomic.Bool
}

func New(store *storage.Store, providerService *providers.Service, version string, getenv func(string) string) *Service {
	return &Service{store: store, providers: providerService, version: version, getenv: getenv, started: time.Now()}
}

func (service *Service) Recover(ctx context.Context) error {
	_, err := service.store.SystemDB().ExecContext(ctx, `UPDATE backup_jobs SET state='failed',error='backup outcome unknown after process restart or restore',finished_at=? WHERE state='running'`, time.Now().UnixMilli())
	return err
}

func (service *Service) Settings(ctx context.Context) (Settings, error) {
	var value Settings
	var enabled bool
	var updated int64
	err := service.store.SystemDB().QueryRowContext(ctx, `SELECT backup_enabled,backup_interval_hours,backup_retention_count,backup_destination,local_directory,s3_endpoint,s3_region,s3_bucket,s3_prefix,s3_access_key_env,s3_secret_key_env,request_retention_days,audit_retention_days,revision,updated_at FROM operation_settings WHERE singleton=1`).Scan(
		&enabled, &value.BackupIntervalHours, &value.BackupRetention, &value.BackupDestination, &value.LocalDirectory, &value.S3Endpoint, &value.S3Region, &value.S3Bucket, &value.S3Prefix, &value.S3AccessKeyEnv, &value.S3SecretKeyEnv, &value.RequestRetention, &value.AuditRetention, &value.Revision, &updated,
	)
	value.BackupEnabled = enabled
	value.UpdatedAt = formatTime(updated)
	_, keyErr := storage.DecodeBackupKey(service.getenv("POCKET_AI_GATEWAY_BACKUP_KEY"))
	value.BackupKeyConfigured = keyErr == nil
	return value, err
}

func (service *Service) UpdateSettings(ctx context.Context, actor string, expected int64, value Settings) (Settings, error) {
	normalizeSettings(&value)
	if err := validateSettings(value); err != nil {
		return Settings{}, err
	}
	tx, err := service.store.SystemDB().BeginTx(ctx, nil)
	if err != nil {
		return Settings{}, err
	}
	defer tx.Rollback()
	if err := requireOwner(ctx, tx, actor); err != nil {
		return Settings{}, err
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE operation_settings SET backup_enabled=?,backup_interval_hours=?,backup_retention_count=?,backup_destination=?,local_directory=?,s3_endpoint=?,s3_region=?,s3_bucket=?,s3_prefix=?,s3_access_key_env=?,s3_secret_key_env=?,request_retention_days=?,audit_retention_days=?,revision=revision+1,updated_at=? WHERE singleton=1 AND revision=?`,
		value.BackupEnabled, value.BackupIntervalHours, value.BackupRetention, value.BackupDestination, value.LocalDirectory, value.S3Endpoint, value.S3Region, value.S3Bucket, value.S3Prefix, value.S3AccessKeyEnv, value.S3SecretKeyEnv, value.RequestRetention, value.AuditRetention, now, expected)
	if err != nil {
		return Settings{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return Settings{}, ErrConflict
	}
	if err := insertAudit(ctx, tx, actor, "settings.update", "operations", "settings", `{}`); err != nil {
		return Settings{}, err
	}
	if err := tx.Commit(); err != nil {
		return Settings{}, err
	}
	return service.Settings(ctx)
}

func normalizeSettings(value *Settings) {
	value.BackupDestination = strings.TrimSpace(value.BackupDestination)
	value.LocalDirectory = strings.TrimSpace(value.LocalDirectory)
	value.S3Endpoint = strings.TrimRight(strings.TrimSpace(value.S3Endpoint), "/")
	value.S3Region = strings.TrimSpace(value.S3Region)
	value.S3Bucket = strings.TrimSpace(value.S3Bucket)
	value.S3Prefix = strings.Trim(strings.TrimSpace(value.S3Prefix), "/")
	value.S3AccessKeyEnv = strings.TrimSpace(value.S3AccessKeyEnv)
	value.S3SecretKeyEnv = strings.TrimSpace(value.S3SecretKeyEnv)
}

func validateSettings(value Settings) error {
	if value.BackupIntervalHours < 1 || value.BackupIntervalHours > 720 || value.BackupRetention < 1 || value.BackupRetention > 365 || value.RequestRetention < 1 || value.RequestRetention > 3650 || value.AuditRetention < 30 || value.AuditRetention > 3650 {
		return ErrInvalid
	}
	if value.BackupDestination != "local" && value.BackupDestination != "s3" {
		return ErrInvalid
	}
	if value.BackupDestination == "s3" && (value.S3Endpoint == "" || value.S3Region == "" || value.S3Bucket == "" || value.S3AccessKeyEnv == "" || value.S3SecretKeyEnv == "") {
		return ErrInvalid
	}
	for _, name := range []string{value.S3AccessKeyEnv, value.S3SecretKeyEnv} {
		if name != "" && !validEnvironmentName(name) {
			return ErrInvalid
		}
	}
	return nil
}

func validEnvironmentName(value string) bool {
	for index, char := range value {
		if !(char == '_' || char >= 'A' && char <= 'Z' || index > 0 && char >= '0' && char <= '9') {
			return false
		}
	}
	return value != ""
}

func (service *Service) ListBackups(ctx context.Context) ([]BackupJob, error) {
	rows, err := service.store.SystemDB().QueryContext(ctx, `SELECT id,state,destination,archive_name,checksum,size_bytes,snapshot_generation,error,started_at,finished_at FROM backup_jobs ORDER BY started_at DESC,id DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]BackupJob, 0)
	for rows.Next() {
		var item BackupJob
		var started int64
		var finished sql.NullInt64
		if err := rows.Scan(&item.ID, &item.State, &item.Destination, &item.ArchiveName, &item.Checksum, &item.SizeBytes, &item.SnapshotGeneration, &item.Error, &started, &finished); err != nil {
			return nil, err
		}
		item.StartedAt = formatTime(started)
		if finished.Valid {
			item.FinishedAt = formatTime(finished.Int64)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (service *Service) RunBackup(ctx context.Context, actor string) (job BackupJob, err error) {
	if !service.running.CompareAndSwap(false, true) {
		return BackupJob{}, errors.New("a backup is already running")
	}
	defer service.running.Store(false)
	settings, err := service.Settings(ctx)
	if err != nil {
		return BackupJob{}, err
	}
	id, err := credentials.RandomToken(12)
	if err != nil {
		return BackupJob{}, err
	}
	id = "bak_" + id
	archiveName := time.Now().UTC().Format("20060102T150405Z") + "-" + id + ".pagbak"
	now := time.Now().UnixMilli()
	tx, err := service.store.SystemDB().BeginTx(ctx, nil)
	if err != nil {
		return BackupJob{}, err
	}
	if err = requireOwner(ctx, tx, actor); err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO backup_jobs(id,state,destination,archive_name,started_at) VALUES(?,'running',?,?,?)`, id, settings.BackupDestination, archiveName, now)
	}
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if err != nil {
		return BackupJob{}, err
	}
	defer func() {
		if err == nil {
			return
		}
		message := err.Error()
		if len(message) > 500 {
			message = message[:500]
		}
		_, _ = service.store.SystemDB().ExecContext(context.Background(), `UPDATE backup_jobs SET state='failed',error=?,finished_at=? WHERE id=? AND state='running'`, message, time.Now().UnixMilli(), id)
	}()
	key, err := storage.DecodeBackupKey(service.getenv("POCKET_AI_GATEWAY_BACKUP_KEY"))
	if err != nil {
		return BackupJob{}, err
	}
	backupDirectory, err := service.backupDirectory(settings)
	if err != nil {
		return BackupJob{}, err
	}
	archive := filepath.Join(backupDirectory, archiveName)
	manifest, checksum, size, err := storage.CreateEncryptedSnapshot(ctx, service.store, archive, service.version, key)
	if err != nil {
		return BackupJob{}, err
	}
	if settings.BackupDestination == "s3" {
		if err = uploadS3(ctx, archive, archiveName, settings, service.getenv); err != nil {
			_ = os.Remove(archive)
			return BackupJob{}, err
		}
		_ = os.Remove(archive)
	}
	warning := ""
	if settings.BackupDestination == "local" {
		if pruneErr := pruneLocalBackups(backupDirectory, settings.BackupRetention); pruneErr != nil {
			warning = "backup succeeded; retention cleanup failed"
		}
	}
	finished := time.Now().UnixMilli()
	tx, err = service.store.SystemDB().BeginTx(ctx, nil)
	if err != nil {
		return BackupJob{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE backup_jobs SET state='succeeded',checksum=?,size_bytes=?,snapshot_generation=?,error=?,finished_at=? WHERE id=? AND state='running'`, checksum, size, manifest.Generation, warning, finished, id); err != nil {
		return BackupJob{}, err
	}
	if actor != "" {
		if err = insertAudit(ctx, tx, actor, "backup.create", "backup", id, `{}`); err != nil {
			return BackupJob{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return BackupJob{}, err
	}
	return BackupJob{ID: id, State: "succeeded", Destination: settings.BackupDestination, ArchiveName: archiveName, Checksum: checksum, SizeBytes: size, SnapshotGeneration: manifest.Generation, Error: warning, StartedAt: formatTime(now), FinishedAt: formatTime(finished)}, nil
}

func (service *Service) backupDirectory(settings Settings) (string, error) {
	directory := settings.LocalDirectory
	if directory == "" {
		directory = filepath.Join(filepath.Dir(service.store.DataDir()), filepath.Base(service.store.DataDir())+"_backups")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	data, _ := filepath.Abs(service.store.DataDir())
	if within(absolute, data) {
		return "", errors.New("backup directory must be outside the data directory")
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return "", err
	}
	return absolute, nil
}

func pruneLocalBackups(directory string, keep int64) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".pagbak") {
			names = append(names, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, name := range names[min(int(keep), len(names)):] {
		if err := os.Remove(filepath.Join(directory, name)); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) RunDue(ctx context.Context) error {
	if _, err := service.store.SystemDB().ExecContext(ctx, `DELETE FROM stored_responses WHERE expires_at < ?`, time.Now().UnixMilli()); err != nil {
		return err
	}
	if _, err := service.store.SystemDB().ExecContext(ctx, `DELETE FROM stored_chat_completions WHERE expires_at < ?`, time.Now().UnixMilli()); err != nil {
		return err
	}
	if _, err := service.store.SystemDB().ExecContext(ctx, `DELETE FROM message_batches WHERE expires_at < ?`, time.Now().UnixMilli()); err != nil {
		return err
	}
	if _, err := service.store.SystemDB().ExecContext(ctx, `DELETE FROM openai_uploads WHERE expires_at < ?`, time.Now().UnixMilli()); err != nil {
		return err
	}
	if _, err := service.store.SystemDB().ExecContext(ctx, `DELETE FROM openai_batches WHERE retention_expires_at < ?`, time.Now().UnixMilli()); err != nil {
		return err
	}
	if _, err := service.store.SystemDB().ExecContext(ctx, `DELETE FROM openai_files WHERE expires_at < ?`, time.Now().UnixMilli()); err != nil {
		return err
	}
	if _, err := service.store.SystemDB().ExecContext(ctx, `DELETE FROM openai_vector_stores WHERE expires_at IS NOT NULL AND expires_at < ?`, time.Now().UnixMilli()); err != nil {
		return err
	}
	if _, _, err := purgeDeletedConversations(ctx, service.store.SystemDB(), time.Now().Add(-30*24*time.Hour).UnixMilli()); err != nil {
		return err
	}
	settings, err := service.Settings(ctx)
	if err != nil || !settings.BackupEnabled {
		return err
	}
	var latest sql.NullInt64
	if err := service.store.SystemDB().QueryRowContext(ctx, `SELECT MAX(started_at) FROM backup_jobs WHERE state='succeeded'`).Scan(&latest); err != nil {
		return err
	}
	if latest.Valid && time.Since(time.UnixMilli(latest.Int64)) < time.Duration(settings.BackupIntervalHours)*time.Hour {
		return nil
	}
	_, err = service.RunBackup(ctx, "")
	return err
}

func (service *Service) RunRetention(ctx context.Context, actor string) (map[string]int64, error) {
	settings, err := service.Settings(ctx)
	if err != nil {
		return nil, err
	}
	requestCutoff := time.Now().Add(-time.Duration(settings.RequestRetention) * 24 * time.Hour).UnixMilli()
	auditCutoff := time.Now().Add(-time.Duration(settings.AuditRetention) * 24 * time.Hour).UnixMilli()
	counts := map[string]int64{}
	unlock := service.store.LockProjection()
	defer unlock()
	tx, err := service.store.SystemDB().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := requireOwner(ctx, tx, actor); err != nil {
		return nil, err
	}
	finalized := `request_id IN (SELECT id FROM requests WHERE finished_at IS NOT NULL AND finished_at < ? AND state IN ('succeeded','failed','cancelled') AND retained_at IS NULL)`
	result, err := tx.ExecContext(ctx, `UPDATE attempts SET upstream_model_record_id='',upstream_model_id='',target_dialect='',target_operation='',translation_applied=0,request_tool_count=0,response_tool_call_count=0,tool_call_status='none',selection_reason='',rejected_candidates_json='[]' WHERE `+finalized, requestCutoff)
	if err != nil {
		return nil, err
	}
	counts["request_attempt_details"] = rowsAffected(result)
	result, err = tx.ExecContext(ctx, `UPDATE requests SET operation='',dialect='',retained_at=? WHERE finished_at IS NOT NULL AND finished_at < ? AND state IN ('succeeded','failed','cancelled') AND retained_at IS NULL`, time.Now().UnixMilli(), requestCutoff)
	if err != nil {
		return nil, err
	}
	counts["request_history"] = rowsAffected(result)
	result, err = tx.ExecContext(ctx, `DELETE FROM stored_responses WHERE expires_at < ?`, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	counts["stored_responses"] = rowsAffected(result)
	result, err = tx.ExecContext(ctx, `DELETE FROM stored_chat_completions WHERE expires_at < ?`, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	counts["stored_chat_completions"] = rowsAffected(result)
	result, err = tx.ExecContext(ctx, `DELETE FROM message_batches WHERE expires_at < ?`, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	counts["message_batches"] = rowsAffected(result)
	result, err = tx.ExecContext(ctx, `DELETE FROM openai_uploads WHERE expires_at < ?`, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	counts["openai_uploads"] = rowsAffected(result)
	result, err = tx.ExecContext(ctx, `DELETE FROM openai_batches WHERE retention_expires_at < ?`, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	counts["openai_batches"] = rowsAffected(result)
	result, err = tx.ExecContext(ctx, `DELETE FROM openai_files WHERE expires_at < ?`, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	counts["openai_files"] = rowsAffected(result)
	result, err = tx.ExecContext(ctx, `DELETE FROM openai_vector_stores WHERE expires_at IS NOT NULL AND expires_at < ?`, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	counts["openai_vector_stores"] = rowsAffected(result)
	items, conversations, err := purgeDeletedConversations(ctx, tx, time.Now().Add(-30*24*time.Hour).UnixMilli())
	if err != nil {
		return nil, err
	}
	counts["conversation_items"], counts["conversations"] = items, conversations
	result, err = tx.ExecContext(ctx, `DELETE FROM audit_events WHERE created_at < ?`, auditCutoff)
	if err != nil {
		return nil, err
	}
	counts["audit_events"] = rowsAffected(result)
	if err := insertAudit(ctx, tx, actor, "retention.run", "operations", "retention", `{}`); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	dataTx, err := service.store.DataDB().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer dataTx.Rollback()
	if _, err = dataTx.ExecContext(ctx, `INSERT INTO projection_metadata(key,value,updated_at) VALUES('event_detail_cutoff',?,CURRENT_TIMESTAMP) ON CONFLICT(key) DO UPDATE SET value=CASE WHEN CAST(excluded.value AS INTEGER)>CAST(value AS INTEGER) THEN excluded.value ELSE value END,updated_at=excluded.updated_at`, strconv.FormatInt(requestCutoff, 10)); err != nil {
		return nil, fmt.Errorf("save projected event retention cutoff: %w", err)
	}
	var effectiveCutoff int64
	if err = dataTx.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM projection_metadata WHERE key='event_detail_cutoff'`).Scan(&effectiveCutoff); err != nil {
		return nil, fmt.Errorf("read projected event retention cutoff: %w", err)
	}
	result, err = dataTx.ExecContext(ctx, `UPDATE usage_events SET payload_json='{}' WHERE created_at < ? AND payload_json <> '{}'`, effectiveCutoff)
	if err != nil {
		return nil, fmt.Errorf("retain projected usage event details: %w", err)
	}
	counts["usage_event_details"] = rowsAffected(result)
	if err := dataTx.Commit(); err != nil {
		return nil, err
	}
	return counts, nil
}

func purgeDeletedConversations(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, before int64) (int64, int64, error) {
	items, err := executor.ExecContext(ctx, `DELETE FROM conversation_items WHERE conversation_id IN (SELECT id FROM conversations WHERE deleted_at IS NOT NULL AND deleted_at < ?)`, before)
	if err != nil {
		return 0, 0, err
	}
	conversations, err := executor.ExecContext(ctx, `DELETE FROM conversations WHERE deleted_at IS NOT NULL AND deleted_at < ?`, before)
	if err != nil {
		return 0, 0, err
	}
	return rowsAffected(items), rowsAffected(conversations), nil
}

func (service *Service) Audit(ctx context.Context, query AuditQuery) ([]AuditEvent, string, error) {
	if query.Limit < 1 || query.Limit > 200 {
		query.Limit = 50
	}
	before, beforeID, err := decodeAuditCursor(query.Cursor)
	if err != nil {
		return nil, "", errors.New("invalid cursor")
	}
	rows, err := service.store.SystemDB().QueryContext(ctx, `SELECT id,actor_user_id,action,resource_type,resource_id,detail_json,created_at FROM audit_events
		WHERE (?='' OR actor_user_id=?) AND (?='' OR action=?) AND (?='' OR resource_type=?)
		AND (?=0 OR created_at>=?) AND (?=0 OR created_at<=?)
		AND (?=0 OR created_at<? OR (created_at=? AND id<?))
		ORDER BY created_at DESC,id DESC LIMIT ?`, query.Actor, query.Actor, query.Action, query.Action, query.Resource, query.Resource, query.From, query.From, query.To, query.To, before, before, before, beforeID, query.Limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := make([]AuditEvent, 0)
	var lastCreated int64
	for rows.Next() {
		var item AuditEvent
		var created int64
		if err := rows.Scan(&item.ID, &item.ActorUserID, &item.Action, &item.Resource, &item.ResourceID, &item.DetailJSON, &created); err != nil {
			return nil, "", err
		}
		if len(items) == query.Limit {
			return items, encodeAuditCursor(lastCreated, items[len(items)-1].ID), nil
		}
		if item.ActorUserID.Valid {
			item.Actor = item.ActorUserID.String
		}
		item.CreatedAt = formatTime(created)
		lastCreated = created
		items = append(items, item)
	}
	return items, "", rows.Err()
}

func (service *Service) Diagnostics(ctx context.Context) (Diagnostics, error) {
	value := Diagnostics{Version: service.version, SQLiteVersion: service.store.SQLiteVersion(), UptimeSeconds: int64(time.Since(service.started).Seconds()), Goroutines: runtime.NumGoroutine()}
	for filename, destination := range map[string]*int64{"system.db": &value.SystemBytes, "data.db": &value.DataBytes} {
		if info, err := os.Stat(filepath.Join(service.store.DataDir(), filename)); err == nil {
			*destination = info.Size()
		}
	}
	if err := service.store.SystemDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM event_outbox WHERE delivered_at IS NULL`).Scan(&value.PendingOutboxEvents); err != nil {
		return Diagnostics{}, err
	}
	_ = service.store.SystemDB().QueryRowContext(ctx, `SELECT state FROM backup_jobs ORDER BY started_at DESC,id DESC LIMIT 1`).Scan(&value.LatestBackupState)
	_, err := storage.DecodeBackupKey(service.getenv("POCKET_AI_GATEWAY_BACKUP_KEY"))
	value.BackupKeyConfigured = err == nil
	return value, nil
}

func (service *Service) Ready(ctx context.Context) error {
	for _, database := range []*sql.DB{service.store.SystemDB(), service.store.DataDB()} {
		if err := database.PingContext(ctx); err != nil {
			return err
		}
	}
	status, err := usage.OutboxState(ctx, service.store.SystemDB())
	if err != nil {
		return err
	}
	if status.Full {
		return errors.New("usage projection backlog is full")
	}
	return nil
}

func insertAudit(ctx context.Context, tx *sql.Tx, actor, action, resource, resourceID, detail string) error {
	id, err := credentials.RandomToken(12)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_user_id,action,resource_type,resource_id,detail_json,created_at) VALUES(?,?,?,?,?,?,?)`, "aud_"+id, actor, action, resource, resourceID, detail, time.Now().UnixMilli())
	return err
}

func requireOwner(ctx context.Context, tx *sql.Tx, actor string) error {
	if actor == "" {
		return nil
	}
	var role string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM users WHERE id=? AND status='active'`, actor).Scan(&role); err != nil || role != "owner" {
		return ErrDenied
	}
	return nil
}

func rowsAffected(result sql.Result) int64 {
	count, _ := result.RowsAffected()
	return count
}

func encodeAuditCursor(created int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(created, 10) + ":" + id))
}

func decodeAuditCursor(value string) (int64, string, error) {
	if value == "" {
		return 0, "", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, "", err
	}
	created, id, ok := strings.Cut(string(decoded), ":")
	if !ok || id == "" {
		return 0, "", errors.New("invalid cursor")
	}
	at, err := strconv.ParseInt(created, 10, 64)
	return at, id, err
}

func formatTime(value int64) string { return time.UnixMilli(value).UTC().Format(time.RFC3339Nano) }

func within(candidate, parent string) bool {
	relative, err := filepath.Rel(parent, candidate)
	return err == nil && !filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
