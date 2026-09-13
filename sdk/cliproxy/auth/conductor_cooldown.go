package auth

import (
	"sync/atomic"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

var transientErrorCooldownSeconds atomic.Int64

// SetTransientErrorCooldownSeconds configures cooldowns for transient upstream
// errors (408/500/502/503/504/520-526). 0 keeps the legacy default; negative
// values disable transient error cooldowns.
func SetTransientErrorCooldownSeconds(seconds int) {
	transientErrorCooldownSeconds.Store(int64(seconds))
}

// QuotaCooldownDisabledForAuth returns whether cooling is disabled for the auth under global settings.
func QuotaCooldownDisabledForAuth(auth *Auth) bool {
	return quotaCooldownDisabledForAuth(auth)
}

// QuotaCooldownDisabledForAuthWithConfig returns whether cooling is disabled for the auth with the given config.
func QuotaCooldownDisabledForAuthWithConfig(auth *Auth, cfg *internalconfig.Config) bool {
	return quotaCooldownDisabledForAuthWithConfig(auth, cfg)
}

func quotaCooldownDisabledForAuthWithConfig(auth *Auth, cfg *internalconfig.Config) bool {
	// Home owns cooldown state, so downstream instances must not schedule local cooldowns.
	if cfg != nil && cfg.Home.Enabled {
		return true
	}
	if auth != nil {
		if override, ok := auth.DisableCoolingOverride(); ok {
			return override
		}
	}
	return quotaCooldownDisabled.Load()
}

// nextTransientErrorRetryAfter returns the retry deadline for a transient
// upstream failure with cooling enabled.
func nextTransientErrorRetryAfter(now time.Time) time.Time {
	return recoverableFailureRetryAfterWithHint(now, nil, false)
}

// recoverableFailureRetryAfter returns the retry deadline for a recoverable
// failure, honoring the global transient cooldown configuration.
func recoverableFailureRetryAfter(now time.Time, disableCooling bool) time.Time {
	return recoverableFailureRetryAfterWithHint(now, nil, disableCooling)
}

// recoverableFailureRetryAfterWithHint returns the retry deadline for a
// recoverable failure. An upstream RetryAfter hint takes precedence over the
// configured cooldown; a deliberate zero write (disableCooling or a negative
// configuration) clears the deadline.
func recoverableFailureRetryAfterWithHint(now time.Time, retryAfter *time.Duration, disableCooling bool) time.Time {
	if disableCooling {
		return time.Time{}
	}
	seconds := transientErrorCooldownSeconds.Load()
	if seconds < 0 {
		return time.Time{}
	}
	if retryAfter != nil && *retryAfter > 0 {
		return now.Add(*retryAfter)
	}
	if seconds == 0 {
		return now.Add(transientErrorCooldown)
	}
	return now.Add(time.Duration(seconds) * time.Second)
}
