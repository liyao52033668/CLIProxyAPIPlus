package executor

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// kiroHTTPClientPool provides a shared HTTP client with connection pooling for Kiro API.
// This reduces connection overhead and improves performance for concurrent requests.
// Based on kiro2Api's connection pooling pattern.
var (
	kiroHTTPClientPool     *http.Client
	kiroHTTPClientPoolOnce sync.Once
)

// getKiroPooledHTTPClient returns a shared HTTP client with optimized connection pooling.
// The client is lazily initialized on first use and reused across requests.
// This is especially beneficial for:
// - Reducing TCP handshake overhead
// - Enabling HTTP/2 multiplexing
// - Better handling of keep-alive connections
func getKiroPooledHTTPClient() *http.Client {
	kiroHTTPClientPoolOnce.Do(func() {
		transport := &http.Transport{
			// Connection pool settings
			MaxIdleConns:        100,              // Max idle connections across all hosts
			MaxIdleConnsPerHost: 20,               // Max idle connections per host
			MaxConnsPerHost:     50,               // Max total connections per host
			IdleConnTimeout:     90 * time.Second, // How long idle connections stay in pool

			// Timeouts for connection establishment
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second, // TCP connection timeout
				KeepAlive: 30 * time.Second, // TCP keep-alive interval
			}).DialContext,

			// TLS handshake timeout
			TLSHandshakeTimeout: 10 * time.Second,

			// Response header timeout
			ResponseHeaderTimeout: 30 * time.Second,

			// Expect 100-continue timeout
			ExpectContinueTimeout: 1 * time.Second,

			// Enable HTTP/2 when available
			ForceAttemptHTTP2: true,
		}

		kiroHTTPClientPool = &http.Client{
			Transport: transport,
			// No global timeout - let individual requests set their own timeouts via context
		}

		log.Debugf("kiro: initialized pooled HTTP client (MaxIdleConns=%d, MaxIdleConnsPerHost=%d, MaxConnsPerHost=%d)",
			transport.MaxIdleConns, transport.MaxIdleConnsPerHost, transport.MaxConnsPerHost)
	})

	return kiroHTTPClientPool
}

// newKiroHTTPClientWithPooling creates an HTTP client that uses connection pooling when appropriate.
// It respects proxy configuration from auth or config, falling back to the pooled client.
// This provides the best of both worlds: custom proxy support + connection reuse.
func newKiroHTTPClientWithPooling(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	// Check if a proxy is configured - if so, we need a custom client
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	// If proxy is configured, use the existing proxy-aware client (doesn't pool)
	if proxyURL != "" {
		log.Debugf("kiro: using proxy-aware HTTP client (proxy=%s)", proxyURL)
		return newProxyAwareHTTPClient(ctx, cfg, auth, timeout)
	}

	// No proxy - use pooled client for better performance
	pooledClient := getKiroPooledHTTPClient()

	// If timeout is specified, we need to wrap the pooled transport with timeout
	if timeout > 0 {
		return &http.Client{
			Transport: pooledClient.Transport,
			Timeout:   timeout,
		}
	}

	return pooledClient
}

// kiroEndpointConfig bundles endpoint URL with its compatible Origin and AmzTarget values.
// This solves the "triple mismatch" problem where different endpoints require matching
// Origin and X-Amz-Target header values.
//
// Based on reference implementations:
// - amq2api-main: Uses Amazon Q endpoint with CLI origin and AmazonQDeveloperStreamingService target
// - AIClient-2-API: Uses CodeWhisperer endpoint with AI_EDITOR origin and AmazonCodeWhispererStreamingService target
type kiroEndpointConfig struct {
	URL       string // Endpoint URL
	Origin    string // Request Origin: "CLI" for Amazon Q quota, "AI_EDITOR" for Kiro IDE quota
	AmzTarget string // X-Amz-Target header value
	Name      string // Endpoint name for logging
}

// kiroDefaultRegion is the default AWS region for Kiro API endpoints.
// Used when no region is specified in auth metadata.
const kiroDefaultRegion = "us-east-1"

// extractRegionFromProfileARN extracts the AWS region from a ProfileARN.
// ARN format: arn:aws:codewhisperer:REGION:ACCOUNT:profile/PROFILE_ID
// Returns empty string if region cannot be extracted.
func extractRegionFromProfileARN(profileArn string) string {
	if profileArn == "" {
		return ""
	}
	parts := strings.Split(profileArn, ":")
	if len(parts) >= 4 && parts[3] != "" {
		return parts[3]
	}
	return ""
}

