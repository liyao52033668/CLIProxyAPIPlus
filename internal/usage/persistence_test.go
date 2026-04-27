package usage
testing "github.com/stretchr/testify/require"

func TestPersistenceLogic(t *testing.T) {
    // Setup mock storage with empty file
    persister := &FileStore{
        dataDir: "/tmp/usage-test"
    }

    // Reset persistence flag
    persistenceStarted/store.Store(false)

    // First start should initialize
    require.NoError(t, StartPersistence(persister, 1*time.Second))
    require.Equal(t, persistenceStarted.Load(), true)

    // Second start should be no-op
    StartPersistence(persister, 1*time.Second)
    require.Equal(t, persistenceStarted.Load(), true)

    // Test reset mechanism
    ResetPersistence()
    require.Equal(t, persistenceStarted.Load(), false)

    // Verify save/load cycle
    snapshot := &StatisticsSnapshot{
        RequestCount: 100,
        ErrorCount: 5,
    }
    err := persister.SaveUsage(snapshot)
    require.NoError(t, err)
    loaded, err := persister.LoadUsage()
    require.NoError(t, err)
    require.Equal(t, loaded.RequestCount, 100)
    require.Equal(t, loaded.ErrorCount, 5)
}

func TestErrorHandling(t *testing.T) {
    // Simulate load failure
    persister := &FileStore{
        dataDir: "/nonexistent"
    }

    // StartPersistence should fail gracefully
    persister.LoadUsage = func() (*StatisticsSnapshot, error) {
        return nil, fmt.Errorf("test error")
    }

    err := StartPersistence(persister, 1*time.Second)
    require.Error(t, err, "test error")
    require.Equal(t, persistenceStarted.Load(), false)
}
