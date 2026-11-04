package cli

import (
	"testing"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
)

func TestSelectOnlineMachines(t *testing.T) {
	input := []model.Machine{
		{ID: "ccdq", Name: "ccdq", Online: true},
		{ID: "macbook", Name: "macbook", Online: false},
		{ID: "mac-mini", Name: "mac-mini", Online: true},
		{ID: "home-pc", Name: "home-pc", Online: true},
	}

	got := selectOnlineMachines(input)
	if len(got) != 3 {
		t.Fatalf("expected 3 online machines, got %d", len(got))
	}
	for _, m := range got {
		if !m.Online {
			t.Errorf("expected %s to be online", m.ID)
		}
		if m.ID == "macbook" {
			t.Errorf("offline machine macbook should be filtered out")
		}
	}

	if selectOnlineMachines(nil) != nil {
		t.Fatal("expected nil when no machines are online")
	}
}
