package httpx

import (
	"errors"
	"strings"
	"testing"
)

func TestRedactRemovesSecrets(t *testing.T) {
	err := errors.New(`Post "https://api.telegram.org/bot123:SECRET/sendMessage": dial tcp: refused`)

	got := Redact(err, "123:SECRET")
	if strings.Contains(got.Error(), "SECRET") {
		t.Errorf("redact left the secret in: %v", got)
	}
	if !strings.Contains(got.Error(), "***") {
		t.Errorf("redact did not mark where the secret was: %v", got)
	}
}

func TestRedactLeavesCleanErrorsAlone(t *testing.T) {
	original := errors.New("something ordinary")
	if got := Redact(original, "SECRET"); got != original {
		t.Errorf("an error without the secret should be returned as is")
	}
	if got := Redact(original); got != original {
		t.Errorf("no secrets means no change")
	}
	if got := Redact(nil, "SECRET"); got != nil {
		t.Errorf("nil stays nil")
	}
}