// buildKiroEndpointConfigs creates endpoint configurations for the specified region.
// This enables dynamic region support for Enterprise/IdC users in non-us-east-1 regions.
//
// Uses Q endpoint (q.{region}.amazonaws.com) as primary for ALL auth types:
// - Works universally across all AWS regions (CodeWhisperer endpoint only exists in us-east-1)
// - Uses /generateAssistantResponse path with AI_EDITOR origin
// - Does NOT require X-Amz-Target header
//
// The AmzTarget field is kept for backward compatibility but should be empty
// to indicate that the header should NOT be set.
func buildKiroEndpointConfigs(region string) []kiroEndpointConfig {
	if region == "" {
		region = kiroDefaultRegion
	}
	return []kiroEndpointConfig{
		{
			// Primary: Q endpoint - works for all regions and auth types
			URL:       fmt.Sprintf("https://q.%s.amazonaws.com/generateAssistantResponse", region),
			Origin:    "AI_EDITOR",
			AmzTarget: "", // Empty = don't set X-Amz-Target header
			Name:      "AmazonQ",
		},
		{
			// Fallback: CodeWhisperer endpoint (legacy, only works in us-east-1)
			URL:       fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/generateAssistantResponse", region),
			Origin:    "AI_EDITOR",
			AmzTarget: "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
			Name:      "CodeWhisperer",
		},
	}
}

// resolveKiroAPIRegion determines the AWS region for Kiro API calls.
// Region priority:
// 1. auth.Metadata["api_region"] - explicit API region override
// 2. ProfileARN region - extracted from arn:aws:service:REGION:account:resource
// 3. kiroDefaultRegion (us-east-1) - fallback
// Note: OIDC "region" is NOT used - it's for token refresh, not API calls
func resolveKiroAPIRegion(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return kiroDefaultRegion
	}
	// Priority 1: Explicit api_region override
	if r, ok := auth.Metadata["api_region"].(string); ok && r != "" {
		log.Debugf("kiro: using region %s (source: api_region)", r)
		return r
	}
	// Priority 2: Extract from ProfileARN
	if profileArn, ok := auth.Metadata["profile_arn"].(string); ok && profileArn != "" {
		if arnRegion := extractRegionFromProfileARN(profileArn); arnRegion != "" {
			log.Debugf("kiro: using region %s (source: profile_arn)", arnRegion)
			return arnRegion
		}
	}
	// Note: OIDC "region" field is NOT used for API endpoint
	// Kiro API only exists in us-east-1, while OIDC region can vary (e.g., ap-northeast-2)
	// Using OIDC region for API calls causes DNS failures
	log.Debugf("kiro: using region %s (source: default)", kiroDefaultRegion)
	return kiroDefaultRegion
}

// kiroEndpointConfigs is kept for backward compatibility with default us-east-1 region.
// Prefer using buildKiroEndpointConfigs(region) for dynamic region support.
var kiroEndpointConfigs = buildKiroEndpointConfigs(kiroDefaultRegion)

// getKiroEndpointConfigs returns the list of Kiro API endpoint configurations to try in order.
// Supports dynamic region based on auth metadata "api_region", "profile_arn", or "region" field.
// Supports reordering based on "preferred_endpoint" in auth metadata/attributes.
//
// Region priority:
// 1. auth.Metadata["api_region"] - explicit API region override
// 2. ProfileARN region - extracted from arn:aws:service:REGION:account:resource
// 3. kiroDefaultRegion (us-east-1) - fallback
// Note: OIDC "region" is NOT used - it's for token refresh, not API calls
func getKiroEndpointConfigs(auth *cliproxyauth.Auth) []kiroEndpointConfig {
	if auth == nil {
		return kiroEndpointConfigs
	}

	region := resolveKiroAPIRegion(auth)
	log.Debugf("kiro: using region %s", region)

	configs := buildKiroEndpointConfigs(region)

	preference := getAuthValue(auth, "preferred_endpoint")
	if preference == "" {
		return configs
	}

	targetName, ok := endpointAliases[preference]
	if !ok {
		return configs
	}

	var preferred, others []kiroEndpointConfig
	for _, cfg := range configs {
		if strings.ToLower(cfg.Name) == targetName {
			preferred = append(preferred, cfg)
		} else {
			others = append(others, cfg)
		}
	}

	if len(preferred) == 0 {
		return configs
	}
	return append(preferred, others...)
}

// KiroExecutor handles requests to AWS CodeWhisperer (Kiro) API.
