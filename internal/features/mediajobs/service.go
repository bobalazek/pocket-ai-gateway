package mediajobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

const (
	Scope          = "media:generate"
	maxInputBytes  = 1 << 20
	maxOutputBytes = 4 << 20
)

var (
	ErrNotFound = errors.New("media job not found")
	ErrDenied   = errors.New("media job access denied")
	ErrInvalid  = errors.New("media job input is invalid")
)

type Service struct {
	database  *sql.DB
	keys      *keys.Service
	providers *providers.Service
	usage     *usage.Service
	masterKey []byte
	wake      chan struct{}
}

type CreateInput struct {
	Model     string          `json:"model"`
	MediaType string          `json:"media_type"`
	Input     json.RawMessage `json:"input"`
}

type Job struct {
	ID               string          `json:"id"`
	Object           string          `json:"object"`
	State            string          `json:"state"`
	Model            string          `json:"model"`
	MediaType        string          `json:"media_type"`
	Provider         string          `json:"provider"`
	ProviderJobID    string          `json:"provider_job_id,omitempty"`
	RequestID        string          `json:"request_id"`
	Output           json.RawMessage `json:"output,omitempty"`
	Error            json.RawMessage `json:"error,omitempty"`
	CancelRequested  bool            `json:"cancel_requested"`
	Cancelable       bool            `json:"cancelable"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
	CompletedAt      *string         `json:"completed_at,omitempty"`
	revision         int64
	connectionID     string
	keyID            string
	ownerID          string
	attemptID        string
	inputCiphertext  []byte
	inputNonce       []byte
	inputBytes       int64
	outputCiphertext []byte
	outputNonce      []byte
	outputBytes      int64
	pollFailures     int64
}

type workerJob struct {
	Job
	input json.RawMessage
}

func New(database *sql.DB, keyService *keys.Service, providerService *providers.Service, usageService *usage.Service, masterKey []byte) *Service {
	return &Service{database: database, keys: keyService, providers: providerService, usage: usageService, masterKey: append([]byte(nil), masterKey...), wake: make(chan struct{}, 1)}
}

func (service *Service) Create(ctx context.Context, principal keys.Principal, input CreateInput) (Job, error) {
	input.Model = strings.TrimSpace(input.Model)
	input.MediaType = strings.TrimSpace(input.MediaType)
	if input.MediaType == "" {
		input.MediaType = "other"
	}
	if input.Model == "" || len(input.Model) > 200 || !validMediaType(input.MediaType) || len(input.Input) < 2 || len(input.Input) > maxInputBytes || !jsonObject(input.Input) {
		return Job{}, ErrInvalid
	}
	target, err := service.providers.Target(ctx, input.Model)
	if err != nil {
		if errors.Is(err, providers.ErrNotFound) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}
	if !mediaTargetSupported(target) || !principal.Allows(Scope, input.Model, target.TargetConnectionID) || !providers.PresetSupportsModelCapabilities(target.Preset, target.UpstreamID, []string{"media_jobs"}) {
		return Job{}, ErrDenied
	}
	if target.Preset == "together" && input.MediaType != "video" {
		return Job{}, ErrInvalid
	}
	token, err := credentials.RandomToken(18)
	if err != nil {
		return Job{}, err
	}
	id := "media_" + token
	ciphertext, nonce, err := credentials.Seal(service.masterKey, input.Input, mediaJobAAD(id, principal.KeyID, int64(len(input.Input)), "input"))
	if err != nil {
		return Job{}, err
	}
	now := time.Now().UnixMilli()
	quoteAt, err := service.usage.QuoteTime(ctx)
	if err != nil {
		return Job{}, err
	}
	targetOperation := "predictions"
	if target.Preset == "together" {
		targetOperation = "videos"
	}
	admission, err := service.usage.Admit(ctx, usage.AdmissionInput{KeyID: principal.KeyID, ConnectionID: target.TargetConnectionID, ModelID: input.Model, UpstreamModelRecordID: target.TargetModelID, UpstreamModelID: target.UpstreamID, ConnectionRevision: target.ConnectionRevision, ModelRevision: target.Revision, Operation: "media/jobs", TargetOperation: targetOperation, Scope: Scope, Dialect: "gateway", TargetDialect: target.Preset, SelectionReason: "fixed media provider", PriceQuoteAt: quoteAt, PriceUnavailable: true, BodyBytes: int64(len(input.Input))})
	if err != nil {
		return Job{}, err
	}
	_, err = service.database.ExecContext(ctx, `INSERT INTO media_jobs(id,owner_user_id,key_id,request_id,attempt_id,model_id,connection_id,provider,media_type,state,input_ciphertext,input_nonce,input_bytes,next_poll_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,'queued',?,?,?,?,?,?)`, id, principal.OwnerUserID, principal.KeyID, admission.RequestID, admission.AttemptID, input.Model, target.TargetConnectionID, target.Preset, input.MediaType, ciphertext, nonce, len(input.Input), now, now, now)
	if err != nil {
		_ = service.usage.CancelBeforeDispatch(context.WithoutCancel(ctx), admission.AttemptID)
		return Job{}, err
	}
	service.signal()
	return service.get(ctx, id, principal.OwnerUserID, principal.KeyID, false)
}

func mediaTargetSupported(target providers.Target) bool {
	return target.RoutingStrategy == "fixed" && (target.Preset == "replicate" || target.Preset == "together" || target.Preset == "custom" && strings.TrimSpace(target.AdapterRequestScript) != "")
}

func (service *Service) Get(ctx context.Context, principal keys.Principal, id string) (Job, error) {
	if !principal.AllowsScope(Scope) {
		return Job{}, ErrDenied
	}
	return service.get(ctx, id, principal.OwnerUserID, principal.KeyID, true)
}

func (service *Service) AdminGet(ctx context.Context, id string) (Job, error) {
	return service.get(ctx, id, "", "", false)
}

func (service *Service) List(ctx context.Context, principal keys.Principal, limit int) ([]Job, error) {
	if !principal.AllowsScope(Scope) {
		return nil, ErrDenied
	}
	return service.list(ctx, principal.OwnerUserID, principal.KeyID, limit)
}

func (service *Service) AdminList(ctx context.Context, limit int) ([]Job, error) {
	return service.list(ctx, "", "", limit)
}

func (service *Service) Cancel(ctx context.Context, principal keys.Principal, id string) (Job, error) {
	if !principal.AllowsScope(Scope) {
		return Job{}, ErrDenied
	}
	return service.cancel(ctx, id, principal.OwnerUserID, principal.KeyID)
}

func (service *Service) AdminCancel(ctx context.Context, id string) (Job, error) {
	return service.cancel(ctx, id, "", "")
}

func (service *Service) get(ctx context.Context, id, ownerID, keyID string, includeOutput bool) (Job, error) {
	query := mediaJobSelect + " WHERE id=?"
	args := []any{id}
	if keyID != "" {
		query += " AND owner_user_id=? AND key_id=?"
		args = append(args, ownerID, keyID)
	}
	job, err := scanJob(service.database.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if includeOutput && len(job.outputCiphertext) > 0 {
		plain, err := credentials.Open(service.masterKey, job.outputCiphertext, job.outputNonce, mediaJobAAD(job.ID, job.keyID, job.outputBytes, "output"))
		if err != nil || !json.Valid(plain) {
			return Job{}, errors.New("media job output cannot be decrypted")
		}
		job.Output = plain
	}
	return job, nil
}

func (service *Service) list(ctx context.Context, ownerID, keyID string, limit int) ([]Job, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	query := mediaJobSelect
	args := []any{}
	if keyID != "" {
		query += " WHERE owner_user_id=? AND key_id=?"
		args = append(args, ownerID, keyID)
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := service.database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Job, 0)
	for rows.Next() {
		item, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (service *Service) cancel(ctx context.Context, id, ownerID, keyID string) (Job, error) {
	where := "id=?"
	args := []any{time.Now().UnixMilli(), id}
	if keyID != "" {
		where += " AND owner_user_id=? AND key_id=?"
		args = append(args, ownerID, keyID)
	}
	result, err := service.database.ExecContext(ctx, `UPDATE media_jobs SET cancel_requested=1,state=CASE WHEN state='queued' THEN 'canceled' WHEN state IN ('submitting','starting','processing') THEN 'canceling' ELSE state END,input_ciphertext=CASE WHEN state='queued' THEN NULL ELSE input_ciphertext END,input_nonce=CASE WHEN state='queued' THEN NULL ELSE input_nonce END,completed_at=CASE WHEN state='queued' THEN ? ELSE completed_at END,updated_at=?,revision=revision+1 WHERE `+where, append([]any{args[0], args[0]}, args[1:]...)...)
	if err != nil {
		return Job{}, err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return Job{}, ErrNotFound
	}
	var attemptID, state string
	if err := service.database.QueryRowContext(ctx, "SELECT attempt_id,state FROM media_jobs WHERE id=?", id).Scan(&attemptID, &state); err == nil && state == "canceled" {
		_ = service.usage.CancelBeforeDispatch(context.WithoutCancel(ctx), attemptID)
	}
	service.signal()
	return service.get(ctx, id, ownerID, keyID, false)
}

const mediaJobSelect = `SELECT id,owner_user_id,key_id,request_id,attempt_id,model_id,connection_id,provider,provider_job_id,media_type,state,input_ciphertext,input_nonce,input_bytes,output_ciphertext,output_nonce,output_bytes,error_json,cancel_requested,poll_failures,revision,created_at,updated_at,completed_at FROM media_jobs`

type scanner interface{ Scan(...any) error }

func scanJob(row scanner) (Job, error) {
	var job Job
	var created, updated int64
	var completed sql.NullInt64
	var errorJSON string
	err := row.Scan(&job.ID, &job.ownerID, &job.keyID, &job.RequestID, &job.attemptID, &job.Model, &job.connectionID, &job.Provider, &job.ProviderJobID, &job.MediaType, &job.State, &job.inputCiphertext, &job.inputNonce, &job.inputBytes, &job.outputCiphertext, &job.outputNonce, &job.outputBytes, &errorJSON, &job.CancelRequested, &job.pollFailures, &job.revision, &created, &updated, &completed)
	if err != nil {
		return Job{}, err
	}
	job.Object = "media.job"
	job.Cancelable = job.State == "queued" || job.State == "submitting" || job.State == "starting" || job.State == "processing"
	if errorJSON != "null" {
		job.Error = json.RawMessage(errorJSON)
	}
	job.CreatedAt, job.UpdatedAt = formatTime(created), formatTime(updated)
	if completed.Valid {
		value := formatTime(completed.Int64)
		job.CompletedAt = &value
	}
	return job, nil
}

func validMediaType(value string) bool {
	return value == "image" || value == "video" || value == "audio" || value == "other"
}

func jsonObject(value []byte) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}

func mediaJobAAD(id, keyID string, size int64, kind string) []byte {
	return []byte(fmt.Sprintf("media-job:%s:%s:%s:%d", kind, id, keyID, size))
}

func formatTime(value int64) string { return time.UnixMilli(value).UTC().Format(time.RFC3339Nano) }

func (service *Service) signal() {
	select {
	case service.wake <- struct{}{}:
	default:
	}
}
