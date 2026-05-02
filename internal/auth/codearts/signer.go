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

func SignRequest(req *http.Request, body []byte, ak, sk, securityToken string) {
	now := time.Now().UTC()
	timeStr := now.Format("20060102T150405Z")

	req.Header.Set("X-Sdk-Date", timeStr)
	req.Header.Set("host", req.URL.Host)
	if securityToken != "" {
		req.Header.Set("X-Security-Token", securityToken)
	}

	method := req.Method
	path := req.URL.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}

	canonicalQuery := canonicalQueryString(req.URL.Query())

	lowerMap := make(map[string]string)
	for k, v := range req.Header {
		if len(v) > 0 {
			lowerMap[strings.ToLower(k)] = v[0]
		}
	}

	signedHeaderKeys := make([]string, 0, len(lowerMap))
	for k := range lowerMap {
		signedHeaderKeys = append(signedHeaderKeys, k)
	}
	sort.Strings(signedHeaderKeys)

	var canonicalHeaders strings.Builder
	for _, key := range signedHeaderKeys {
		val := lowerMap[key]
		canonicalHeaders.WriteString(key)
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(strings.TrimSpace(val))
		canonicalHeaders.WriteString("\n")
	}

	signedHeadersStr := strings.Join(signedHeaderKeys, ";")

	bodyHash := sha256Hex(body)

	canonicalReq := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		method, path, canonicalQuery,
		canonicalHeaders.String(), signedHeadersStr, bodyHash)

	stringToSign := fmt.Sprintf("SDK-HMAC-SHA256\n%s\n%s",
		timeStr, sha256Hex([]byte(canonicalReq)))

	signature := hmacSHA256Hex([]byte(sk), []byte(stringToSign))

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
