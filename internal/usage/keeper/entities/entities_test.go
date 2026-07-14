package entities

import "testing"

func TestAllIncludesCoreModels(t *testing.T) {
	items := All()
	if len(items) != 6 {
		t.Fatalf("expected 6 models after adding usage archive tables, got %d", len(items))
	}
	if _, ok := items[0].(*UsageEvent); !ok {
		t.Fatalf("expected UsageEvent to be first registered model, got %T", items[0])
	}
	if _, ok := items[1].(*UsageHourlyAggregate); !ok {
		t.Fatalf("expected UsageHourlyAggregate to be registered, got %T", items[1])
	}
	if _, ok := items[2].(*UsageEventKey); !ok {
		t.Fatalf("expected UsageEventKey to be registered, got %T", items[2])
	}
	if _, ok := items[3].(*RedisUsageInbox); !ok {
		t.Fatalf("expected RedisUsageInbox to be registered, got %T", items[3])
	}
}
