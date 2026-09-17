package providers

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
)

func (service *Service) GetAdapterScript(ctx context.Context, actor auth.User, connectionID string) (AdapterScript, error) {
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return AdapterScript{}, err
	}
	return service.getAdapterScript(ctx, connectionID)
}

func (service *Service) getAdapterScript(ctx context.Context, connectionID string) (AdapterScript, error) {
	var item AdapterScript
	var updated int64
	err := service.database.QueryRowContext(ctx, `SELECT provider_connections.id,COALESCE(provider_adapter_scripts.request_script,''),COALESCE(provider_adapter_scripts.response_script,''),provider_connections.revision,COALESCE(provider_adapter_scripts.updated_at,provider_connections.updated_at)
		FROM provider_connections LEFT JOIN provider_adapter_scripts ON provider_adapter_scripts.connection_id=provider_connections.id
		WHERE provider_connections.id=?`, connectionID).Scan(&item.ConnectionID, &item.RequestScript, &item.ResponseScript, &item.Revision, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return AdapterScript{}, ErrNotFound
	}
	item.UpdatedAt = formatTime(updated)
	return item, err
}

func (service *Service) PutAdapterScript(ctx context.Context, actor auth.User, connectionID string, revision int64, input AdapterScriptInput) (AdapterScript, error) {
	service.dispatch.Lock()
	defer service.dispatch.Unlock()
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return AdapterScript{}, err
	}
	connection, err := service.getConnection(ctx, connectionID)
	if err != nil {
		return AdapterScript{}, err
	}
	if connection.Preset != "custom" || connection.Adapter != "openai_compatible" {
		return AdapterScript{}, errors.New("adapter scripts must use a custom OpenAI compatible connection")
	}
	input, err = validateAdapterScriptInput(input)
	if err != nil {
		return AdapterScript{}, err
	}
	now := time.Now().UnixMilli()
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return AdapterScript{}, err
	}
	defer tx.Rollback()
	if err = requireManager(ctx, tx, &actor); err != nil {
		return AdapterScript{}, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE provider_connections SET revision=revision+1,updated_at=? WHERE id=? AND revision=?", now, connectionID, revision)
	if err != nil {
		return AdapterScript{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return AdapterScript{}, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO provider_adapter_scripts(connection_id,request_script,response_script,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(connection_id) DO UPDATE SET request_script=excluded.request_script,response_script=excluded.response_script,updated_at=excluded.updated_at`, connectionID, input.RequestScript, input.ResponseScript, now); err != nil {
		return AdapterScript{}, err
	}
	if err = audit(ctx, tx, actor.ID, "provider.adapter_script.update", "provider_connection", connectionID); err != nil {
		return AdapterScript{}, err
	}
	if err = tx.Commit(); err != nil {
		return AdapterScript{}, err
	}
	return service.getAdapterScript(ctx, connectionID)
}

func (service *Service) DeleteAdapterScript(ctx context.Context, actor auth.User, connectionID string, revision int64) error {
	service.dispatch.Lock()
	defer service.dispatch.Unlock()
	if err := requireManager(ctx, service.database, &actor); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	tx, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = requireManager(ctx, tx, &actor); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE provider_connections SET revision=revision+1,updated_at=? WHERE id=? AND revision=?", now, connectionID, revision)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM provider_adapter_scripts WHERE connection_id=?", connectionID); err != nil {
		return err
	}
	if err = audit(ctx, tx, actor.ID, "provider.adapter_script.delete", "provider_connection", connectionID); err != nil {
		return err
	}
	return tx.Commit()
}
