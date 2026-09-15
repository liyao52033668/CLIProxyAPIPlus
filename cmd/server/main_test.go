package main

import "testing"

func TestModelCatalogUpdaterPlan(t *testing.T) {
	tests := []struct {
		name                 string
		localModel           bool
		homeEnabled          bool
		wantModels           bool
		wantCodexClientModel bool
		wantDevin            bool
	}{
		{name: "standard", wantModels: true, wantCodexClientModel: true, wantDevin: true},
		{name: "home", homeEnabled: true, wantCodexClientModel: true},
		{name: "local", localModel: true},
		{name: "local home", localModel: true, homeEnabled: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModels, gotCodexClient, gotDevin := modelCatalogUpdaterPlan(tt.localModel, tt.homeEnabled)
			if gotModels != tt.wantModels || gotCodexClient != tt.wantCodexClientModel || gotDevin != tt.wantDevin {
				t.Fatalf("modelCatalogUpdaterPlan(%v, %v) = (%v, %v, %v), want (%v, %v, %v)", tt.localModel, tt.homeEnabled, gotModels, gotCodexClient, gotDevin, tt.wantModels, tt.wantCodexClientModel, tt.wantDevin)
			}
		})
	}
}
