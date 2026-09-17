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
	price := int64(120000)
	m2 := 180.0
	rooms := 3
	return model.Listing{
		ZonapropID:   "abc",
		CanonicalURL: "https://www.zonaprop.com.ar/p/casa.html",
		Title:        "Casa en San Isidro",
		PriceAmount:  &price,
		Currency:     "USD",
		M2Tot:        &m2,
		M2Basis:      "tot",
		Rooms:        &rooms,
		Location:     "San Isidro, GBA Norte",
		PhotoURL:     "https://imgar.zonapropcdn.com/avisos/1/2/3.jpg",
	}
}

func TestDryRunPrintsWithoutNetwork(t *testing.T) {
	var hit atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
	}))
	defer server.Close()

	n, out := notifierFor(t, map[string]string{}, nil)
	n.apiBase = server.URL
	if err := n.Notify(context.Background(), "", model.Delivery{ListingID: 42, Listing: listing()}); err != nil {
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
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"TELEGRAM_CHAT_ID":   "-100123",
	}, stubImageFetcher{body: photoBytes})
	n.apiBase = server.URL
	if err := n.Notify(context.Background(), "", model.Delivery{ListingID: 42, Listing: listing()}); err != nil {
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
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"TELEGRAM_CHAT_ID":   "-100123",
	}, nil)
	n.apiBase = server.URL
	if err := n.Notify(context.Background(), "", model.Delivery{ListingID: 42, Listing: l}); err != nil {
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
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"TELEGRAM_CHAT_ID":   "-100123",
	}, stubImageFetcher{err: io.ErrUnexpectedEOF})
	n.apiBase = server.URL
	if err := n.Notify(context.Background(), "", model.Delivery{ListingID: 42, Listing: listing()}); err != nil {
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
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"TELEGRAM_CHAT_ID":   "-100123",
	}, stubImageFetcher{body: []byte("img")})
	n.apiBase = server.URL
	if err := n.Notify(context.Background(), "", model.Delivery{ListingID: 42, Listing: listing()}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("telegram calls = %d, want 2 (retry after 429)", calls.Load())
	}
}

func TestNotifyBuildsCaption(t *testing.T) {
	caption := caption(model.Delivery{Listing: listing()})
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

func TestNotifySendsRatingKeyboard(t *testing.T) {
	var markup string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(10 << 20)
		markup = r.FormValue("reply_markup")
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()

	n, _ := notifierFor(t, map[string]string{
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"TELEGRAM_CHAT_ID":   "-100123",
	}, stubImageFetcher{body: []byte("img")})
	n.apiBase = server.URL
	if err := n.Notify(context.Background(), "", model.Delivery{ListingID: 4242, Listing: listing()}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	for _, want := range []string{`"callback_data":"u:4242"`, `"callback_data":"d:4242"`} {
		if !strings.Contains(markup, want) {
			t.Errorf("reply_markup missing %q: %s", want, markup)
		}
	}
}

// The URL is the one field the user always needs, so truncation must never eat it.
func TestCaptionKeepsURLWhenTruncated(t *testing.T) {
	l := listing()
	l.Title = strings.Repeat("Departamento amplio luminoso ", 200)

	got := caption(model.Delivery{Listing: l})
	if n := UTF16Len(got); n > CaptionLimit {
		t.Errorf("caption is %d UTF-16 units, limit is %d", n, CaptionLimit)
	}
	if !strings.Contains(got, l.CanonicalURL) {
		t.Error("truncated caption lost the URL")
	}
}

// An emoji outside the BMP counts as two UTF-16 code units, so a rune count would
// let the caption exceed the limit and the API would reject it.
func TestCaptionCountsUTF16Units(t *testing.T) {
	l := listing()
	l.Title = strings.Repeat("🏠", 600) // 1200 UTF-16 units on its own

	got := caption(model.Delivery{Listing: l})
	if n := UTF16Len(got); n > CaptionLimit {
		t.Errorf("caption is %d UTF-16 units, limit is %d", n, CaptionLimit)
	}
	if !strings.Contains(got, l.CanonicalURL) {
		t.Error("caption lost the URL")
	}
}

// A rejected photo request falls back to text, so the user still gets the listing
// instead of nothing.
func TestPhotoBadRequestFallsBackToText(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/sendPhoto") {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"ok":false,"description":"Bad Request: caption is too long"}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()

	n, _ := notifierFor(t, map[string]string{
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"TELEGRAM_CHAT_ID":   "-100123",
	}, stubImageFetcher{body: []byte("img")})
	n.apiBase = server.URL
	if err := n.Notify(context.Background(), "", model.Delivery{ListingID: 1, Listing: listing()}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(paths) != 2 || !strings.HasSuffix(paths[1], "/sendMessage") {
		t.Errorf("paths = %v, want sendPhoto then sendMessage", paths)
	}
}

type recordingImageFetcher struct{ urls []string }

func (f *recordingImageFetcher) Fetch(_ context.Context, u string) ([]byte, error) {
	f.urls = append(f.urls, u)
	return []byte("img"), nil
}

// The photo URL comes from third-party HTML and the bot runs inside the operator's
// network, so an arbitrary one is a server-side request forgery: fetching it would
// make the operator's host request an internal address on the page's behalf. The
// card must still go out, without the photo.
func TestOffsitePhotoHostIsNotFetched(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Errorf("expected a text fallback, got %s", r.URL.Path)
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()

	img := &recordingImageFetcher{}
	n, _ := notifierFor(t, map[string]string{
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"TELEGRAM_CHAT_ID":   "-100123",
	}, img)
	n.apiBase = server.URL

	l := listing()
	l.PhotoURL = "http://169.254.169.254/latest/meta-data/iam/security-credentials/"
	if err := n.Notify(context.Background(), "", model.Delivery{ListingID: 1, Listing: l}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(img.urls) != 0 {
		t.Errorf("an off-site photo was fetched: %v", img.urls)
	}
}

func TestPhotoHostAllowlist(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://imgar.zonapropcdn.com/avisos/1/2/3.jpg", true},
		{"https://zonapropcdn.com/x.jpg", true},
		{"https://www.zonaprop.com.ar/x.jpg", true},
		{"http://imgar.zonapropcdn.com/x.jpg", false},      // http
		{"https://zonapropcdn.com.evil.test/x.jpg", false}, // suffix trap
		{"https://169.254.169.254/x.jpg", false},           // cloud metadata
		{"http://192.168.1.1/admin", false},                // LAN
		{"not a url", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := photoURLAllowed(tc.url); got != tc.want {
			t.Errorf("photoURLAllowed(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

// A send failure must never put the bot token in a log line. The token lives in the
// request URL, and net/http errors include the URL, so an unredacted error leaks it.
func TestNetworkErrorsDoNotLeakTheToken(t *testing.T) {
	n, _ := notifierFor(t, map[string]string{
		"TELEGRAM_BOT_TOKEN": "123456:SECRET-TOKEN-VALUE",
		"TELEGRAM_CHAT_ID":   "-100123",
	}, nil)
	// Nothing is listening here, so the request fails and the error travels up to
	// the caller's log.
	n.apiBase = "http://127.0.0.1:1"

	err := n.SendText(context.Background(), "-100123", "hola")
	if err == nil {
		t.Fatal("expected a network error")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN-VALUE") {
		t.Errorf("the bot token leaked into the error: %v", err)
	}
}
