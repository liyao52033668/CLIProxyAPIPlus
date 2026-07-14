package main

import "testing"

func TestModelCatalogUpdaterPlan(t *testing.T) {
	tests := []struct {
		name                 string
		localModel           bool
		homeEnabled          bool
		wantModels           bool
		wantCodexClientModel bool
	}{
		{name: "standard", wantModels: true, wantCodexClientModel: true},
		{name: "home", homeEnabled: true, wantCodexClientModel: true},
		{name: "local", localModel: true},
		{name: "local home", localModel: true, homeEnabled: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModels, gotCodexClient := modelCatalogUpdaterPlan(tt.localModel, tt.homeEnabled)
			if gotModels != tt.wantModels || gotCodexClient != tt.wantCodexClientModel {
				t.Fatalf("modelCatalogUpdaterPlan(%v, %v) = (%v, %v), want (%v, %v)", tt.localModel, tt.homeEnabled, gotModels, gotCodexClient, tt.wantModels, tt.wantCodexClientModel)
			}
		})
	}
}
