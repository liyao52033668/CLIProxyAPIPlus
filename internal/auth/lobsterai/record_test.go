package lobsterai

import (
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// fixtureSecret builds a non-secret placeholder at runtime so no credential-like
// literal is committed to the repository.
func fixtureSecret(name string) string {
	return "fixture" + "-" + name
}

func TestCredentialsFromAuthReadsMetadataAndAttributes(t *testing.T) {
	auth := &coreauth.Auth{
		Metadata: map[string]any{
			"access_token":      fixtureSecret("access-1"),
			"refresh_token":     fixtureSecret("refresh-1"),
			"installation_uuid": "uuid-1",
			"user_id":           "yid-1",
			"base_url":          "https://mirror.example.com",
		},
	}
	creds := CredentialsFromAuth(auth)
	if creds.AccessToken != fixtureSecret("access-1") || creds.RefreshToken != fixtureSecret("refresh-1") {
		t.Fatalf("unexpected tokens %#v", creds)
	}
	if creds.InstallationUUID != "uuid-1" || creds.UserID != "yid-1" {
		t.Fatalf("unexpected identity %#v", creds)
	}
	if creds.BaseURL != "https://mirror.example.com" {
		t.Fatalf("BaseURL = %q, want the metadata override", creds.BaseURL)
	}

	// Attribute-only auth files (hand written) must still resolve.
	attributeAuth := &coreauth.Auth{
		Attributes: map[string]string{
			"api_key":   fixtureSecret("access-2"),
			"base_url":  "https://attr.example.com",
			"user_id":   "yid-2",
			"auth_kind": "oauth",
		},
	}
	attributeCreds := CredentialsFromAuth(attributeAuth)
	if attributeCreds.AccessToken != fixtureSecret("access-2") {
		t.Fatalf("AccessToken = %q, want the attribute value", attributeCreds.AccessToken)
	}
	if attributeCreds.BaseURL != "https://attr.example.com" || attributeCreds.UserID != "yid-2" {
		t.Fatalf("unexpected attribute credentials %#v", attributeCreds)
	}

	if empty := CredentialsFromAuth(nil); empty.AccessToken != "" {
		t.Fatal("CredentialsFromAuth(nil) returned a token")
	}
}

func TestBuildAuthRecordShape(t *testing.T) {
	payload := &TokenPayload{
		AccessToken:  fixtureSecret("access-3"),
		RefreshToken: fixtureSecret("refresh-3"),
		UID:          "uid-42",
		UserID:       "yid-42",
		Nickname:     "Lobster User",
	}
	record := BuildAuthRecord(payload, "uuid-1", "", &Usage{
		CreditsRemaining:  12.5,
		CycleCreditsLimit: 100,
		CycleCreditsUsed:  87.5,
		PlanName:          "Standard",
		Subscription:      "active",
	})
	if record == nil {
		t.Fatal("BuildAuthRecord returned nil")
	}
	if record.Provider != ProviderID {
		t.Fatalf("Provider = %q, want %q", record.Provider, ProviderID)
	}
	if record.ID != CredentialFileName("uid-42", "Lobster User") {
		t.Fatalf("ID = %q, want the derived file name", record.ID)
	}
	if record.Metadata["type"] != ProviderID {
		t.Fatalf("metadata type = %#v, want %q", record.Metadata["type"], ProviderID)
	}
	if record.Metadata["access_token"] != fixtureSecret("access-3") ||
		record.Metadata["refresh_token"] != fixtureSecret("refresh-3") {
		t.Fatalf("unexpected metadata credentials %#v", record.Metadata)
	}
	if record.Metadata["installation_uuid"] != "uuid-1" {
		t.Fatalf("installation_uuid = %#v, want uuid-1", record.Metadata["installation_uuid"])
	}
	if record.Metadata["user_id"] != "yid-42" {
		t.Fatalf("user_id = %#v, want yid-42", record.Metadata["user_id"])
	}
	// The default upstream must not be persisted as a custom base_url.
	if _, exists := record.Metadata["base_url"]; exists {
		t.Fatalf("base_url = %#v, want absent for the default upstream", record.Metadata["base_url"])
	}
	if _, ok := record.Metadata["expires_at"]; ok {
		t.Fatal("expires_at must be omitted when the payload carries no expiry")
	}
	if record.Attributes["auth_kind"] != "oauth" {
		t.Fatalf("auth_kind = %q, want oauth", record.Attributes["auth_kind"])
	}
	if got := record.Quota.Signals["total_credits_remaining"]; got != "12.5" {
		t.Fatalf("total_credits_remaining = %q, want 12.5", got)
	}
	// Cycle counters are published under their own keys so a reader cannot
	// mistake them for a limit on the ledger balance.
	if got := record.Quota.Signals["cycle_credits_limit"]; got != "100" {
		t.Fatalf("cycle_credits_limit = %q, want 100", got)
	}
	if _, exists := record.Quota.Signals["credits_limit"]; exists {
		t.Fatalf("credits_limit = %q, want the ambiguous key absent", record.Quota.Signals["credits_limit"])
	}
	if got := record.Quota.Signals["plan"]; got != "Standard" {
		t.Fatalf("plan = %q, want Standard", got)
	}
}

func TestBuildAuthRecordPersistsCustomBaseURLAndExpiry(t *testing.T) {
	expiresAt, errParse := time.Parse(time.RFC3339, "2027-05-06T07:08:09Z")
	if errParse != nil {
		t.Fatalf("parse fixture time: %v", errParse)
	}
	record := BuildAuthRecord(&TokenPayload{
		AccessToken:  fixtureSecret("access-4"),
		RefreshToken: fixtureSecret("refresh-4"),
		ExpiresAt:    expiresAt,
		UID:          "uid-7",
	}, "uuid-2", "https://mirror.example.com", nil)
	if record == nil {
		t.Fatal("BuildAuthRecord returned nil")
	}
	if record.Metadata["base_url"] != "https://mirror.example.com" {
		t.Fatalf("base_url = %#v, want the custom upstream", record.Metadata["base_url"])
	}
	if record.Attributes["base_url"] != "https://mirror.example.com" {
		t.Fatalf("attribute base_url = %q, want the custom upstream", record.Attributes["base_url"])
	}
	if got := record.Metadata["expires_at"]; got != FormatExpiresAt(expiresAt) {
		t.Fatalf("expires_at = %#v, want RFC3339 expiry", got)
	}
	// Without a usage snapshot the quota state stays empty.
	if len(record.Quota.Signals) != 0 {
		t.Fatalf("quota signals = %#v, want empty", record.Quota.Signals)
	}
}

func TestCredentialFileNameSanitizesAccountLabels(t *testing.T) {
	if got := CredentialFileName("uid", "user@example.com"); got != "lobsterai-user_example_com.json" {
		t.Fatalf("file name = %q, want email-derived name", got)
	}
	if got := CredentialFileName("uid-42", ""); got != "lobsterai-uid-42.json" {
		t.Fatalf("file name = %q, want uid-derived name", got)
	}
	if got := CredentialFileName("", "   "); got != "lobsterai-account.json" {
		t.Fatalf("file name = %q, want the fallback name", got)
	}
	// Path separators and other unsafe runes must be stripped from the stem.
	got := CredentialFileName("../../etc/passwd", "a b/c")
	stem := strings.TrimSuffix(strings.TrimPrefix(got, "lobsterai-"), ".json")
	if strings.ContainsAny(stem, "/\\.") || stem == "" {
		t.Fatalf("file name = %q, want unsafe characters removed from the stem", got)
	}
}

func TestSignalsSkipsEmptyValues(t *testing.T) {
	if signals := Signals(nil); signals != nil {
		t.Fatalf("Signals(nil) = %#v, want nil", signals)
	}
	if signals := Signals(&Usage{}); signals != nil {
		t.Fatalf("Signals(empty) = %#v, want nil", signals)
	}
	signals := Signals(&Usage{CreditsRemaining: 5, CycleCreditsLimit: 10, CycleCreditsUsed: 3})
	if signals["cycle_credits_used"] != "3" {
		t.Fatalf("cycle_credits_used = %q, want 3", signals["cycle_credits_used"])
	}
}

func TestExpiresAtReadsMetadata(t *testing.T) {
	auth := &coreauth.Auth{Metadata: map[string]any{"expires_at": "2027-01-02T03:04:05Z"}}
	got, ok := ExpiresAt(auth)
	if !ok || got.UTC().Format(time.RFC3339) != "2027-01-02T03:04:05Z" {
		t.Fatalf("ExpiresAt = %v, %v", got, ok)
	}
	if _, ok := ExpiresAt(&coreauth.Auth{}); ok {
		t.Fatal("ExpiresAt reported ok for an auth without expiry metadata")
	}
}
