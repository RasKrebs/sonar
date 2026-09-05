package client

import (
	"errors"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

// TestCheckProtocol pins contract §7's versioning rule: only the major has to
// match, because additive changes bump the minor.
func TestCheckProtocol(t *testing.T) {
	tests := []struct {
		daemon    string
		wantMatch bool
	}{
		{rpc.ProtocolVersion, true},
		{"1.0.0", true},
		{"1.4.0", true},
		{"1.0.99", true},
		{"v1.2.3", true},
		{" 1.2.3 ", true},
		{"2.0.0", false},
		{"0.9.0", false},
	}

	for _, tt := range tests {
		err := CheckProtocol(tt.daemon)
		if tt.wantMatch && err != nil {
			t.Errorf("CheckProtocol(%q) = %v, want nil", tt.daemon, err)
			continue
		}
		if !tt.wantMatch {
			var mismatch *ProtocolMismatchError
			if !errors.As(err, &mismatch) {
				t.Errorf("CheckProtocol(%q) = %v, want a ProtocolMismatchError", tt.daemon, err)
				continue
			}
			if !strings.Contains(mismatch.Error(), "sonar daemon restart") {
				t.Errorf("mismatch message has no restart hint: %q", mismatch.Error())
			}
		}
	}
}

func TestCheckProtocolRejectsGarbage(t *testing.T) {
	for _, v := range []string{"", "   ", "one.two.three", "x"} {
		if err := CheckProtocol(v); err == nil {
			t.Errorf("CheckProtocol(%q) accepted an unparseable version", v)
		}
	}
}

func TestSubscribeOptionsInclude(t *testing.T) {
	tests := []struct {
		opts SubscribeOptions
		want string
	}{
		{SubscribeOptions{}, ""},
		{SubscribeOptions{Stats: true}, "stats"},
		{SubscribeOptions{Health: true}, "health"},
		{SubscribeOptions{Stats: true, Health: true}, "stats,health"},
	}
	for _, tt := range tests {
		got := strings.Join(tt.opts.include(), ",")
		if got != tt.want {
			t.Errorf("include() for %+v = %q, want %q", tt.opts, got, tt.want)
		}
	}
}
