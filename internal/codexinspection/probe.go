package codexinspection

import "context"

type DefaultProber struct{}

func (DefaultProber) ProbeCodexAccounts(_ context.Context, files []AuthFileRecord, _ InspectionSettings) ([]InspectionResultItem, error) {
	results := make([]InspectionResultItem, 0, len(files))
	for _, file := range files {
		results = append(results, InspectionResultItem{
			FileName:     file.FileName,
			DisplayName:  file.DisplayName,
			Provider:     file.Provider,
			AuthIndex:    file.AuthIndex,
			AccountID:    file.AccountID,
			Disabled:     file.Disabled,
			Action:       ActionKeep,
			ActionReason: "no issue detected",
			Executable:   false,
		})
	}
	return results, nil
}

func buildSummary(results []InspectionResultItem, totalFiles int) InspectionSummary {
	summary := InspectionSummary{
		TotalFiles:   totalFiles,
		SampledCount: len(results),
	}

	for _, result := range results {
		switch result.Action {
		case ActionKeep:
			summary.KeepCount++
		case ActionDelete:
			summary.DeleteCount++
		case ActionDisable:
			summary.DisableCount++
		case ActionEnable:
			summary.EnableCount++
		case ActionReauth:
			summary.ReauthCount++
		}

		if result.Disabled {
			summary.DisabledCount++
		} else {
			summary.EnabledCount++
		}
	}

	return summary
}
