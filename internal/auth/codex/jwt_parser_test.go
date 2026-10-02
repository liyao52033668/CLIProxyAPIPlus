package codex

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func makeTestJWT(payload map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payloadBytes, _ := json.Marshal(payload)
	claims := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + claims + "."
}

func TestGetPlanType(t *testing.T) {
	var nilClaims *JWTClaims
	if got := nilClaims.GetPlanType(); got != DefaultPlanType {
		t.Fatalf("nilClaims.GetPlanType() = %q, want %q", got, DefaultPlanType)
	}

	emptyClaims := &JWTClaims{}
	if got := emptyClaims.GetPlanType(); got != DefaultPlanType {
		t.Fatalf("emptyClaims.GetPlanType() = %q, want %q", got, DefaultPlanType)
	}

	whitespaceClaims := &JWTClaims{
		CodexAuthInfo: CodexAuthInfo{
			ChatgptPlanType: "   ",
		},
	}
	if got := whitespaceClaims.GetPlanType(); got != DefaultPlanType {
		t.Fatalf("whitespaceClaims.GetPlanType() = %q, want %q", got, DefaultPlanType)
	}

	proClaims := &JWTClaims{
		CodexAuthInfo: CodexAuthInfo{
			ChatgptPlanType: "pro",
		},
	}
	if got := proClaims.GetPlanType(); got != "pro" {
		t.Fatalf("proClaims.GetPlanType() = %q, want %q", got, "pro")
	}
}

func TestParseJWTToken_MissingPlanTypeDefaultsToFree(t *testing.T) {
	jwtWithoutPlan := makeTestJWT(map[string]any{
		"email": "user@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc-12345",
		},
	})

	claims, errParse := ParseJWTToken(jwtWithoutPlan)
	if errParse != nil {
		t.Fatalf("ParseJWTToken failed: %v", errParse)
	}
	if got := claims.GetPlanType(); got != "free" {
		t.Fatalf("claims.GetPlanType() = %q, want %q", got, "free")
	}

	jwtWithPlan := makeTestJWT(map[string]any{
		"email": "user@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc-12345",
			"chatgpt_plan_type":  "team",
		},
	})

	claimsTeam, errParseTeam := ParseJWTToken(jwtWithPlan)
	if errParseTeam != nil {
		t.Fatalf("ParseJWTToken failed: %v", errParseTeam)
	}
	if got := claimsTeam.GetPlanType(); got != "team" {
		t.Fatalf("claimsTeam.GetPlanType() = %q, want %q", got, "team")
	}
}

func TestParseJWTToken_AcceptsStringAudience(t *testing.T) {
	token := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIiwiY3BhX3N5bnRoZXRpYyI6dHJ1ZX0.eyJpc3MiOiJodHRwczovL2F1dGgub3BlbmFpLmNvbS8iLCJzdWIiOiJ1c2VyLUNLcWJEOTVoSDV3VzVad05HZVlHQ25HUSIsImF1ZCI6ImNoYXRncHQyYXBpLWV4cG9ydCIsImlhdCI6MTc4MzA2MzY5MCwiZXhwIjoxNzkwODM3OTE2LCJlbWFpbCI6IlRhYml0aGFBbm5hYmV0aDM5NTJAb3V0bG9vay5jb20iLCJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9hY2NvdW50X2lkIjoiMjU1ZGU0YTYtOTZhNC00MzBhLWI2NjAtMzU4OTU0NDI0ZTc5IiwiY2hhdGdwdF91c2VyX2lkIjoidXNlci1DS3FiRDk1aEg1d1c1WndOR2VZR0NuR1EiLCJjaGF0Z3B0X3BsYW5fdHlwZSI6ImsxMiJ9fQ.synthetic"

	claims, err := ParseJWTToken(token)
	if err != nil {
		t.Fatalf("ParseJWTToken() error = %v", err)
	}
	if claims == nil {
		t.Fatal("ParseJWTToken() returned nil claims")
	}
	if len(claims.Aud) != 1 || claims.Aud[0] != "chatgpt2api-export" {
		t.Fatalf("Aud = %#v, want []string{\"chatgpt2api-export\"}", claims.Aud)
	}
	if claims.CodexAuthInfo.ChatgptAccountID != "255de4a6-96a4-430a-b660-358954424e79" {
		t.Fatalf("ChatgptAccountID = %q", claims.CodexAuthInfo.ChatgptAccountID)
	}
	if claims.CodexAuthInfo.ChatgptPlanType != "k12" {
		t.Fatalf("ChatgptPlanType = %q", claims.CodexAuthInfo.ChatgptPlanType)
	}
}
