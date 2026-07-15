package usage

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	usageconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository"
)

type managerRecordingPlugin struct {
	mu      sync.Mutex
	records []Record
	handled chan struct{}
}

func (p *managerRecordingPlugin) HandleUsage(_ context.Context, record Record) {
	p.mu.Lock()
	p.records = append(p.records, record)
	p.mu.Unlock()
	p.handled <- struct{}{}
}

func (p *managerRecordingPlugin) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.records)
}

func TestManagerWithDispatchPausedFlushesAndOrdersUsage(t *testing.T) {
	db, err := repository.OpenDatabase(usageconfig.Config{SQLitePath: filepath.Join(t.TempDir(), "usage.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql database: %v", err)
	}
	t.Cleanup(func() {
		if errClose := sqlDB.Close(); errClose != nil {
			t.Fatalf("close database: %v", errClose)
		}
	})

	manager := NewManagerWithDB(db)
	recorder := &managerRecordingPlugin{handled: make(chan struct{}, 2)}
	manager.Register(recorder)
	t.Cleanup(manager.Stop)

	now := time.Now().UTC()
	manager.Publish(context.Background(), Record{APIKey: "provider-a", Model: "model-a", RequestedAt: now})
	err = manager.WithDispatchPaused(context.Background(), func() error {
		if recorder.count() != 1 {
			return fmt.Errorf("records before callback = %d, want 1", recorder.count())
		}
		var count int64
		if errCount := db.Model(&entities.UsageEvent{}).Count(&count).Error; errCount != nil {
			return errCount
		}
		if count != 1 {
			return fmt.Errorf("persisted records before callback = %d, want 1", count)
		}
		manager.Publish(context.Background(), Record{APIKey: "provider-b", Model: "model-b", RequestedAt: now.Add(time.Second)})
		if recorder.count() != 1 {
			return fmt.Errorf("callback observed later queued record")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithDispatchPaused returned error: %v", err)
	}

	select {
	case <-recorder.handled:
	case <-time.After(time.Second):
		t.Fatal("first usage record was not dispatched")
	}
	select {
	case <-recorder.handled:
	case <-time.After(time.Second):
		t.Fatal("queued usage record was not dispatched after callback")
	}
	if err = manager.WithDispatchPaused(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("flush queued usage: %v", err)
	}
	var count int64
	if err = db.Model(&entities.UsageEvent{}).Count(&count).Error; err != nil {
		t.Fatalf("count usage events: %v", err)
	}
	if count != 2 {
		t.Fatalf("persisted records = %d, want 2", count)
	}
}

func TestManagerWithDispatchPausedWaitsForRunningCallbackAfterCancel(t *testing.T) {
	manager := NewManager(0)
	t.Cleanup(manager.Stop)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- manager.WithDispatchPaused(ctx, func() error {
			close(started)
			<-release
			return nil
		})
	}()

	<-started
	cancel()
	select {
	case err := <-result:
		t.Fatalf("WithDispatchPaused returned before callback completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("WithDispatchPaused returned error: %v", err)
	}
}

func TestManagerWithDispatchPausedKeepsBufferWhenFlushFails(t *testing.T) {
	db, err := repository.OpenDatabase(usageconfig.Config{SQLitePath: filepath.Join(t.TempDir(), "usage.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql database: %v", err)
	}
	if err = sqlDB.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	manager := NewManagerWithDB(db)
	manager.Publish(context.Background(), Record{APIKey: "provider-a", Model: "model-a", RequestedAt: time.Now().UTC()})
	callbackCalled := false
	err = manager.WithDispatchPaused(context.Background(), func() error {
		callbackCalled = true
		return nil
	})
	if err == nil {
		t.Fatal("WithDispatchPaused error = nil, want flush error")
	}
	if callbackCalled {
		t.Fatal("callback ran after database flush failed")
	}
	database := manager.plugins[0].(*databasePlugin)
	database.mu.Lock()
	buffered := len(database.buffer)
	database.mu.Unlock()
	if buffered != 1 {
		t.Fatalf("buffered records = %d, want 1", buffered)
	}
	manager.Stop()
}
