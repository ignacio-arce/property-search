// Package httpx holds the one HTTP detail this bot needs in more than one place.
package httpx

import "net/http"

// NoProxyTransport clones the default transport with proxying disabled.
//
// The bot must never inherit HTTP_PROXY from the environment. That variable is for
// Zonaprop traffic only (via ZONAPROP_PROXY, applied explicitly on the tls-client
// path); Go reads HTTP_PROXY by default, so a container that has it set would
// silently route Telegram calls and FlareSolverr calls — whose compose service
// name does not resolve at the proxy — through a rotating proxy and break them.
func NoProxyTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return t
}
