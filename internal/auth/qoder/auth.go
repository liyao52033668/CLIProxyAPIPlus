// Package qoder provides OAuth2 authentication functionality for the Qoder provider.
package qoder

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// UserStatusResponse represents the response from the user status endpoint.
type UserStatusResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// QoderAuth handles Qoder PKCE + URI-scheme authentication.
type QoderAuth struct {
	httpClient *http.Client
}

// NewQoderAuth creates a new Qoder auth service.
func NewQoderAuth(httpClient *http.Client) *QoderAuth {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &QoderAuth{httpClient: httpClient}
}

// GeneratePKCE generates a PKCE verifier/challenge pair and a nonce.
func GeneratePKCE() (nonce, challenge, verifier string, err error) {
	// Generate 32-byte random verifier
	verifierBytes := make([]byte, 32)
	if _, err = rand.Read(verifierBytes); err != nil {
		return "", "", "", fmt.Errorf("qoder: generate verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(verifierBytes)

	// SHA-256 challenge
	challengeHash := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(challengeHash[:])

	// Nonce
	nonceBytes := make([]byte, 16)
	if _, err = rand.Read(nonceBytes); err != nil {
		return "", "", "", fmt.Errorf("qoder: generate nonce: %w", err)
	}
	nonce = fmt.Sprintf("%x", nonceBytes)

	return nonce, challenge, verifier, nil
}

// BuildAuthURL constructs the Qoder login URL for browser-based authentication.
func BuildAuthURL(nonce, challenge, machineID string) string {
	return BuildAuthURLWithRedirect(nonce, challenge, machineID, RedirectURI)
}

// BuildAuthURLWithRedirect constructs the Qoder login URL with a custom redirect URI.
func BuildAuthURLWithRedirect(nonce, challenge, machineID, redirectURI string) string {
	params := url.Values{}
	params.Set("nonce", nonce)
	params.Set("challenge", challenge)
	params.Set("challenge_method", "S256")
	params.Set("redirect_uri", redirectURI)
	params.Set("machine_id", machineID)
	return AuthBase + SelectAccountsPath + "?" + params.Encode()
}

// BuildAuthURLWithRedirectAndState constructs the Qoder login URL with a custom redirect URI and state.
func BuildAuthURLWithRedirectAndState(nonce, challenge, machineID, redirectURI, state string) string {
	params := url.Values{}
	params.Set("nonce", nonce)
	params.Set("challenge", challenge)
	params.Set("challenge_method", "S256")
	params.Set("redirect_uri", redirectURI)
	params.Set("machine_id", machineID)
	if state != "" {
		params.Set("state", state)
	}
	return AuthBase + SelectAccountsPath + "?" + params.Encode()
}

// FetchUserStatus retrieves user info using a device token.
func (o *QoderAuth) FetchUserStatus(deviceToken string) (*UserStatusResponse, error) {
	deviceToken = strings.TrimSpace(deviceToken)
	if deviceToken == "" {
		return nil, fmt.Errorf("qoder user status: missing device token")
	}
	reqURL := OpenAPIBase + UserStatusPath
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("qoder user status: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+deviceToken)
	req.Header.Set("Cosy-Version", IDEVersion)
	req.Header.Set("Cosy-Clienttype", "0")

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("qoder user status: execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("qoder user status: close body error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, errRead := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if errRead != nil {
			return nil, fmt.Errorf("qoder user status: read response: %w", errRead)
		}
		body := strings.TrimSpace(string(bodyBytes))
		if body == "" {
			return nil, fmt.Errorf("qoder user status: request failed: status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("qoder user status: request failed: status %d: %s", resp.StatusCode, body)
	}

	var user UserStatusResponse
	if errDecode := json.NewDecoder(resp.Body).Decode(&user); errDecode != nil {
		return nil, fmt.Errorf("qoder user status: decode response: %w", errDecode)
	}
	return &user, nil
}

// DecodeAuthField decodes the obfuscated auth callback field to extract user info.
func DecodeAuthField(authStr string) ([]byte, error) {
	if strings.TrimSpace(authStr) == "" {
		return nil, fmt.Errorf("qoder: empty auth field")
	}

	// Reverse custom alphabet to standard base64
	var stdBuilder strings.Builder
	for _, c := range authStr {
		if c == CustomPad {
			stdBuilder.WriteByte('=')
		} else {
			idx := strings.Index(CustomAlphabet, string(c))
			if idx >= 0 {
				stdBuilder.WriteByte(StdAlphabet[idx])
			} else {
				return nil, fmt.Errorf("qoder: char out of custom alphabet: %c", c)
			}
		}
	}

	stdB64 := stdBuilder.String()
	n := len(stdB64)
	if n == 0 {
		return nil, fmt.Errorf("qoder: empty after alphabet conversion")
	}

	// Reverse the rearrangement: original = last_third + middle_third + first_third
	// So to recover: first_third + middle_third + last_third
	a := n / 3
	if a == 0 {
		return nil, fmt.Errorf("qoder: string too short to decode")
	}

	// reconstructed = stdB64[n-a:] + stdB64[a:n-a] + stdB64[:a]
	rearranged := stdB64[n-a:] + stdB64[:a] + stdB64[a:n-a]

	return base64.StdEncoding.DecodeString(rearranged)
}

// DecodeAuthFieldToJSON decodes the auth field and parses the result as JSON.
func DecodeAuthFieldToJSON(authStr string) (map[string]any, error) {
	raw, err := DecodeAuthField(authStr)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("qoder: failed to parse auth JSON: %w", err)
	}
	return result, nil
}

// GenerateMachineID creates a deterministic machine identifier.
func GenerateMachineID(hostname, macAddr, system, machine string) string {
	raw := fmt.Sprintf("%s-%s-%s-%s", hostname, macAddr, system, machine)
	digest := sha256.Sum256([]byte(raw))
	encoded := base64.RawURLEncoding.EncodeToString(digest[:])
	var parts []string
	for i := 0; i < len(encoded); i += 22 {
		end := min(i+22, len(encoded))
		parts = append(parts, encoded[i:end])
	}
	return strings.Join(parts, "-")
}

// PollResponse represents the response from the poll endpoint.
type PollResponse struct {
	Token  string `json:"token"`
	Auth   string `json:"auth"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

// PollForToken polls the Qoder device endpoint until authentication completes.
func PollForToken(ctx context.Context, machineID, challenge string) (*PollResponse, error) {
	delay := PollBaseDelay
	consecutiveErrors := 0

	client := &http.Client{Timeout: 10 * time.Second}

	for range PollMaxAttempts {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}

		params := url.Values{}
		params.Set("machine_id", machineID)
		params.Set("challenge", challenge)

		reqURL := CenterBase + "/device/token?" + params.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("qoder poll: create request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			consecutiveErrors++
			if consecutiveErrors >= MaxConsecutiveErrors {
				return nil, fmt.Errorf("qoder poll: too many consecutive errors: %w", err)
			}
			delay = min(time.Duration(float64(delay)*PollBackoffMultiply), PollMaxDelay)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized {
			consecutiveErrors = 0
			delay = min(time.Duration(float64(delay)*PollBackoffMultiply), PollMaxDelay)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var pollResp PollResponse
			if err := json.Unmarshal(body, &pollResp); err != nil {
				return nil, fmt.Errorf("qoder poll: parse response: %w", err)
			}
			if pollResp.Status == "pending" {
				consecutiveErrors = 0
				delay = min(time.Duration(float64(delay)*PollBackoffMultiply), PollMaxDelay)
				continue
			}
			if pollResp.Token != "" {
				return &pollResp, nil
			}
			if pollResp.Error != "" {
				return nil, fmt.Errorf("qoder poll: %s", pollResp.Error)
			}
			return nil, fmt.Errorf("qoder poll: unexpected response")
		}

		return nil, fmt.Errorf("qoder poll: request failed: status %d: %s", resp.StatusCode, string(body))
	}

	return nil, fmt.Errorf("qoder poll: max attempts reached")
}
