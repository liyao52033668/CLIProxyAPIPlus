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

func TestRunCodexInspectionWithoutBodyReturnsOK(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stub := &stubCodexInspectionService{
		snapshot: codexsvc.DefaultSnapshot(),
	}
	h := &Handler{}
	h.SetCodexInspectionService(stub)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex-inspection/run", nil)

	h.RunCodexInspection(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if stub.lastRunRequest.TriggerType != codexsvc.TriggerTypeManual {
		t.Fatalf("TriggerType = %q, want %q", stub.lastRunRequest.TriggerType, codexsvc.TriggerTypeManual)
	}
	if len(stub.lastRunRequest.FileNames) != 0 {
		t.Fatalf("FileNames = %v, want empty", stub.lastRunRequest.FileNames)
	}
}

func TestRunCodexInspectionRejectsInvalidBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stub := &stubCodexInspectionService{
		snapshot: codexsvc.DefaultSnapshot(),
	}
	h := &Handler{}
	h.SetCodexInspectionService(stub)

	body := bytes.NewBufferString(`{"fileNames":`)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex-inspection/run", body)
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.RunCodexInspection(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["error"] != "invalid request body" {
		t.Fatalf("error = %q, want %q", resp["error"], "invalid request body")
	}
}

func TestRunCodexInspectionForwardsFileNames(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stub := &stubCodexInspectionService{
		snapshot: codexsvc.DefaultSnapshot(),
	}
	h := &Handler{}
	h.SetCodexInspectionService(stub)

	body := bytes.NewBufferString(`{"fileNames":["alpha.json","beta.json"]}`)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex-inspection/run", body)
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.RunCodexInspection(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if stub.lastRunRequest.TriggerType != codexsvc.TriggerTypeManual {
		t.Fatalf("TriggerType = %q, want %q", stub.lastRunRequest.TriggerType, codexsvc.TriggerTypeManual)
	}
	want := []string{"alpha.json", "beta.json"}
	if len(stub.lastRunRequest.FileNames) != len(want) {
		t.Fatalf("FileNames len = %d, want %d (%v)", len(stub.lastRunRequest.FileNames), len(want), stub.lastRunRequest.FileNames)
	}
	for i := range want {
		if stub.lastRunRequest.FileNames[i] != want[i] {
			t.Fatalf("FileNames[%d] = %q, want %q", i, stub.lastRunRequest.FileNames[i], want[i])
		}
	}
}

func TestRunCodexInspectionIgnoresTriggerTypeFromBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stub := &stubCodexInspectionService{
		snapshot: codexsvc.DefaultSnapshot(),
	}
	h := &Handler{}
	h.SetCodexInspectionService(stub)

	body := bytes.NewBufferString(`{"triggerType":"scheduled","fileNames":["alpha.json"]}`)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex-inspection/run", body)
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.RunCodexInspection(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if stub.lastRunRequest.TriggerType != codexsvc.TriggerTypeManual {
		t.Fatalf("TriggerType = %q, want %q", stub.lastRunRequest.TriggerType, codexsvc.TriggerTypeManual)
	}
	if len(stub.lastRunRequest.FileNames) != 1 || stub.lastRunRequest.FileNames[0] != "alpha.json" {
		t.Fatalf("FileNames = %v, want [alpha.json]", stub.lastRunRequest.FileNames)
	}
}

func TestRunCodexInspectionWithUnknownContentLengthForwardsFileNames(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stub := &stubCodexInspectionService{
		snapshot: codexsvc.DefaultSnapshot(),
	}
	h := &Handler{}
	h.SetCodexInspectionService(stub)

	body := bytes.NewBufferString(`{"fileNames":["alpha.json"]}`)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex-inspection/run", body)
	ctx.Request.ContentLength = -1
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.RunCodexInspection(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if stub.lastRunRequest.TriggerType != codexsvc.TriggerTypeManual {
		t.Fatalf("TriggerType = %q, want %q", stub.lastRunRequest.TriggerType, codexsvc.TriggerTypeManual)
	}
	if len(stub.lastRunRequest.FileNames) != 1 || stub.lastRunRequest.FileNames[0] != "alpha.json" {
		t.Fatalf("FileNames = %v, want [alpha.json]", stub.lastRunRequest.FileNames)
	}
}

func TestRunCodexInspectionRejectsInvalidBodyWithUnknownContentLength(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stub := &stubCodexInspectionService{
		snapshot: codexsvc.DefaultSnapshot(),
	}
	h := &Handler{}
	h.SetCodexInspectionService(stub)

	body := bytes.NewBufferString(`{"fileNames":`)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex-inspection/run", body)
	ctx.Request.ContentLength = -1
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.RunCodexInspection(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
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
	snapshot       codexsvc.LatestSnapshot
	lastRunRequest codexsvc.RunRequest
	runErr         error
	updateErr      error
	executeErr     error
	executeResult  codexsvc.ExecuteActionsResult
}

func (s *stubCodexInspectionService) GetSnapshot() (codexsvc.LatestSnapshot, error) {
	return s.snapshot, nil
}

func (s *stubCodexInspectionService) Run(_ context.Context, req codexsvc.RunRequest) (codexsvc.LatestSnapshot, error) {
	s.lastRunRequest = req
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
