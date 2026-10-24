package synthesizer

// ExportExcludedModelsFromMetadata exposes per-account excluded-model extraction
// to management write paths so PATCH validation and file loading share one parser.
func ExportExcludedModelsFromMetadata(metadata map[string]any) ([]string, error) {
	return extractExcludedModelsFromMetadata(metadata)
}
