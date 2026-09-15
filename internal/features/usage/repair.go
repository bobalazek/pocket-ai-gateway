package usage

import (
	"context"
	"errors"
	"time"
)

// FinalizeFailedRequest retries the immediate cleanup path after post-settlement publication fails.
func (service *Service) FinalizeFailedRequest(ctx context.Context, requestID string) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		last = service.FinalizeRequest(ctx, requestID, "failed")
		if last == nil || errors.Is(last, ErrConflict) || errors.Is(last, context.Canceled) || errors.Is(last, context.DeadlineExceeded) {
			return last
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * 25 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return last
}

const staleRequestRepairAge = 5 * time.Minute

// RepairStaleRequests closes orphaned logical requests after all attempts have been terminal long enough
// that synchronous publication or background completion can no longer reasonably be in flight.
func (service *Service) RepairStaleRequests(ctx context.Context) (int64, error) {
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	current := service.now()
	now := current.UnixMilli()
	cutoff := current.Add(-staleRequestRepairAge).UnixMilli()
	rows, err := tx.QueryContext(ctx, `SELECT requests.id FROM requests
		WHERE requests.state='in_progress'
		AND NOT EXISTS (SELECT 1 FROM attempts WHERE attempts.request_id=requests.id AND attempts.state IN ('reserved','dispatching','streaming'))
		AND NOT EXISTS (SELECT 1 FROM stored_responses WHERE stored_responses.request_id=requests.id AND stored_responses.state IN ('queued','running'))
		AND NOT EXISTS (SELECT 1 FROM message_batch_items WHERE message_batch_items.request_id=requests.id AND message_batch_items.state IN ('queued','claimed','dispatching','settling'))
		AND ((NOT EXISTS (SELECT 1 FROM attempts WHERE attempts.request_id=requests.id) AND requests.started_at<=?)
			OR (EXISTS (SELECT 1 FROM attempts WHERE attempts.request_id=requests.id)
				AND NOT EXISTS (SELECT 1 FROM attempts WHERE attempts.request_id=requests.id AND (attempts.finished_at IS NULL OR attempts.finished_at>?))))
		ORDER BY requests.started_at,requests.id LIMIT 100`, cutoff, cutoff)
	if err != nil {
		return 0, err
	}
	var requestIDs []string
	for rows.Next() {
		var requestID string
		if err := rows.Scan(&requestID); err != nil {
			rows.Close()
			return 0, err
		}
		requestIDs = append(requestIDs, requestID)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	var repaired int64
	for _, requestID := range requestIDs {
		result, err := tx.ExecContext(ctx, `UPDATE requests SET state='failed',finished_at=? WHERE id=? AND state='in_progress'
			AND NOT EXISTS (SELECT 1 FROM attempts WHERE attempts.request_id=requests.id AND attempts.state IN ('reserved','dispatching','streaming'))
			AND NOT EXISTS (SELECT 1 FROM stored_responses WHERE stored_responses.request_id=requests.id AND stored_responses.state IN ('queued','running'))
			AND NOT EXISTS (SELECT 1 FROM message_batch_items WHERE message_batch_items.request_id=requests.id AND message_batch_items.state IN ('queued','claimed','dispatching','settling'))
			AND ((NOT EXISTS (SELECT 1 FROM attempts WHERE attempts.request_id=requests.id) AND requests.started_at<=?)
				OR (EXISTS (SELECT 1 FROM attempts WHERE attempts.request_id=requests.id)
					AND NOT EXISTS (SELECT 1 FROM attempts WHERE attempts.request_id=requests.id AND (attempts.finished_at IS NULL OR attempts.finished_at>?))))`, now, requestID, cutoff, cutoff)
		if err != nil {
			return 0, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if changed == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM concurrency_leases WHERE lease_kind='request' AND request_id=?`, requestID); err != nil {
			return 0, err
		}
		repaired += changed
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return repaired, nil
}
