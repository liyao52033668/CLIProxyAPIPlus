package entities

// All returns the core database entities managed by AutoMigrate.
func All() []any {
	return []any{
		&UsageEvent{},
		&UsageHourlyAggregate{},
		&UsageEventKey{},
		&RedisUsageInbox{},
		&ModelPriceSetting{},
		&UsageIdentity{},
	}
}
