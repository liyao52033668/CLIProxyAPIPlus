package codex

import "testing"

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
