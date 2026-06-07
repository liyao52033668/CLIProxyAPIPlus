package cliproxy

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

var fetchQoderCatalog = func(ctx context.Context, auth *coreauth.Auth, cfg *config.Config) executor.QoderModelCatalog {
	return executor.FetchQoderModelCatalog(ctx, auth, cfg)
}

var fetchQoderModels = func(ctx context.Context, auth *coreauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	return fetchQoderCatalog(ctx, auth, cfg).Models
}
