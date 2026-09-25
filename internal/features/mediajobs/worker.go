package mediajobs

import (
	"context"
	"encoding/json"
	"net/http/httptrace"
	"strings"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/credentials"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
)

const (
	mediaPollInterval    = time.Second
	mediaMaxPollFailures = int64(8)
)

func (service *Service) Run(ctx context.Context) {
	service.recoverInterrupted(ctx)
	ticker := time.NewTicker(mediaPollInterval)
	defer ticker.Stop()
	for {
		for service.workOnce(ctx) {
			if ctx.Err() != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-service.wake:
		case <-ticker.C:
		}
	}
}

func (service *Service) recoverInterrupted(ctx context.Context) {
	now := time.Now().UnixMilli()
	rows, err := service.database.QueryContext(ctx, `SELECT id FROM media_jobs WHERE state='submitting' AND provider_job_id=''`)
	if err == nil {
		var ids []string
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			service.recoverSubmission(ctx, id)
		}
	}
	_, _ = service.database.ExecContext(ctx, `UPDATE media_jobs SET next_poll_at=? WHERE state IN ('starting','processing','canceling')`, now)
}

func (service *Service) workOnce(ctx context.Context) bool {
	job, ok := service.claimQueued(ctx)
	if ok {
		service.submit(ctx, job)
		return true
	}
	job, ok = service.dueActive(ctx)
	if ok {
		service.poll(ctx, job)
		return true
	}
	return false
}

func (service *Service) claimQueued(ctx context.Context) (workerJob, bool) {
	now := time.Now().UnixMilli()
	job, err := scanJob(service.database.QueryRowContext(ctx, mediaJobSelect+` WHERE id=(SELECT id FROM media_jobs WHERE state='queued' ORDER BY created_at,id LIMIT 1)`))
	if err != nil {
		return workerJob{}, false
	}
	result, err := service.database.ExecContext(ctx, `UPDATE media_jobs SET state='submitting',updated_at=?,revision=revision+1 WHERE id=? AND state='queued'`, now, job.ID)
	if err != nil {
		return workerJob{}, false
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return workerJob{}, false
	}
	plain, err := credentials.Open(service.masterKey, job.inputCiphertext, job.inputNonce, mediaJobAAD(job.ID, job.keyID, job.inputBytes, "input"))
	if err != nil || !jsonObject(plain) {
		service.fail(ctx, job.ID, "stored_input_invalid", "The stored media input is unavailable")
		return workerJob{}, false
	}
	return workerJob{Job: job, input: plain}, true
}

func (service *Service) dueActive(ctx context.Context) (workerJob, bool) {
	job, err := scanJob(service.database.QueryRowContext(ctx, mediaJobSelect+` WHERE state IN ('starting','processing','canceling') AND next_poll_at<=? ORDER BY next_poll_at,created_at,id LIMIT 1`, time.Now().UnixMilli()))
	return workerJob{Job: job}, err == nil
}

func (service *Service) target(ctx context.Context, job Job) (providers.Target, error) {
	principal, err := service.keys.Principal(ctx, job.keyID)
	if err != nil || principal.OwnerUserID != job.ownerID {
		return providers.Target{}, ErrDenied
	}
	target, err := service.providers.Target(ctx, job.Model)
	if err != nil {
		return providers.Target{}, err
	}
	if target.Preset != job.Provider || !mediaTargetSupported(target) || target.TargetConnectionID != job.connectionID || !principal.Allows(Scope, job.Model, target.TargetConnectionID) || !providers.PresetSupportsModelCapabilities(target.Preset, target.UpstreamID, []string{"media_jobs"}) {
		return providers.Target{}, ErrDenied
	}
	return target, nil
}

func (service *Service) submit(ctx context.Context, job workerJob) {
	target, err := service.target(ctx, job.Job)
	if err != nil {
		service.fail(ctx, job.ID, "permission_denied", "The API key, model, or provider grant is no longer active")
		return
	}
	release, current := service.providers.BeginDispatch(ctx, target)
	if !current {
		service.fail(ctx, job.ID, "configuration_changed", "The provider configuration changed before submission")
		return
	}
	if err := service.usage.MarkDispatching(ctx, job.attemptID); err != nil {
		release()
		_ = service.usage.CancelBeforeDispatch(context.WithoutCancel(ctx), job.attemptID)
		service.failRow(ctx, job.ID, "accounting_unavailable", "The media job could not be admitted for dispatch")
		return
	}
	var prediction providerPrediction
	// Release configuration writes once the create request is sent, not after the provider responds.
	createCtx := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { release() }})
	if job.Provider == "together" {
		prediction, err = createTogetherVideo(createCtx, target, job.input)
	} else if job.Provider == "gemini" {
		prediction, err = createGeminiVideo(createCtx, target, job.input)
	} else if job.Provider == "custom" {
		prediction, err = createCustomMediaJob(createCtx, target, job.input)
	} else {
		prediction, err = createReplicatePrediction(createCtx, target, job.input)
	}
	release()
	if err != nil {
		service.fail(ctx, job.ID, "provider_request_failed", "The provider could not create the media job")
		return
	}
	service.applyPrediction(ctx, job.ID, prediction)
}

