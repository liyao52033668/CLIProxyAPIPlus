package api

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage/keeper/entities"
)

type usageIdentityResolver struct {
	authFilesByIdentity map[string]entities.UsageIdentity
	providersByIdentity map[string]entities.UsageIdentity
}

func newUsageIdentityResolver(identities []entities.UsageIdentity) usageIdentityResolver {
	authFilesByIdentity := make(map[string]entities.UsageIdentity, len(identities))
	providersByIdentity := make(map[string]entities.UsageIdentity, len(identities))
	for _, identity := range identities {
		if identity.IsDeleted {
			continue
		}
		key := strings.TrimSpace(identity.Identity)
		if key == "" {
			continue
		}
		switch identity.AuthType {
		case entities.UsageIdentityAuthTypeAuthFile:
			authFilesByIdentity[key] = identity
		case entities.UsageIdentityAuthTypeAIProvider:
			providersByIdentity[key] = identity
		}
	}

	return usageIdentityResolver{
		authFilesByIdentity: authFilesByIdentity,
		providersByIdentity: providersByIdentity,
	}
}

type resolvedUsageIdentity struct {
	DisplayName string
	Type        string
}

func usageIdentityDisplayName(item entities.UsageIdentity) string {
	name := strings.TrimSpace(item.Name)
	provider := strings.TrimSpace(item.Provider)
	if item.AuthType != entities.UsageIdentityAuthTypeAIProvider {
		if name != "" {
			return name
		}
		return provider
	}

	prefix := strings.TrimSpace(item.Prefix)
	switch {
	case name != "" && prefix != "":
		return name + "(" + prefix + ")"
	case name != "":
		return name
	case prefix != "":
		return prefix
	default:
		return provider
	}
}

func resolvedUsageIdentityFromEntity(item entities.UsageIdentity) resolvedUsageIdentity {
	return resolvedUsageIdentity{
		DisplayName: usageIdentityDisplayName(item),
		Type:        strings.TrimSpace(item.Type),
	}
}

func (r usageIdentityResolver) resolveByAuthIndex(authIndex string) (resolvedUsageIdentity, bool) {
	key := strings.TrimSpace(authIndex)
	if key == "" {
		return resolvedUsageIdentity{}, false
	}
	if identity, ok := r.providersByIdentity[key]; ok {
		return resolvedUsageIdentityFromEntity(identity), true
	}
	if identity, ok := r.authFilesByIdentity[key]; ok {
		return resolvedUsageIdentityFromEntity(identity), true
	}
	return resolvedUsageIdentity{}, false
}
