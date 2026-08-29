package executor

import "testing"

func TestCursorTokenUsageEstimatesOnly(t *testing.T) {
	u := &cursorTokenUsage{}
	u.setInputEstimate(400)
	u.addOutput(12)
	u.addOutput(8)

	input, output := u.get()
	if input != 100 || output != 20 {
		t.Fatalf("get() = (%d, %d), want (100, 20)", input, output)
	}
}

func TestCursorTokenUsageTurnTotalsOverride(t *testing.T) {
	u := &cursorTokenUsage{}
	u.setInputEstimate(400)
	u.addOutput(20)
	u.setTurnTotals(90, 150, 40, 10, 25)

	input, output := u.get()
	if input != 90 || output != 150 {
		t.Fatalf("get() = (%d, %d), want real turn totals (90, 150)", input, output)
	}
	if d := u.detail(); d.InputTokens != 90 || d.OutputTokens != 150 || d.TotalTokens != 240 {
		t.Fatalf("detail() = %+v, want real totals", d)
	}
}

func TestCursorTokenUsageClientUsageClamp(t *testing.T) {
	u := &cursorTokenUsage{}
	u.setInputEstimate(400) // estimated 100 tokens across the turn chain
	u.addOutput(50)
	// turn_ended spans the whole upstream turn: input exceeds the estimate.
	u.setTurnTotals(900, 150, 0, 0, 0)

	input, output, cacheRead := u.clientUsage()
	if input != 100 || output != 150 || cacheRead != 0 {
		t.Fatalf("clientUsage() = (%d, %d, %d), want clamped (100, 150, 0)", input, output, cacheRead)
	}
}

func TestCursorTokenUsageClientUsageCacheSplit(t *testing.T) {
	u := &cursorTokenUsage{}
	u.setInputEstimate(400)
	u.setTurnTotals(80, 30, 60, 0, 0)

	input, output, cacheRead := u.clientUsage()
	// Real input (80) is below the estimate (100) and is used as-is; cache
	// reads are capped at the input and reported as a breakdown.
	if input != 80 || output != 30 || cacheRead != 60 {
		t.Fatalf("clientUsage() = (%d, %d, %d), want (80, 30, 60)", input, output, cacheRead)
	}
}

func TestCursorTokenUsageZeroTurnCountersKeepEstimates(t *testing.T) {
	u := &cursorTokenUsage{}
	u.setInputEstimate(400)
	u.addOutput(7)
	// Descriptor-generation upstreams send an empty TurnEndedUpdate.
	u.setTurnTotals(0, 0, 0, 0, 0)

	input, output := u.get()
	if input != 100 || output != 7 {
		t.Fatalf("get() = (%d, %d), want estimate fallback (100, 7)", input, output)
	}
}

func TestImageSizePNG(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x07\xd0\x00\x00\x04\xb0\x08\x06\x00\x00\x00")
	w, h := imageSize(png)
	if w != 2000 || h != 1200 {
		t.Fatalf("imageSize(png) = (%d, %d), want (2000, 1200)", w, h)
	}
}

func TestImageSizeGIF(t *testing.T) {
	gif := []byte("GIF89a\x10\x01\x80\x00\x00\x00")
	w, h := imageSize(gif)
	if w != 272 || h != 128 {
		t.Fatalf("imageSize(gif) = (%d, %d), want (272, 128)", w, h)
	}
}

func TestImageSizeJPEG(t *testing.T) {
	jpeg := []byte("\xff\xd8\xff\xc0\x00\x11\x08\x02\xbc\x04\xb0\x03\x01\x11\x00")
	w, h := imageSize(jpeg)
	if w != 1200 || h != 700 {
		t.Fatalf("imageSize(jpeg) = (%d, %d), want (1200, 700)", w, h)
	}
}

func TestImageSizeWebp(t *testing.T) {
	// VP8X canvas size is stored minus one in 24-bit little-endian fields:
	// w-1 = 0x06CE (1743), h-1 = 0x04AF (1200).
	webp := []byte("RIFF\x1e\x00\x00\x00WEBPVP8X\x0e\x00\x00\x00\x10\x00\x00\x00\xce\x06\x00\xaf\x04\x00")
	w, h := imageSize(webp)
	if w != 1743 || h != 1200 {
		t.Fatalf("imageSize(webp) = (%d, %d), want (1743, 1200)", w, h)
	}
}

func TestImageSizeUnknown(t *testing.T) {
	if w, h := imageSize([]byte("not an image")); w != 0 || h != 0 {
		t.Fatalf("imageSize(unknown) = (%d, %d), want (0, 0)", w, h)
	}
}
