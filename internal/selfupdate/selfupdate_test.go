package selfupdate

import "testing"

func TestIsNewer(t *testing.T) {
	tests := []struct {
		current, remote string
		want            bool
	}{
		{"v1.0.0", "v1.0.1", true},
		{"v1.0.1", "v1.0.0", false},
		{"v1.0.0", "v1.0.0", false},
		{"dev", "v1.0.0", false},
		{"v1.0.0", "v2.0.0", true},
	}

	for _, tt := range tests {
		if got := IsNewer(tt.current, tt.remote); got != tt.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", tt.current, tt.remote, got, tt.want)
		}
	}
}
