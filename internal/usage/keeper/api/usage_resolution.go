package api

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service"
)

// loadUsageResolutionData loads active usage identities needed for source resolution in Request Events and Credentials.
func loadUsageResolutionData(
	c *gin.Context,
	usageIdentityProvider service.UsageIdentityProvider,
) ([]entities.UsageIdentity, error) {
	if usageIdentityProvider == nil {
		return []entities.UsageIdentity{}, nil
	}

	// Request Events source dropdowns and Credentials display resolution only need active identities; use the SQL active-only query.
	return usageIdentityProvider.ListActiveUsageIdentities(c.Request.Context())
}
