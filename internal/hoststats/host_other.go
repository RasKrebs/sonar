//go:build !linux && !darwin && !windows

package hoststats

import "context"

// readHost is the fallback for a platform sonar does not ship binaries for.
// Every field stays null, so a client sees "we do not know" rather than a zero
// that reads like an idle machine.
func readHost(_ context.Context) (reading, error) { return reading{}, nil }
