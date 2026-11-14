//go:build windows

package platform

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// SystemProxy returns the HTTP proxy configured in the Windows Internet
// Settings dialog. Empty strings mean "not configured".
//
// Cloudnest passes the result to rclone through HTTP_PROXY/HTTPS_PROXY so the
// "use system proxy" switch behaves the same way as the browser does, including
// PAC-free manual proxy servers and the override list.
func SystemProxy() (httpProxy, httpsProxy, noProxy string) {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return "", "", ""
	}
	defer key.Close()

	enabled, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return "", "", ""
	}
	server, _, err := key.GetStringValue("ProxyServer")
	if err != nil || strings.TrimSpace(server) == "" {
		return "", "", ""
	}
	httpProxy, httpsProxy = splitProxyServer(server)

	if override, _, err := key.GetStringValue("ProxyOverride"); err == nil {
		noProxy = ConvertProxyOverride(override)
	}
	return httpProxy, httpsProxy, noProxy
}

// splitProxyServer handles both registry shapes: a single "host:port" value
// used for every protocol, and the "http=h:p;https=h:p;ftp=h:p" form.
func splitProxyServer(server string) (httpProxy, httpsProxy string) {
	server = strings.TrimSpace(server)
	if !strings.Contains(server, "=") {
		u := normalizeProxyURL(server)
		return u, u
	}
	for _, part := range strings.Split(server, ";") {
		scheme, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(scheme)) {
		case "http":
			httpProxy = normalizeProxyURL(value)
		case "https":
			httpsProxy = normalizeProxyURL(value)
		}
	}
	if httpsProxy == "" {
		httpsProxy = httpProxy
	}
	if httpProxy == "" {
		httpProxy = httpsProxy
	}
	return httpProxy, httpsProxy
}

func normalizeProxyURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		return value
	}
	return "http://" + value
}

// ConvertProxyOverride turns the Windows override list into NO_PROXY syntax.
// "<local>" becomes the loopback names and "*.corp.example" becomes
// ".corp.example", which is how Go's proxy matcher spells a domain suffix.
func ConvertProxyOverride(list string) string {
	items := make([]string, 0, 8)
	for _, raw := range strings.Split(list, ";") {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		if strings.EqualFold(item, "<local>") {
			items = append(items, "localhost", "127.0.0.1", "::1")
			continue
		}
		if strings.HasPrefix(item, "*.") {
			item = item[1:]
		}
		items = append(items, item)
	}
	return strings.Join(items, ",")
}
