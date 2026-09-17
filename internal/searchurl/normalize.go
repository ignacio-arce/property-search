// Package searchurl normalizes and validates Zonaprop search URLs.
package searchurl

import (
	"fmt"
	"net/url"
	"strings"
)

// zonapropHost is the only host allowed to be fetched. Matching is on the exact
// host or a dot-suffixed subdomain, never on a substring: a substring check lets
// "zonaprop.com.ar.attacker.test" through, and the deep validation would then
// load that URL in a real browser on the operator's LAN.
const zonapropHost = "zonaprop.com.ar"

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
	return host == zonapropHost || strings.HasSuffix(host, "."+zonapropHost)
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

// recentSortSuffix is Zonaprop's path suffix for ordering by publication date
// descending. The probe confirmed it works, and it is what makes the newest
// listings appear first — which is the whole basis of "tell me what is new".
const recentSortSuffix = "-orden-publicado-descendente"

// InjectRecentSort rewrites a search URL to order by most recently published if it
// does not already. It returns the URL and whether it changed it.
//
// Without this the bot would see whatever default order Zonaprop chooses, and the
// silent baseline would mark an arbitrary slice of the inventory as seen.
func InjectRecentSort(raw string) (string, bool, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw, false, fmt.Errorf("invalid url %q: %w", raw, err)
	}
	base := strings.TrimSuffix(u.Path, "/")
	if strings.HasSuffix(base, recentSortSuffix) {
		return raw, false, nil
	}
	// The suffix is inserted before the .html extension.
	if strings.HasSuffix(base, ".html") {
		base = strings.TrimSuffix(base, ".html") + recentSortSuffix + ".html"
	} else {
		base += recentSortSuffix
	}
	u.Path = base
	return u.String(), true, nil
}
