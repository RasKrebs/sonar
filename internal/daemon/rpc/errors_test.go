package rpc

import "testing"

func TestErrorCodes(t *testing.T) {
	e := NewError(CodeNotFound, "port 3000 not found", "run `sonar list`")
	if e.Code != 1001 || e.Data.Code != "not_found" || e.Data.Hint == "" {
		t.Fatalf("%+v", e)
	}
}

func TestEveryRegistryCodeHasAName(t *testing.T) {
	for _, c := range []int{CodeInvalidParams, CodeInternal, CodeNotFound, CodeAmbiguous,
		CodePermission, CodeUnsupported, CodeBusy, CodeInvalidConfig, CodeAlreadyRunning,
		CodeTimeout, CodeInvalidSelector, CodeOutsideHome, CodeTargetNotListening,
		CodeProviderNotInstalled, CodeProviderUnavailable, CodeProviderNotPermitted,
		CodeProviderAuthFailed, CodeProviderCrashed, CodeProviderTimeout,
		CodeProviderLimitReached, CodeListenPortInUse, CodeInstallDeclined,
		CodeSessionNotFound, CodeClaimConflict} {
		if CodeName(c) == "" {
			t.Fatalf("code %d has no data.code name", c)
		}
	}
	if CodeName(4242) != "internal" {
		t.Fatalf("unknown codes must fall back to internal, got %q", CodeName(4242))
	}
}

func TestErrorImplementsError(t *testing.T) {
	var err error = NewError(CodeTimeout, "timed out", "raise --timeout")
	if err.Error() != "timed out" {
		t.Fatalf("Error() = %q", err.Error())
	}
}
