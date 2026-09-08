package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"zonapropbot/internal/config"
	"zonapropbot/internal/model"
)

type stubImageFetcher struct {
	body []byte
	err  error
}

func (s stubImageFetcher) Fetch(ctx context.Context, u string) ([]byte, error) {
	return s.body, s.err
}

func notifierFor(t *testing.T, env map[string]string, img imageFetcher) (*Notifier, *bytes.Buffer) {
	t.Helper()
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	var out bytes.Buffer
	n := New(cfg, img, &out)
	n.minDelay = 0
	return n, &out
}

func listing() model.Listing {
	return model.Listing{
		ID:        "abc",
		URL:       "https://www.zonaprop.com.ar/p/casa.html",
		Title:     "Casa en San Isidro",
		Price:     "USD 120.000",
		M2:        "180 m² tot.",
		Ambientes: "3 amb.",
		Location:  "San Isidro, GBA Norte",
		PhotoURL:  "https://imgar.zonapropcdn.com/avisos/1/2/3.jpg",
	}
}

func TestDryRunPrintsWithoutNetwork(t *testing.T) {
	var hit atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
	}))
	defer server.Close()

	n, out := notifierFor(t, map[string]string{"SEARCH_URLS": "u"}, nil)
	n.apiBase = server.URL
	if err := n.Notify(context.Background(), listing()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if hit.Load() {
		t.Error("dry-run must not hit the network")
	}
	text := out.String()
	for _, want := range []string{"Casa en San Isidro", "USD 120.000", "180 m² tot.", "3 amb.", "https://www.zonaprop.com.ar/p/casa.html"} {
		if !strings.Contains(text, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, text)
		}
	}
}

func TestNotifySendsMultipartPhoto(t *testing.T) {
	photoBytes := []byte("fake-jpeg-bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendPhoto") {
			t.Errorf("path = %s, want sendPhoto", r.URL.Path)
		}
		r.ParseMultipartForm(10 << 20)
		if got := r.FormValue("chat_id"); got != "-100123" {
			t.Errorf("chat_id = %q", got)
		}
		caption := r.FormValue("caption")
		for _, want := range []string{"Casa en San Isidro", "USD 120.000", "https://www.zonaprop.com.ar/p/casa.html"} {
			if !strings.Contains(caption, want) {
				t.Errorf("caption missing %q: %q", want, caption)
			}
		}
		file, _, err := r.FormFile("photo")
		if err != nil {
			t.Errorf("photo file: %v", err)
			return
		}
		got, _ := io.ReadAll(file)
		if !bytes.Equal(got, photoBytes) {
			t.Errorf("photo bytes mismatch")
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()

	n, _ := notifierFor(t, map[string]string{
		"SEARCH_URLS":        "u",
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"TELEGRAM_CHAT_ID":   "-100123",
	}, stubImageFetcher{body: photoBytes})
	n.apiBase = server.URL
	if err := n.Notify(context.Background(), listing()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
}

func TestNotifyFallsBackToTextWhenNoPhoto(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Errorf("path = %s, want sendMessage (no image)", r.URL.Path)
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()

	l := listing()
	l.PhotoURL = ""
	n, _ := notifierFor(t, map[string]string{
		"SEARCH_URLS":        "u",
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"TELEGRAM_CHAT_ID":   "-100123",
	}, nil)
	n.apiBase = server.URL
	if err := n.Notify(context.Background(), l); err != nil {
		t.Fatalf("Notify: %v", err)
	}
}

func TestNotifyFallsBackToTextWhenImageFetchFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Errorf("path = %s, want sendMessage fallback", r.URL.Path)
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()

	n, _ := notifierFor(t, map[string]string{
		"SEARCH_URLS":        "u",
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"TELEGRAM_CHAT_ID":   "-100123",
	}, stubImageFetcher{err: io.ErrUnexpectedEOF})
	n.apiBase = server.URL
	if err := n.Notify(context.Background(), listing()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
}

func TestNotifyHonorsRetryAfter(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			resp := map[string]any{
				"ok":          false,
				"error_code":  429,
				"description": "Too Many Requests: retry after 1",
				"parameters":  map[string]any{"retry_after": 1},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()

	n, _ := notifierFor(t, map[string]string{
		"SEARCH_URLS":        "u",
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"TELEGRAM_CHAT_ID":   "-100123",
	}, stubImageFetcher{body: []byte("img")})
	n.apiBase = server.URL
	if err := n.Notify(context.Background(), listing()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("telegram calls = %d, want 2 (retry after 429)", calls.Load())
	}
}

func TestNotifyBuildsCaption(t *testing.T) {
	n, _ := notifierFor(t, map[string]string{"SEARCH_URLS": "u"}, nil)
	caption := n.caption(listing())
	for _, want := range []string{"Casa en San Isidro", "USD 120.000", "180 m² tot.", "3 amb.", "San Isidro, GBA Norte", "https://www.zonaprop.com.ar/p/casa.html"} {
		if !strings.Contains(caption, want) {
			t.Errorf("caption missing %q:\n%s", want, caption)
		}
	}
}

func TestMultipartFieldNames(t *testing.T) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	if err := addFormField(w, "chat_id", "-100123"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if !strings.Contains(b.String(), `name="chat_id"`) {
		t.Error("multipart missing chat_id field")
	}
}
