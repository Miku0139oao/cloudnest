//go:build !windows

package platform

// SystemProxy is not implemented outside Windows; the engine falls back to the
// proxy environment variables it inherits.
func SystemProxy() (httpProxy, httpsProxy, noProxy string) { return "", "", "" }

// ConvertProxyOverride mirrors the Windows helper so tests can share code.
func ConvertProxyOverride(list string) string { return list }