func (service *Service) poll(ctx context.Context, job workerJob) {
	if (job.Provider == "together" || job.Provider == "gemini") && job.CancelRequested {
		service.completeLocalCancel(ctx, job.ID)
		return
	}
	target, err := service.target(ctx, job.Job)
	if err != nil {
		service.fail(ctx, job.ID, "permission_denied", "The API key, model, or provider grant is no longer active")
		return
	}
	release, current := service.providers.BeginDispatch(ctx, target)
	if !current {
		service.fail(ctx, job.ID, "configuration_changed", "The provider configuration changed while the job was running")
		return
	}
	var prediction providerPrediction
	if job.Provider == "together" {
		prediction, err = getTogetherVideo(ctx, target, job.ProviderJobID)
	} else if job.Provider == "gemini" {
		prediction, err = getGeminiVideo(ctx, target, job.ProviderJobID)
	} else if job.Provider == "custom" && (job.CancelRequested || job.State == "canceling") {
		prediction, err = cancelCustomMediaJob(ctx, target, job.ProviderJobID)
	} else if job.Provider == "custom" {
		prediction, err = getCustomMediaJob(ctx, target, job.ProviderJobID)
	} else if job.CancelRequested || job.State == "canceling" {
		prediction, err = cancelReplicatePrediction(ctx, target, job.ProviderJobID)
	} else {
		prediction, err = getReplicatePrediction(ctx, target, job.ProviderJobID)
	}
	release()
	if err != nil {
		failures := job.pollFailures + 1
		if !retryableProviderError(err) || failures >= mediaMaxPollFailures {
			service.fail(ctx, job.ID, "provider_poll_failed", "The provider media job could not be polled")
			return
		}
		service.reschedule(ctx, job.ID, failures)
		return
	}
	service.applyPrediction(ctx, job.ID, prediction)
}

func (service *Service) applyPrediction(ctx context.Context, id string, prediction providerPrediction) {
	state, terminal := normalizeProviderState(prediction.Status)
	if state == "" {
		service.fail(ctx, id, "provider_response_invalid", "The provider returned an unsupported media job state")
		return
	}
	now := time.Now().UnixMilli()
	var outputCiphertext, outputNonce []byte
	outputBytes := int64(0)
	errorJSON := `null`
	if terminal && state == "succeeded" && string(prediction.Output) != "null" {
		var keyID string
		if err := service.database.QueryRowContext(ctx, "SELECT key_id FROM media_jobs WHERE id=?", id).Scan(&keyID); err != nil {
			return
		}
		outputBytes = int64(len(prediction.Output))
		if outputBytes > maxOutputBytes {
			state = "failed"
			errorJSON = `{"code":"provider_output_too_large","message":"The provider output exceeds 4 MiB"}`
			outputBytes = 0
		} else {
			var err error
			outputCiphertext, outputNonce, err = credentials.Seal(service.masterKey, prediction.Output, mediaJobAAD(id, keyID, outputBytes, "output"))
			if err != nil {
				state = "failed"
				errorJSON = `{"code":"storage_unavailable","message":"The provider output could not be stored"}`
				outputCiphertext, outputNonce = nil, nil
				outputBytes = 0
			}
		}
	}
	if state == "failed" && errorJSON == `null` {
		errorJSON = `{"code":"provider_failed","message":"The provider media job failed"}`
	}
	completed := any(nil)
	if terminal {
		completed = now
		var attemptID string
		if err := service.database.QueryRowContext(ctx, "SELECT attempt_id FROM media_jobs WHERE id=?", id).Scan(&attemptID); err != nil {
			return
		}
		usageStatus := "unknown"
		if prediction.CostNanos != nil {
			usageStatus = "provider_reported"
		}
		settlementState := state
		if settlementState == "canceled" {
			settlementState = "cancelled"
		}
		if err := service.usage.Settle(ctx, attemptID, usage.SettlementInput{IdempotencyKey: "media-job:" + id + ":terminal", State: settlementState, UsageStatus: usageStatus, CostNanos: prediction.CostNanos, FinalRequest: true}); err != nil {
			return
		}
	}
	_, _ = service.database.ExecContext(ctx, `UPDATE media_jobs SET provider_job_id=?,state=?,output_ciphertext=?,output_nonce=?,output_bytes=?,error_json=?,input_ciphertext=CASE WHEN ? THEN NULL ELSE input_ciphertext END,input_nonce=CASE WHEN ? THEN NULL ELSE input_nonce END,poll_failures=0,next_poll_at=?,completed_at=?,updated_at=?,revision=revision+1 WHERE id=? AND state IN ('submitting','starting','processing','canceling')`, prediction.ID, state, outputCiphertext, outputNonce, outputBytes, errorJSON, terminal, terminal, now+mediaPollInterval.Milliseconds(), completed, now, id)
}

