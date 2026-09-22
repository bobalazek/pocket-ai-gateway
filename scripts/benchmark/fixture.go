package main

import (
	"context"
	"time"

	"github.com/bobalazek/pocket-ai-gateway/internal/features/auth"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/keys"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/providers"
	"github.com/bobalazek/pocket-ai-gateway/internal/features/usage"
	"github.com/bobalazek/pocket-ai-gateway/internal/storage"
)

// Only a freshly created temporary directory is passed here. The measured
// process subsequently opens this fully migrated, small instance normally.
func seed(dir, upstream string) (session, secret, sqlite string, err error) {
	ctx := context.Background()
	store, err := storage.Open(ctx, dir)
	if err != nil {
		return
	}
	defer store.Close()
	sqlite = store.SQLiteVersion()
	owner, session, err := auth.New(store.SystemDB()).Claim(ctx, auth.ClaimInput{Email: "benchmark@example.test", DisplayName: "Benchmark", Password: "disposable-benchmark-password"})
	if err != nil {
		return
	}
	master, err := providers.LoadOrCreateMasterKey(dir, false)
	if err != nil {
		return
	}
	service := providers.New(store.SystemDB(), master)
	connection, err := service.CreateConnection(ctx, owner, providers.ConnectionInput{Name: "Local benchmark mock", Adapter: "openai_compatible", BaseURL: upstream + "/v1", Enabled: true, AllowPrivateNetwork: true, TimeoutMS: 30000})
	if err != nil {
		return
	}
	if err = service.PutCredential(ctx, owner, connection.ID, "local-mock-only", ""); err != nil {
		return
	}
	model, err := service.CreateUpstreamModel(ctx, owner, connection.ID, "benchmark", []string{"chat"})
	if err != nil {
		return
	}
	if _, err = service.CreatePublicModel(ctx, owner, "benchmark", "Benchmark", "Local mock", model.ID, model.Capabilities); err != nil {
		return
	}
	_, secret, err = keys.New(store.SystemDB()).Create(ctx, owner.ID, keys.Input{Label: "Benchmark", Scopes: []string{"chat:generate"}, ModelPatterns: []string{"benchmark"}, ConnectionIDs: []string{connection.ID}})
	if err != nil {
		return
	}
	usageService := usage.New(store.SystemDB())
	if _, err = usageService.CreatePrice(ctx, owner, usage.PriceInput{ConnectionID: connection.ID, ModelID: "benchmark", InputUSDPerMillion: "1", OutputUSDPerMillion: "2", Source: "Synthetic benchmark", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		return
	}
	_, err = usageService.CreatePolicy(ctx, owner, usage.PolicyInput{ScopeKind: "instance", Metric: "requests", Algorithm: "quota", Period: "day", LimitUnits: 1000000})
	return
}
