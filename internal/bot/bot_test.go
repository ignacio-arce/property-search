package bot

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"log"
	"testing"

	"zonapropbot/internal/config"
	"zonapropbot/internal/fetch"
	"zonapropbot/internal/model"
	"zonapropbot/internal/store"
)

const sampleHTML = `<html><body>
<div data-to-posting="/p/uno.html"><h2 data-qa="POSTING_CARD_PRICE">USD 100</h2>
<h3 data-qa="POSTING_CARD_FEATURES"><span>100 m² tot.</span><span>2 amb.</span></h3>
<div data-qa="POSTING_CARD_GALLERY"><img src="https://img/x1.jpg"/></div>
<h4 data-qa="POSTING_CARD_LOCATION">Lugar</h4>
<h2 data-qa="POSTING_CARD_DESCRIPTION"><a href="/p/uno.html">Uno</a></h2></div>
<div data-to-posting="/p/dos.html"><h2 data-qa="POSTING_CARD_PRICE">USD 200</h2>
<h3 data-qa="POSTING_CARD_FEATURES"><span>200 m² tot.</span><span>4 amb.</span></h3>
<div data-qa="POSTING_CARD_GALLERY"><img src="https://img/x2.jpg"/></div>
<h4 data-qa="POSTING_CARD_LOCATION">Lugar2</h4>
<h2 data-qa="POSTING_CARD_DESCRIPTION"><a href="/p/dos.html">Dos</a></h2></div>
</body></html>`

type stubFetcher struct {
	body []byte
	err  error
}

func (s stubFetcher) Fetch(ctx context.Context, u string) (*fetch.Result, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &fetch.Result{Body: s.body, Mode: "stub"}, nil
}

type recordingNotifier struct {
	notified []model.Listing
	err      error
}

func (r *recordingNotifier) Notify(ctx context.Context, l model.Listing) error {
	if r.err != nil {
		return r.err
	}
	r.notified = append(r.notified, l)
	return nil
}

func setup(t *testing.T) (*config.Config, *recordingNotifier, *store.Store) {
	t.Helper()
	cfg, err := config.Load(func(k string) string {
		return map[string]string{"SEARCH_URLS": "https://www.zonaprop.com.ar/buscar.html"}[k]
	})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return cfg, &recordingNotifier{}, st
}

func logger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

func TestFirstRunNotifiesEverything(t *testing.T) {
	cfg, notifier, st := setup(t)
	defer st.Close()
	err := RunOnce(context.Background(), cfg, stubFetcher{body: []byte(sampleHTML)}, notifier, st, logger())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(notifier.notified) != 2 {
		t.Fatalf("notified %d listings, want 2 on first run", len(notifier.notified))
	}
	if !st.Contains(notifier.notified[0].ID) || !st.Contains(notifier.notified[1].ID) {
		t.Error("first run must persist all notified ids")
	}
}

func TestSecondRunOnlyNew(t *testing.T) {
	cfg, notifier, st := setup(t)
	defer st.Close()
	if err := RunOnce(context.Background(), cfg, stubFetcher{body: []byte(sampleHTML)}, notifier, st, logger()); err != nil {
		t.Fatal(err)
	}
	notifier.notified = nil
	if err := RunOnce(context.Background(), cfg, stubFetcher{body: []byte(sampleHTML)}, notifier, st, logger()); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notified) != 0 {
		t.Errorf("second run notified %d, want 0 (all seen)", len(notifier.notified))
	}
}

func TestFailedNotifyIsNotMarkedSeen(t *testing.T) {
	cfg, notifier, st := setup(t)
	defer st.Close()
	notifier.err = io.ErrUnexpectedEOF
	if err := RunOnce(context.Background(), cfg, stubFetcher{body: []byte(sampleHTML)}, notifier, st, logger()); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notified) != 0 {
		t.Fatal("notifier error should prevent any id being recorded")
	}
	if st.Contains(sha1Of("https://www.zonaprop.com.ar/p/uno.html")) {
		t.Error("id must not be marked seen when notify failed")
	}
}

func TestURLFetchFailureContinuesToNext(t *testing.T) {
	cfg, notifier, st := setup(t)
	defer st.Close()
	cfg.SearchURLs = []string{"https://bad.example/x", "https://www.zonaprop.com.ar/buscar.html"}
	err := RunOnce(context.Background(), cfg, stubFetcher{body: []byte(sampleHTML)}, notifier, st, logger())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(notifier.notified) == 0 {
		t.Error("URL fetch failure must not abort processing of remaining URLs")
	}
}

func TestContextCancellationStops(t *testing.T) {
	cfg, _, st := setup(t)
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := RunOnce(ctx, cfg, stubFetcher{body: []byte(sampleHTML)}, &recordingNotifier{}, st, logger())
	if err == nil {
		t.Fatal("expected context error when context is already cancelled")
	}
}

func sha1Of(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}
