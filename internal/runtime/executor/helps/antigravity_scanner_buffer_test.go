package helps

import "testing"

func TestAntigravityScannerBuffer(t *testing.T) {
	tests := []struct {
		name      string
		baseModel string
		want      int
	}{
		{name: "image_model_uses_image_bound", baseModel: "gemini-3.1-flash-image", want: AntigravityImageScannerBuffer},
		{name: "image_model_mixed_case", baseModel: "Gemini-3.1-Flash-Image", want: AntigravityImageScannerBuffer},
		{name: "image_suffix_model", baseModel: "gemini-3-pro-image", want: AntigravityImageScannerBuffer},
		{name: "text_model_keeps_text_bound", baseModel: "gemini-3.7-flash", want: AntigravityTextScannerBuffer},
		{name: "claude_model_keeps_text_bound", baseModel: "claude-sonnet-4-5", want: AntigravityTextScannerBuffer},
		{name: "empty_model_keeps_text_bound", baseModel: "", want: AntigravityTextScannerBuffer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AntigravityScannerBuffer(tt.baseModel); got != tt.want {
				t.Fatalf("AntigravityScannerBuffer(%q) = %d, want %d", tt.baseModel, got, tt.want)
			}
		})
	}
	if AntigravityImageScannerBuffer <= AntigravityTextScannerBuffer {
		t.Fatalf("image bound %d must exceed text bound %d", AntigravityImageScannerBuffer, AntigravityTextScannerBuffer)
	}
}
