// Package commandcode contains shared wire types for the Command Code API.
//
// The Command Code API (https://api.commandcode.ai) uses a custom wire protocol
// at POST /alpha/generate. Requests wrap OpenAI-like params inside an envelope,
// and responses are NDJSON event streams (one JSON object per line).
package commandcode

import (
	"runtime"
	"time"
)

const (
	// GenerateEndpoint is the chat generation endpoint path.
	GenerateEndpoint = "/alpha/generate"
)

// ServerConfig describes the local workspace environment required by the Command Code CLI wire protocol.
type ServerConfig struct {
	WorkingDir    string   `json:"workingDir"`
	Date          string   `json:"date"`
	Environment   string   `json:"environment"`
	Structure     []string `json:"structure"`
	IsGitRepo     bool     `json:"isGitRepo"`
	CurrentBranch string   `json:"currentBranch"`
	MainBranch    string   `json:"mainBranch"`
	GitStatus     string   `json:"gitStatus"`
	RecentCommits []string `json:"recentCommits"`
}

// DefaultServerConfig returns a plausible workspace configuration for the envelope.
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		WorkingDir:    "",
		Date:          time.Now().UTC().Format("2006-01-02"),
		Environment:   runtime.GOOS + "-" + runtime.GOARCH,
		Structure:     []string{},
		IsGitRepo:     false,
		CurrentBranch: "",
		MainBranch:    "",
		GitStatus:     "",
		RecentCommits: []string{},
	}
}

// WireRequest is the top-level request envelope for /alpha/generate.
type WireRequest struct {
	Config         any        `json:"config"`
	Memory         any        `json:"memory"`
	Taste          any        `json:"taste"`
	Skills         any        `json:"skills"`
	PermissionMode string     `json:"permissionMode"`
	ThreadID       string     `json:"threadId,omitempty"`
	Mode           string     `json:"mode,omitempty"`
	Params         WireParams `json:"params"`
}

// WireParams holds the model-facing parameters.
type WireParams struct {
	Model           string        `json:"model"`
	Messages        []WireMessage `json:"messages"`
	Tools           []WireTool    `json:"tools,omitempty"`
	System          string        `json:"system,omitempty"`
	MaxTokens       int64         `json:"max_tokens,omitempty"`
	Stream          bool          `json:"stream"`
	Temperature     *float64      `json:"temperature,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
}

// WireMessage is a single message in the wire conversation. Role is "user",
// "assistant" or "tool". "tool" messages carry "tool-result" content parts,
// and the upstream schema requires both toolCallId and toolName on each part.
type WireMessage struct {
	Role    string        `json:"role"`
	Content []WireContent `json:"content"`
}

// WireContent is one content part. Type is one of
// "text", "image", "tool-call", "tool-result" or "reasoning".
// Reasoning parts carry their text in the Text field (upstream field "text").
type WireContent struct {
	Type string `json:"type"`

	// text part; also the text payload of reasoning parts
	Text string `json:"text,omitempty"`

	// image part
	Image    string `json:"image,omitempty"` // data URL: data:<mime>;base64,<data>
	MimeType string `json:"mimeType,omitempty"`

	// tool-call part (assistant side)
	ToolCallID string         `json:"toolCallId,omitempty"`
	ToolName   string         `json:"toolName,omitempty"`
	Input      map[string]any `json:"input,omitempty"`

	// tool-result part (tool role side; toolName is required)
	Output *WireToolOutput `json:"output,omitempty"`
}

// WireToolOutput is the output payload of a tool-result part.
type WireToolOutput struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// WireTool is a tool declaration in the request.
type WireTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// Stream event type names emitted by /alpha/generate NDJSON stream.
const (
	EventTextDelta      = "text-delta"
	EventReasoningDelta = "reasoning-delta"
	EventToolCall       = "tool-call"
	EventFinish         = "finish"
	EventError          = "error"
	EventAbort          = "abort"
)

// FinishUsage mirrors finish.totalUsage from the stream.
type FinishUsage struct {
	InputTokens       int64                    `json:"inputTokens"`
	OutputTokens      int64                    `json:"outputTokens"`
	ReasoningTokens   int64                    `json:"reasoningTokens,omitempty"` // ← NEW: thinking tokens
	InputTokenDetails *FinishUsageInputDetails `json:"inputTokenDetails,omitempty"`
}

// FinishUsageInputDetails carries cache token accounting.
type FinishUsageInputDetails struct {
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
}

// FinishEvent is the terminal event of the NDJSON stream.
type FinishEvent struct {
	Type               string       `json:"type"`
	FinishReason       string       `json:"finishReason,omitempty"`
	RawFinishReason    string       `json:"rawFinishReason,omitempty"`
	TotalUsage         *FinishUsage `json:"totalUsage,omitempty"`
	SystemPromptTokens int64        `json:"systemPromptTokens,omitempty"`
}
