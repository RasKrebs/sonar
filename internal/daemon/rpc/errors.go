package rpc

import "fmt"

// ErrorData is the contract §2 payload every error carries. Clients branch on
// Code (the stable string), never on the numeric JSON-RPC code.
type ErrorData struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Hint   string `json:"hint"`
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int       `json:"code"`
	Message string    `json:"message"`
	Data    ErrorData `json:"data"`
}

func (e *Error) Error() string { return e.Message }

// Error code registry, contract §2. Numeric codes are the JSON-RPC wire codes;
// CodeName maps each to the stable string in error.data.code.
const (
	CodeInvalidParams   = -32602
	CodeInternal        = 1000
	CodeNotFound        = 1001
	CodeAmbiguous       = 1002
	CodePermission      = 1003
	CodeUnsupported     = 1004
	CodeBusy            = 1005
	CodeInvalidConfig   = 1006
	CodeAlreadyRunning  = 1007
	CodeTimeout         = 1008
	CodeInvalidSelector = 1009
	CodeOutsideHome     = 1010
	CodeConflict        = 1011

	// 1100-1199 are owned by spec 3 (expose and proxies).
	CodeTargetNotListening   = 1100
	CodeProviderNotInstalled = 1101
	CodeProviderUnavailable  = 1102
	CodeProviderNotPermitted = 1103
	CodeProviderAuthFailed   = 1104
	CodeProviderCrashed      = 1105
	CodeProviderTimeout      = 1106
	CodeProviderLimitReached = 1107
	CodeListenPortInUse      = 1108
	CodeInstallDeclined      = 1109

	// 1200-1299 are owned by spec 2 (MCP, sessions and claims).
	CodeSessionNotFound = 1200
	CodeClaimConflict   = 1201
)

var codeNames = map[int]string{
	CodeInvalidParams:   "invalid_params",
	CodeInternal:        "internal",
	CodeNotFound:        "not_found",
	CodeAmbiguous:       "ambiguous",
	CodePermission:      "permission_denied",
	CodeUnsupported:     "unsupported_platform",
	CodeBusy:            "daemon_busy",
	CodeInvalidConfig:   "invalid_config",
	CodeAlreadyRunning:  "already_running",
	CodeTimeout:         "timeout",
	CodeInvalidSelector: "invalid_selector",
	CodeOutsideHome:     "outside_home",
	CodeConflict:        "conflict",

	CodeTargetNotListening:   "target_not_listening",
	CodeProviderNotInstalled: "provider_not_installed",
	CodeProviderUnavailable:  "provider_unavailable",
	CodeProviderNotPermitted: "provider_not_permitted",
	CodeProviderAuthFailed:   "provider_auth_failed",
	CodeProviderCrashed:      "provider_crashed",
	CodeProviderTimeout:      "provider_timeout",
	CodeProviderLimitReached: "provider_limit_reached",
	CodeListenPortInUse:      "listen_port_in_use",
	CodeInstallDeclined:      "install_declined",

	CodeSessionNotFound: "session_not_found",
	CodeClaimConflict:   "claim_conflict",
}

// CodeName maps a numeric code to its stable data.code string. Unknown codes
// degrade to "internal" so a client never has to handle an empty code.
func CodeName(code int) string {
	if name, ok := codeNames[code]; ok {
		return name
	}
	return codeNames[CodeInternal]
}

// NewError builds an error carrying both the numeric code and the contract's
// {code, detail, hint} data. Hint is a ready-to-print CLI suggestion.
func NewError(code int, detail, hint string) *Error {
	return &Error{
		Code:    code,
		Message: detail,
		Data:    ErrorData{Code: CodeName(code), Detail: detail, Hint: hint},
	}
}

// Errorf is NewError with a formatted detail and no hint.
func Errorf(code int, format string, args ...any) *Error {
	return NewError(code, fmt.Sprintf(format, args...), "")
}
