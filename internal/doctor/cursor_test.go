package doctor

import (
	"testing"
)

// TestCursorSuite_ReturnsValidSuite verifies CursorSuite returns Suite with all handlers.
func TestCursorSuite_ReturnsValidSuite(t *testing.T) {
	suite := CursorSuite("cursor", "agent")
	if suite.Prepare == nil {
		t.Fatal("CursorSuite.Prepare should not be nil")
	}
	if suite.Install == nil {
		t.Fatal("CursorSuite.Install should not be nil")
	}
	if suite.Head == nil {
		t.Fatal("CursorSuite.Head should not be nil")
	}
}

// TestIsCursorInstance correctly identifies cursor-remote@ instances.
func TestIsCursorInstance(t *testing.T) {
	tests := []struct {
		unit string
		want bool
	}{
		{"cursor-remote@repo1.service", true},
		{"cursor-remote@my-repo.service", true},
		{"cursor-remote@.service", false}, // template is not an instance
		{"claude-remote@repo1.service", false},
		{"cursor-remote@repo1", false}, // missing .service
		{"cursor-remote@repo1.socket", false},
		{"cursor-remote@", false},
		{"", false},
		{"cursor-remote@test-with-dashes.service", true},
	}

	for _, tt := range tests {
		got := isCursorInstance(tt.unit)
		if got != tt.want {
			t.Errorf("isCursorInstance(%q) = %v, want %v", tt.unit, got, tt.want)
		}
	}
}
