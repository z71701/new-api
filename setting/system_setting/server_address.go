package system_setting

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

var ErrServerAddressInvalid = errors.New("server address must be an absolute HTTP(S) URL without a path, query, fragment, or credentials")
var ErrServerAddressInsecure = errors.New("production server address must use HTTPS")
var ErrServerAddressNonPublic = errors.New("production server address cannot use localhost, loopback, or private IP addresses")

// NormalizeServerAddress validates the public origin used in generated links.
// Local development may opt into HTTP and loopback hosts; production callers
// must pass allowInsecure=false.
func NormalizeServerAddress(raw string, allowInsecure bool) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}

	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", ErrServerAddressInvalid
	}

	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if hostname == "" {
		return "", ErrServerAddressInvalid
	}
	if !allowInsecure {
		if parsed.Scheme != "https" {
			return "", ErrServerAddressInsecure
		}
		if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
			return "", ErrServerAddressNonPublic
		}
		if ip := net.ParseIP(hostname); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()) {
			return "", ErrServerAddressNonPublic
		}
	}

	parsed.Path = ""
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}
