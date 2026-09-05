package cmd

import (
	"testing"

	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/ports"
)

func TestFilterByGroup(t *testing.T) {
	pp := []ports.ListeningPort{
		{Port: 8123, Group: "sonar"},
		{Port: 8124, Group: "sonar"},
		{Port: 5173, Group: "storefront"},
		{Port: 3000},
		// A legacy `sonar run --tag` label still matches, because --tag is an
		// alias of --group for one release.
		{Port: 9000, Tag: "legacy", RunID: "abc123"},
	}

	tests := []struct {
		name  string
		value string
		want  []int
	}{
		{"by group", "sonar", []int{8123, 8124}},
		{"another group", "storefront", []int{5173}},
		{"unknown group", "ghost", nil},
		{"legacy run tag", "legacy", []int{9000}},
		{"legacy run id", "abc123", []int{9000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterByGroup(pp, tt.value)
			if len(got) != len(tt.want) {
				t.Fatalf("filterByGroup(%q) = %v, want ports %v", tt.value, got, tt.want)
			}
			for i, p := range got {
				if p.Port != tt.want[i] {
					t.Fatalf("filterByGroup(%q) = %v, want ports %v", tt.value, got, tt.want)
				}
			}
		})
	}
}

func TestDefaultColumnsIncludeGroup(t *testing.T) {
	found := false
	for _, c := range display.DefaultColumns {
		if c == "group" {
			found = true
		}
	}
	if !found {
		t.Errorf("DefaultColumns = %v, want a group column", display.DefaultColumns)
	}
	if err := ValidateColumns([]string{"group", "tag"}); err != nil {
		t.Errorf("group and its tag alias must both be valid columns: %v", err)
	}
}
