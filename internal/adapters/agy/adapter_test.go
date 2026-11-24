package agy

import (
	"testing"

	"bro-bot/internal/effort"
)

func TestResolveAgyModelTarget_AliasesAndEffort(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		effort     effort.Level
		wantModel  string
		wantEffort string
	}{
		{"empty model", "", effort.Auto, "", ""},
		{"orchestrator alias", "orchestrator", effort.Auto, "gemini-pro-orchestrator-neon", ""},
		{"orchestrator ignores effort", "orchestrator-argon", effort.High, "gemini-pro-orchestrator-g1-pro", ""},
		{"flash with explicit low effort", "gemini-3.8-flash-high", effort.Low, "gemini-3.8-flash-low", ""},
		{"unknown model passes effort", "custom-experimental-model", effort.Medium, "custom-experimental-model", "medium"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotModel, gotEffort := resolveAgyModelTarget(tc.model, tc.effort)
			if gotModel != tc.wantModel || gotEffort != tc.wantEffort {
				t.Errorf("resolveAgyModelTarget(%q, %q) = (%q, %q), want (%q, %q)",
					tc.model, tc.effort, gotModel, gotEffort, tc.wantModel, tc.wantEffort)
			}
		})
	}
}
