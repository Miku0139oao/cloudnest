// Package apperr maps rclone/OS failures to stable codes the UI can localise.
package apperr

import (
	"regexp"
	"strings"
)

type Code string

const (
	Network             Code = "network"
	AuthExpired         Code = "auth_expired"
	Permission          Code = "permission"
	Quota               Code = "quota"
	RateLimited         Code = "rate_limited"
	NotFound            Code = "not_found"
	DiskFull            Code = "disk_full"
	PathTooLong         Code = "path_too_long"
	FileLocked          Code = "file_locked"
	Corrupt             Code = "corrupt"
	ProviderUnsupported Code = "provider_unsupported"
	EngineMissing       Code = "engine_missing"
	EngineOld           Code = "engine_old"
	EngineNotReady      Code = "engine_not_ready"
	OAuthPortBusy       Code = "oauth_port_busy"
	OAuthBusy           Code = "oauth_busy"
	Cancelled           Code = "cancelled"
	Unknown             Code = "unknown"
)

type Error struct {
	Code    Code
	Message string // developer-facing English
	Detail  string // redacted rclone text
	Retry   bool
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return string(e.Code) + ": " + e.Message + ": " + e.Detail
	}
	return string(e.Code) + ": " + e.Message
}

func New(code Code, message string) *Error { return &Error{Code: code, Message: message} }

type rule struct {
	re    *regexp.Regexp
	code  Code
	msg   string
	retry bool
}

var rules = []rule{
	{regexp.MustCompile(`(?i)invalid_grant|token (has )?expired|401 unauthorized`), AuthExpired, "sign-in expired", false},
	{regexp.MustCompile(`(?i)storageQuotaExceeded|quota`), Quota, "cloud storage is full", false},
	{regexp.MustCompile(`(?i)rateLimitExceeded|429|too many requests|pacer`), RateLimited, "provider is throttling", true},
	{regexp.MustCompile(`(?i)insufficientPermissions|403 forbidden`), Permission, "permission denied", false},
	{regexp.MustCompile(`(?i)no space left|disk full|not enough space`), DiskFull, "disk is full", false},
	{regexp.MustCompile(`(?i)path too long|filename too long`), PathTooLong, "path is too long", false},
	{regexp.MustCompile(`(?i)being used by another process|cannot access the file`), FileLocked, "file is in use", true},
	{regexp.MustCompile(`(?i)no such host|connection refused|i/o timeout|tls handshake|network is unreachable`), Network, "network error", true},
	{regexp.MustCompile(`(?i)object not found|directory not found|no such file`), NotFound, "not found", false},
	{regexp.MustCompile(`(?i)corrupt|checksum mismatch|hash mismatch`), Corrupt, "data is corrupt", true},
	{regexp.MustCompile(`(?i)not supported|unsupported`), ProviderUnsupported, "provider does not support this", false},
}

// Classify inspects an HTTP status and an rclone error string.
func Classify(status int, message string) *Error {
	switch status {
	case 401:
		return &Error{Code: AuthExpired, Message: "sign-in expired"}
	case 403:
		return &Error{Code: Permission, Message: "permission denied"}
	case 404:
		return &Error{Code: NotFound, Message: "not found"}
	case 429:
		return &Error{Code: RateLimited, Message: "provider is throttling", Retry: true}
	}
	msg := strings.TrimSpace(message)
	for _, r := range rules {
		if r.re.MatchString(msg) {
			return &Error{Code: r.code, Message: r.msg, Detail: redact(msg), Retry: r.retry}
		}
	}
	if status >= 500 {
		return &Error{Code: Network, Message: "provider error", Detail: redact(msg), Retry: true}
	}
	return &Error{Code: Unknown, Message: "unexpected error", Detail: redact(msg), Retry: true}
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(access_token|refresh_token|client_secret|state|token)=[^&\s"]+`),
	regexp.MustCompile(`(?i)Authorization:\s*Basic\s+\S+`),
}

func redact(s string) string {
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, "$1=***")
	}
	if len(s) > 600 {
		s = s[:600] + "…"
	}
	return s
}
