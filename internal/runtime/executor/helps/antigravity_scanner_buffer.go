package helps

import "strings"

// Antigravity SSE single-line scan limits. Image models return generated
// pictures as one huge Base64 JSON line that easily exceeds the shared 4 MiB
// text limit, so image models get a larger dedicated bound.
const (
	// AntigravityTextScannerBuffer is the SSE single-line limit for text models.
	AntigravityTextScannerBuffer = 4 << 20

	// AntigravityImageScannerBuffer is the SSE single-line limit for image
	// models. It stays finite so a runaway upstream cannot grow memory forever.
	AntigravityImageScannerBuffer = 64 << 20
)

// AntigravityScannerBuffer reports the SSE single-line scan limit for the given
// base model. Models whose name marks them as image generators use the larger
// image bound; everything else keeps the shared text bound.
func AntigravityScannerBuffer(baseModel string) int {
	if strings.Contains(strings.ToLower(baseModel), "image") {
		return AntigravityImageScannerBuffer
	}
	return AntigravityTextScannerBuffer
}
