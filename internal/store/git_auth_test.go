package store

import (
	"testing"

	"github.com/go-git/go-git/v6/plumbing/client"
)

func TestGitAuthReturnsHTTPClientOption(t *testing.T) {
	store := NewGitTokenStore("https://example.com/repo.git", "user", "pass", "")

	opts := store.gitAuth()
	if len(opts) != 1 {
		t.Fatalf("expected 1 client option, got %d", len(opts))
	}
	if opts[0] == nil {
		t.Fatal("expected client option to be non-nil")
	}

	configured := client.New(opts...)
	if configured == nil {
		t.Fatal("expected configured client to be non-nil")
	}
}

func TestGitAuthUsesDefaultUsernameWhenEmpty(t *testing.T) {
	store := NewGitTokenStore("https://example.com/repo.git", "", "token", "")

	opts := store.gitAuth()
	if len(opts) != 1 {
		t.Fatalf("expected 1 client option, got %d", len(opts))
	}
}

func TestGitAuthReturnsNilWithoutCredentials(t *testing.T) {
	store := NewGitTokenStore("https://example.com/repo.git", "", "", "")

	if opts := store.gitAuth(); opts != nil {
		t.Fatalf("expected nil client options, got %d options", len(opts))
	}
}
