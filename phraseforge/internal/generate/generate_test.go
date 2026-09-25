package generate

import "testing"

func TestDecideTargetList(t *testing.T) {
	tests := []struct {
		name       string
		existingID int64
		found      bool
		want       targetListDecision
	}{
		{"existing list found", 42, true, targetListDecision{Reuse: true, ListID: 42}},
		{"no existing list", 0, false, targetListDecision{Reuse: false, ListID: 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideTargetList(tt.existingID, tt.found)
			if got != tt.want {
				t.Fatalf("decideTargetList(%d, %v) = %+v, want %+v", tt.existingID, tt.found, got, tt.want)
			}
		})
	}
}
