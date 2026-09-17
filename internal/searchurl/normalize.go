// Package searchurl normalizes and validates Zonaprop search URLs.
package searchurl

import (
	"fmt"
	"net/url"
	"strings"
)

// ZonapropHost is the only host allowed to be fetched. Matching is on the exact
// host or a dot-suffixed subdomain, never on a substring: a substring check lets
// "zonaprop.com.ar.attacker.test" through, and the deep validation would then
// load that URL in a real browser on the operator's LAN.
const ZonapropHost = "zonaprop.com.ar"

// trackingParams are dropped from the canonical form. Zonaprop appends them to
// every card link and to some search URLs; they vary per page and per position,
// so keeping them would make the same search look like two different ones.
var trackingParams = map[string]bool{
	"n_src":       true,
	"n_pg":        true,
	"n_pos":       true,
	"n_pills":     true,
	"n_search_id": true,
}

// IsZonapropURL reports whether raw is an https URL on Zonaprop itself.
func IsZonapropURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" {
		return false
	}
	return host == ZonapropHost || strings.HasSuffix(host, "."+ZonapropHost)
}

// Normalize returns the canonical form of a search URL: lowercased scheme and
// host, fragment dropped, tracking parameters removed and the remaining query
// sorted. Uniqueness is enforced on this, so the same search pasted from a
// different page (or with tracking) collapses to one row.
func Normalize(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid url %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid url %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid url %q: missing host", raw)
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""

	q := u.Query()
	for key := range q {
		lower := strings.ToLower(key)
		if trackingParams[lower] || strings.HasPrefix(lower, "utm_") {
			q.Del(key)
		}
	}
	u.RawQuery = q.Encode() // Encode sorts by key.

	return u.String(), nil
}
