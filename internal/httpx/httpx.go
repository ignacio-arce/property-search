// Package httpx holds the HTTP details this bot needs in more than one place.
package httpx

import (
	"errors"
	"net/http"
	"strings"
)

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

// Redact removes secret values from an error message.
//
// Transport errors embed the request URL, and a request URL can carry a credential:
// the Telegram token lives in the path (/bot<token>/sendMessage), so a plain network
// failure would print the token into the log. The bot's logs are read and shared
// when something goes wrong, which is exactly when the leak would happen.
//
// The returned error is a fresh one, so the original chain is not preserved. That is
// deliberate: every caller does is log it, and a credential in a log line outlives
// the convenience of unwrapping.
func Redact(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	redacted := message
	for _, secret := range secrets {
		if secret != "" {
			redacted = strings.ReplaceAll(redacted, secret, "***")
		}
	}
	if redacted == message {
		return err
	}
	return errors.New(redacted)
}
