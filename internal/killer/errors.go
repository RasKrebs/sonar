package killer

import (
	"errors"
	"fmt"
)

// Error codes from the cross-spec contract's error model (§2). The killer only
// produces the subset it can hit; the daemon and the CLI map them onto their
// own transports.
const (
	CodeNotFound         = "not_found"
	CodeAmbiguous        = "ambiguous"
	CodePermissionDenied = "permission_denied"
	CodeInvalidSelector  = "invalid_selector"
	CodeInternal         = "internal"
)

// CodedError carries a contract error code and its hint alongside the detail
// message that ends up in a Result's Error field.
type CodedError struct {
	Code   string
	Detail string
	Hint   string
	Err    error
}

func (e *CodedError) Error() string { return e.Detail }
func (e *CodedError) Unwrap() error { return e.Err }

func codedf(code, hint string, format string, args ...any) *CodedError {
	return &CodedError{Code: code, Hint: hint, Detail: fmt.Sprintf(format, args...)}
}

// permissionErr is the contract's permission_denied with the sudo hint the
// error-handling section requires of kill.
func permissionErr(pid int, err error) *CodedError {
	return &CodedError{
		Code:   CodePermissionDenied,
		Detail: fmt.Sprintf("not permitted to signal PID %d", pid),
		Hint:   "re-run with sudo",
		Err:    err,
	}
}

// Code returns the contract error code for err, or "internal" for an error the
// killer did not classify. Returns "" for a nil error.
func Code(err error) string {
	if err == nil {
		return ""
	}
	var c *CodedError
	if errors.As(err, &c) {
		return c.Code
	}
	return CodeInternal
}

// Hint returns the actionable hint for err, or "" when there is none.
func Hint(err error) string {
	var c *CodedError
	if errors.As(err, &c) {
		return c.Hint
	}
	return ""
}
