package operations

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const (
	alertWindow          = time.Hour
	alertMinimumRequests = 20
	alertErrorPercent    = 5
)

// AdminStatus adds local, read-only alerts to the lightweight readiness checks.
// It is only called by the authenticated management endpoint, not /ready.
func (service *Service) AdminStatus(ctx context.Context) RuntimeStatus {
	status := service.RuntimeStatus(ctx)
	status.Alerts = []RuntimeAlert{}
	for _, check := range status.Checks {
		if check.State == "ready" {
			continue
		}
		alert := RuntimeAlert{ID: check.ID + "_unavailable", Severity: "critical"}
		switch check.ID {
		case "system_database":
			alert.Title, alert.Description = "System database unavailable", "Check the system database volume and server logs."
		case "data_database":
			alert.Title, alert.Description = "History database unavailable", "Check the history database volume and server logs."
		case "usage_projection":
			alert.Title, alert.Description = "Usage projection unavailable", "Check event outbox capacity and history storage."
		default:
			alert.Title, alert.Description = "Gateway component unavailable", "Check the server and storage before routing traffic."
		}
		status.Alerts = append(status.Alerts, alert)
	}
	if status.Checks[0].State != "ready" {
		return status
	}

	now := time.Now().UTC()
	var succeeded, failed int64
	// ponytail: scan the indexed last hour; add rollups if this status read becomes slow at sustained high volume.
	err := service.store.SystemDB().QueryRowContext(ctx, `SELECT COALESCE(SUM(state = 'succeeded'), 0), COALESCE(SUM(state = 'failed'), 0)
		FROM requests WHERE finished_at >= ? AND finished_at < ?`, now.Add(-alertWindow).UnixMilli(), now.UnixMilli()).Scan(&succeeded, &failed)
	if err != nil {
		status.Alerts = append(status.Alerts, RuntimeAlert{ID: "request_metrics_unavailable", Severity: "warning", Title: "Request metrics unavailable", Description: "Check the system database and request history."})
	} else if completed := succeeded + failed; completed >= alertMinimumRequests && failed*100 >= completed*alertErrorPercent {
		status.Alerts = append(status.Alerts, RuntimeAlert{
			ID: "high_request_error_rate", Severity: "warning", Title: "High request error rate",
			Description: fmt.Sprintf("%d of %d completed requests failed in the last hour (%.1f%%). Review failed requests and provider health.", failed, completed, 100*float64(failed)/float64(completed)),
		})
	}
	var backupState string
	switch err := service.store.SystemDB().QueryRowContext(ctx, `SELECT state FROM backup_jobs WHERE state IN ('succeeded','failed') ORDER BY started_at DESC, id DESC LIMIT 1`).Scan(&backupState); {
	case err == nil && backupState == "failed":
		status.Alerts = append(status.Alerts, RuntimeAlert{ID: "latest_backup_failed", Severity: "warning", Title: "Latest backup failed", Description: "Review the backup job and run a verified backup."})
	case err != nil && err != sql.ErrNoRows:
		status.Alerts = append(status.Alerts, RuntimeAlert{ID: "backup_status_unavailable", Severity: "warning", Title: "Backup status unavailable", Description: "Check the system database and backup jobs."})
	}
	return status
}
