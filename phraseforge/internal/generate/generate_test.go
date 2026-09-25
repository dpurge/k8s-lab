package generate

import "testing"

// TestPayloadResourceBackCompat covers dialog-vocabulary-models-generation's
// backward-compatibility requirement: a pending/failed job row created
// before this change (payload {"text_id":N,"user_id":M}, no resource_type
// field) must still decode and retry correctly — resourceType()/
// resourceID() treat an absent/empty ResourceType as "text" with TextID as
// the id.
func TestPayloadResourceBackCompat(t *testing.T) {
	tests := []struct {
		name     string
		p        payload
		wantType string
		wantID   int64
	}{
		{"pre-existing text_id-only shape", payload{TextID: 7}, resourceTypeText, 7},
		{"current text shape", payload{ResourceType: "text", ResourceID: 9}, resourceTypeText, 9},
		{"current dialog shape", payload{ResourceType: "dialog", ResourceID: 11}, resourceTypeDialog, 11},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.resourceType(); got != tt.wantType {
				t.Errorf("resourceType() = %q, want %q", got, tt.wantType)
			}
			if got := tt.p.resourceID(); got != tt.wantID {
				t.Errorf("resourceID() = %d, want %d", got, tt.wantID)
			}
		})
	}
}

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
