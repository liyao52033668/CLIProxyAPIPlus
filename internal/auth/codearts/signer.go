package codearts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// SignRequest signs an HTTP request using SDK-HMAC-SHA256.
// This is HuaweiCloud's signing algorithm (NOT AWS SigV4).
// Key differences from AWS SigV4:
// - Single-step HMAC (no derived key)
// - Path must end with "/"
// - Algorithm name is "SDK-HMAC-SHA256"
func SignRequest(req *http.Request, ak, sk, securityToken string) {
	now := time.Now().UTC()
	timeStr := now.Format("20060102T150405Z")

	req.Header.Set("X-Sdk-Date", timeStr)
	if securityToken != "" {
		req.Header.Set("X-Security-Token", securityToken)
	}

	// Canonical request
	method := req.Method
	path := req.URL.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}

	// Canonical query string
	canonicalQuery := canonicalQueryString(req.URL.Query())

	// Signed headers
	signedHeaderKeys := []string{"host", "x-sdk-date"}
	if securityToken != "" {
		signedHeaderKeys = append(signedHeaderKeys, "x-security-token")
	}
	// Add content-type if present
	if ct := req.Header.Get("Content-Type"); ct != "" {
		signedHeaderKeys = append(signedHeaderKeys, "content-type")
	}
	sort.Strings(signedHeaderKeys)

	// Canonical headers
	var canonicalHeaders strings.Builder
	for _, key := range signedHeaderKeys {
		var val string
		if key == "host" {
			val = req.Host
			if val == "" {
				val = req.URL.Host
			}
		} else {
			val = req.Header.Get(key)
		}
		canonicalHeaders.WriteString(strings.ToLower(key))
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(strings.TrimSpace(val))
		canonicalHeaders.WriteString("\n")
	}

	signedHeadersStr := strings.Join(signedHeaderKeys, ";")

	// Body hash (empty for GET, or use existing hash)
	bodyHash := sha256Hex([]byte(""))

	canonicalReq := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		method, path, canonicalQuery,
		canonicalHeaders.String(), signedHeadersStr, bodyHash)

	// String to sign
	stringToSign := fmt.Sprintf("SDK-HMAC-SHA256\n%s\n%s",
		timeStr, sha256Hex([]byte(canonicalReq)))

	// Signature (single-step HMAC, not derived key)
	signature := hmacSHA256Hex([]byte(sk), []byte(stringToSign))

	// Authorization header
	authHeader := fmt.Sprintf("SDK-HMAC-SHA256 Access=%s, SignedHeaders=%s, Signature=%s",
		ak, signedHeadersStr, signature)
	req.Header.Set("Authorization", authHeader)
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func hmacSHA256Hex(key, data []byte) string {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func canonicalQueryString(query url.Values) string {
	if len(query) == 0 {
		return ""
	}
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := query[k]
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}