func (service *Service) reschedule(ctx context.Context, id string, failures int64) {
	now := time.Now().UnixMilli()
	delay := mediaPollInterval << min(failures-1, int64(5))
	_, _ = service.database.ExecContext(ctx, `UPDATE media_jobs SET poll_failures=?,next_poll_at=?,updated_at=?,revision=revision+1 WHERE id=? AND state IN ('starting','processing','canceling')`, failures, now+delay.Milliseconds(), now, id)
}

func (service *Service) recoverSubmission(ctx context.Context, id string) {
	var attemptID, state string
	if err := service.database.QueryRowContext(ctx, `SELECT media_jobs.attempt_id,attempts.state FROM media_jobs JOIN attempts ON attempts.id=media_jobs.attempt_id WHERE media_jobs.id=?`, id).Scan(&attemptID, &state); err != nil {
		return
	}
	if state == "reserved" {
		if service.usage.CancelBeforeDispatch(ctx, attemptID) == nil {
			service.failRow(ctx, id, "submission_interrupted", "The gateway restarted before provider dispatch")
		}
		return
	}
	if state != "dispatching" && state != "streaming" {
		return
	}
	if service.usage.Settle(ctx, attemptID, usage.SettlementInput{IdempotencyKey: "media-job:" + id + ":terminal", State: "interrupted_unknown", UsageStatus: "unknown", FinalRequest: true}) != nil {
		return
	}
	now := time.Now().UnixMilli()
	errorJSON, _ := json.Marshal(map[string]string{"code": "submission_outcome_unknown", "message": "The gateway restarted after dispatch and cannot determine the provider job ID"})
	_, _ = service.database.ExecContext(ctx, `UPDATE media_jobs SET state='interrupted_unknown',error_json=?,input_ciphertext=NULL,input_nonce=NULL,completed_at=?,updated_at=?,revision=revision+1 WHERE id=? AND state='submitting'`, string(errorJSON), now, now, id)
}

func (service *Service) completeLocalCancel(ctx context.Context, id string) {
	now := time.Now().UnixMilli()
	var attemptID string
	if err := service.database.QueryRowContext(ctx, "SELECT attempt_id FROM media_jobs WHERE id=?", id).Scan(&attemptID); err != nil {
		return
	}
	if err := service.usage.Settle(ctx, attemptID, usage.SettlementInput{IdempotencyKey: "media-job:" + id + ":terminal", State: "cancelled", UsageStatus: "unknown", FinalRequest: true}); err != nil {
		return
	}
	_, _ = service.database.ExecContext(ctx, `UPDATE media_jobs SET state='canceled',input_ciphertext=NULL,input_nonce=NULL,completed_at=?,updated_at=?,revision=revision+1 WHERE id=? AND state='canceling'`, now, now, id)
}

func (service *Service) fail(ctx context.Context, id, code, message string) {
	var attemptID, state string
	if err := service.database.QueryRowContext(ctx, `SELECT media_jobs.attempt_id,attempts.state FROM media_jobs JOIN attempts ON attempts.id=media_jobs.attempt_id WHERE media_jobs.id=?`, id).Scan(&attemptID, &state); err != nil {
		return
	}
	if state == "reserved" {
		if err := service.usage.CancelBeforeDispatch(ctx, attemptID); err != nil {
			return
		}
	} else if state == "dispatching" || state == "streaming" {
		if err := service.usage.Settle(ctx, attemptID, usage.SettlementInput{IdempotencyKey: "media-job:" + id + ":terminal", State: "failed", UsageStatus: "unknown", FinalRequest: true}); err != nil {
			return
		}
	}
	service.failRow(ctx, id, code, message)
}

func (service *Service) failRow(ctx context.Context, id, code, message string) {
	now := time.Now().UnixMilli()
	errorJSON, _ := json.Marshal(map[string]string{"code": code, "message": message})
	_, _ = service.database.ExecContext(ctx, `UPDATE media_jobs SET state='failed',error_json=?,input_ciphertext=NULL,input_nonce=NULL,completed_at=?,updated_at=?,revision=revision+1 WHERE id=? AND state NOT IN ('succeeded','failed','canceled','interrupted_unknown')`, string(errorJSON), now, now, id)
}

func normalizeProviderState(status string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "starting":
		return "starting", false
	case "processing":
		return "processing", false
	case "queued":
		return "starting", false
	case "in_progress":
		return "processing", false
	case "succeeded", "completed":
		return "succeeded", true
	case "failed", "aborted":
		return "failed", true
	case "canceled", "cancelled":
		return "canceled", true
	default:
		return "", false
	}
}
