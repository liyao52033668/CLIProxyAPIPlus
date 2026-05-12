package repository

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage/keeper/entities"
)

func TestInsertBatchSizeUsesModelColumnCount(t *testing.T) {
	usageIdentityColumnCount := insertBatchColumnCount(entities.UsageIdentity{})
	usageIdentityBatchSize := insertBatchSize(entities.UsageIdentity{})
	if usageIdentityBatchSize >= maxRepositoryInsertBatchSize {
		t.Fatalf("expected wide usage identity model to reduce batch below %d, got %d", maxRepositoryInsertBatchSize, usageIdentityBatchSize)
	}
	if usageIdentityBatchSize != sqliteVariableLimit/usageIdentityColumnCount {
		t.Fatalf("expected usage identity batch size to use %d insert columns, got %d", usageIdentityColumnCount, usageIdentityBatchSize)
	}

	narrowBatchSize := insertBatchSize(narrowInsertBatchModel{})
	if narrowBatchSize != maxRepositoryInsertBatchSize {
		t.Fatalf("expected narrow model to keep max batch size %d, got %d", maxRepositoryInsertBatchSize, narrowBatchSize)
	}
}

type narrowInsertBatchModel struct {
	Name string
}

func TestInsertBatchSizeCachesModelColumnCount(t *testing.T) {
	model := entities.UsageIdentity{}
	first := insertBatchSize(model)
	if insertBatchColumnCountCacheEntries() == 0 {
		t.Fatal("expected insert batch size to cache column count")
	}
	second := insertBatchSize(model)
	if second != first {
		t.Fatalf("expected cached batch size %d, got %d", first, second)
	}
}

func insertBatchColumnCountCacheEntries() int {
	count := 0
	insertBatchColumnCountCache.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}
