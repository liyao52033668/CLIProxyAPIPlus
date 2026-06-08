package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	codexsvc "github.com/router-for-me/CLIProxyAPI/v7/internal/codexinspection"
)

func TestGetCodexInspectionSnapshotReturnsOK(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &Handler{}
	h.SetCodexInspectionService(&stubCodexInspectionService{
		snapshot: codexsvc.DefaultSnapshot(),
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/codex-inspection", nil)

	h.GetCodexInspectionSnapshot(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var snapshot codexsvc.LatestSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if snapshot.Run.Status != codexsvc.RunStatusIdle {
		t.Fatalf("snapshot.Run.Status = %q, want %q", snapshot.Run.Status, codexsvc.RunStatusIdle)
	}
}

func TestExecuteCodexInspectionActionsRequiresDeleteConfirmation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &Handler{}
	h.SetCodexInspectionService(&stubCodexInspectionService{
		executeErr: codexsvc.ErrDeleteConfirmationRequired,
	})

	body := bytes.NewBufferString(`{"action":"delete","fileNames":["alpha.json"]}`)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex-inspection/actions", body)
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.ExecuteCodexInspectionActions(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

type stubCodexInspectionService struct {
	snapshot      codexsvc.LatestSnapshot
	runErr        error
	updateErr     error
	executeErr    error
	executeResult codexsvc.ExecuteActionsResult
}

func (s *stubCodexInspectionService) GetSnapshot() (codexsvc.LatestSnapshot, error) {
	return s.snapshot, nil
}

func (s *stubCodexInspectionService) Run(_ context.Context, _ codexsvc.RunRequest) (codexsvc.LatestSnapshot, error) {
	return s.snapshot, s.runErr
}

func (s *stubCodexInspectionService) UpdateSettings(_ context.Context, settings codexsvc.InspectionSettings) (codexsvc.LatestSnapshot, error) {
	s.snapshot.Settings = settings
	return s.snapshot, s.updateErr
}

func (s *stubCodexInspectionService) ExecuteActions(_ context.Context, _ codexsvc.ExecuteActionsRequest) (codexsvc.ExecuteActionsResult, error) {
	return s.executeResult, s.executeErr
}

var _ CodexInspectionService = (*stubCodexInspectionService)(nil)
