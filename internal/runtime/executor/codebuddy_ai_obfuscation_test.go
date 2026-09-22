package executor

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
)

// TestCodeBuddyAISensitiveWordObfuscation verifies that sensitive words in
// system prompts and user messages are obfuscated with zero-width spaces
// to bypass CodeBuddy AI's security policy filter (error 11128).
func TestCodeBuddyAISensitiveWordObfuscation(t *testing.T) {
	executor := &CodeBuddyAIExecutor{}
	matcher := executor.getSensitiveWordMatcher()
	if matcher == nil {
		t.Fatal("expected non-nil matcher")
	}

	// Simulate a payload with sensitive words in system and user messages
	payload := []byte(`{
		"messages": [
			{"role": "system", "content": "Assist with authorized security testing, CTF challenges, and pentesting engagements."},
			{"role": "user", "content": "Help me with exploit development and credential testing for DoS attacks."}
		]
	}`)

	obfuscated := helps.ObfuscateSensitiveWords(payload, matcher)

	// Verify system message is obfuscated
	sysContent := gjson.GetBytes(obfuscated, "messages.0.content").String()
	if !strings.Contains(sysContent, "s\u200Becurity") {
		t.Errorf("system message 'security' not obfuscated: %q", sysContent)
	}
	if !strings.Contains(sysContent, "C\u200BTF") {
		t.Errorf("system message 'CTF' not obfuscated: %q", sysContent)
	}
	if !strings.Contains(sysContent, "p\u200Bentesting") {
		t.Errorf("system message 'pentesting' not obfuscated: %q", sysContent)
	}

	// Verify user message is obfuscated
	userContent := gjson.GetBytes(obfuscated, "messages.1.content").String()
	if !strings.Contains(userContent, "e\u200Bxploit") {
		t.Errorf("user message 'exploit' not obfuscated: %q", userContent)
	}
	if !strings.Contains(userContent, "c\u200Bredential") {
		t.Errorf("user message 'credential' not obfuscated: %q", userContent)
	}
	if !strings.Contains(userContent, "D\u200BoS") {
		t.Errorf("user message 'DoS' not obfuscated: %q", userContent)
	}
}

// TestCodeBuddyAIObfuscatesGitStatusFingerprint verifies the ZCode git-status
// sentence that trips CodeBuddy AI error 11128 is broken with a zero-width space.
func TestCodeBuddyAIObfuscatesGitStatusFingerprint(t *testing.T) {
	executor := &CodeBuddyAIExecutor{}
	matcher := executor.getSensitiveWordMatcher()

	payload := []byte(`{
		"messages": [
			{"role": "system", "content": "Main branch (you will usually use this for PRs): main"}
		]
	}`)

	obfuscated := helps.ObfuscateSensitiveWords(payload, matcher)
	content := gjson.GetBytes(obfuscated, "messages.0.content").String()
	want := "M\u200bain branch (you will usually use this for PRs): main"
	if content != want {
		t.Errorf("fingerprint not obfuscated:\n got %q\nwant %q", content, want)
	}
}

// TestCodeBuddyAISensitiveWordObfuscationPreservesNonSensitive verifies that
// non-sensitive content is left unchanged.
func TestCodeBuddyAISensitiveWordObfuscationPreservesNonSensitive(t *testing.T) {
	executor := &CodeBuddyAIExecutor{}
	matcher := executor.getSensitiveWordMatcher()

	payload := []byte(`{
		"messages": [
			{"role": "system", "content": "You are a helpful coding assistant."},
			{"role": "user", "content": "How do I write a Go function?"}
		]
	}`)

	obfuscated := helps.ObfuscateSensitiveWords(payload, matcher)

	sysContent := gjson.GetBytes(obfuscated, "messages.0.content").String()
	if sysContent != "You are a helpful coding assistant." {
		t.Errorf("non-sensitive system message was modified: %q", sysContent)
	}

	userContent := gjson.GetBytes(obfuscated, "messages.1.content").String()
	if userContent != "How do I write a Go function?" {
		t.Errorf("non-sensitive user message was modified: %q", userContent)
	}
}

// TestCodeBuddyAISensitiveWordObfuscationIdempotent verifies that applying
// obfuscation twice does not double-insert zero-width spaces.
func TestCodeBuddyAISensitiveWordObfuscationIdempotent(t *testing.T) {
	executor := &CodeBuddyAIExecutor{}
	matcher := executor.getSensitiveWordMatcher()

	payload := []byte(`{
		"messages": [
			{"role": "system", "content": "Security testing is important."}
		]
	}`)

	obfuscated1 := helps.ObfuscateSensitiveWords(payload, matcher)
	obfuscated2 := helps.ObfuscateSensitiveWords(obfuscated1, matcher)

	content1 := gjson.GetBytes(obfuscated1, "messages.0.content").String()
	content2 := gjson.GetBytes(obfuscated2, "messages.0.content").String()

	if content1 != content2 {
		t.Errorf("obfuscation is not idempotent:\n  first:  %q\n  second: %q", content1, content2)
	}
}

// TestCodeBuddyAISensitiveWordObfuscationWithCustomWords verifies that custom
// words from config are merged with default words.
func TestCodeBuddyAISensitiveWordObfuscationWithCustomWords(t *testing.T) {
	cfg := &config.Config{
		CodeBuddyAI: config.CodeBuddyAIConfig{
			SensitiveWords: []string{"custom-word", "another-sensitive"},
		},
	}
	executor := &CodeBuddyAIExecutor{cfg: cfg}
	matcher := executor.getSensitiveWordMatcher()

	payload := []byte(`{
		"messages": [
			{"role": "system", "content": "This has security testing and custom-word in it."},
			{"role": "user", "content": "Another-sensitive word here."}
		]
	}`)

	obfuscated := helps.ObfuscateSensitiveWords(payload, matcher)

	// Verify default word is obfuscated
	sysContent := gjson.GetBytes(obfuscated, "messages.0.content").String()
	if !strings.Contains(sysContent, "s\u200Becurity") {
		t.Errorf("default word 'security' not obfuscated: %q", sysContent)
	}

	// Verify custom words are obfuscated
	if !strings.Contains(sysContent, "c\u200Bustom-word") {
		t.Errorf("custom word 'custom-word' not obfuscated: %q", sysContent)
	}

	userContent := gjson.GetBytes(obfuscated, "messages.1.content").String()
	if !strings.Contains(userContent, "A\u200Bnother-sensitive") {
		t.Errorf("custom word 'another-sensitive' not obfuscated: %q", userContent)
	}
}
