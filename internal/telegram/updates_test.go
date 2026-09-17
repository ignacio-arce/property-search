package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"zonapropbot/internal/config"
)

// newAPI points a real Notifier at a stub server, so the request shape is
// exercised rather than mocked away.
func newAPI(t *testing.T, handler http.HandlerFunc) (*Notifier, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	cfg, err := config.Load(func(key string) string {
		if key == "TELEGRAM_BOT_TOKEN" {
			return "123:abc"
		}
		if key == "TELEGRAM_CHAT_ID" {
			return "-100123"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	n := New(cfg, nil, nil)
	n.apiBase = server.URL
	t.Cleanup(server.Close)
	return n, server
}

func TestGetUpdatesParsesUpdatesAndSendsTheOffset(t *testing.T) {
	var seen url.Values
	n, _ := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot123:abc/getUpdates" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = r.ParseForm()
		seen = r.Form
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": []map[string]any{
				{"update_id": 7, "message": map[string]any{
					"message_id": 55,
					"text":       "/start",
					"from":       map[string]any{"id": 42},
					"chat":       map[string]any{"id": 42, "type": "private"},
				}},
				{"update_id": 8, "callback_query": map[string]any{
					"id":      "cb",
					"data":    "u:9",
					"from":    map[string]any{"id": 42},
					"message": map[string]any{"message_id": 56, "chat": map[string]any{"id": 42, "type": "private"}, "caption": "casa"},
				}},
			},
		})
	})

	updates, err := n.GetUpdates(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(updates) != 2 {
		t.Fatalf("got %d updates, want 2", len(updates))
	}
	if updates[0].Message == nil || updates[0].Message.Text != "/start" {
		t.Errorf("first update message = %+v", updates[0].Message)
	}
	if updates[0].Message.Chat.Type != "private" || updates[0].Message.From.ID != 42 {
		t.Errorf("message identity not parsed: %+v", updates[0].Message)
	}
	if updates[1].CallbackQuery == nil || updates[1].CallbackQuery.Data != "u:9" {
		t.Fatalf("callback not parsed: %+v", updates[1].CallbackQuery)
	}
	// The caption matters: it is what the rating note gets appended to.
	if updates[1].CallbackQuery.Message.Caption != "casa" {
		t.Errorf("callback caption = %q", updates[1].CallbackQuery.Message.Caption)
	}

	if got := seen.Get("offset"); got != "7" {
		t.Errorf("offset param = %q, want 7", got)
	}
	if seen.Get("timeout") == "" {
		t.Error("long-poll timeout param missing")
	}
	if seen.Get("allowed_updates") == "" {
		t.Error("allowed_updates param missing")
	}
}

func TestGetUpdatesSurfacesAnAPIError(t *testing.T) {
	n, _ := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "Unauthorized"})
	})
	if _, err := n.GetUpdates(context.Background(), 0); err == nil {
		t.Fatal("expected an error when the API answers ok=false")
	}
}

func TestAnswerCallbackQuerySendsTheText(t *testing.T) {
	var path string
	var params url.Values
	n, _ := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = r.ParseForm()
		params = r.Form
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	if err := n.AnswerCallbackQuery(context.Background(), "cb-1", "Guardado 👍"); err != nil {
		t.Fatalf("AnswerCallbackQuery: %v", err)
	}
	if path != "/bot123:abc/answerCallbackQuery" {
		t.Errorf("path = %s", path)
	}
	if params.Get("callback_query_id") != "cb-1" {
		t.Errorf("callback_query_id = %q", params.Get("callback_query_id"))
	}
	if params.Get("text") != "Guardado 👍" {
		t.Errorf("text = %q", params.Get("text"))
	}
}

// The keyboard must be revoked with an empty inline keyboard, which is what stops
// a rating from being pressed again days later from the scrollback.
func TestClearRatingKeyboardSendsAnEmptyKeyboard(t *testing.T) {
	var params url.Values
	n, _ := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		params = r.Form
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	if err := n.ClearRatingKeyboard(context.Background(), "-100123", 55); err != nil {
		t.Fatalf("ClearRatingKeyboard: %v", err)
	}
	if params.Get("chat_id") != "-100123" || params.Get("message_id") != "55" {
		t.Errorf("target params = %v", params)
	}
	if params.Get("reply_markup") != `{"inline_keyboard":[]}` {
		t.Errorf("reply_markup = %q, want an empty keyboard", params.Get("reply_markup"))
	}
}

func TestEditMessageCaptionSendsTheText(t *testing.T) {
	var params url.Values
	n, _ := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		params = r.Form
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	if err := n.EditMessageCaption(context.Background(), "-100123", 55, "casa\n\n✓ te gustó"); err != nil {
		t.Fatalf("EditMessageCaption: %v", err)
	}
	if params.Get("caption") != "casa\n\n✓ te gustó" {
		t.Errorf("caption = %q", params.Get("caption"))
	}
	if params.Get("message_id") != "55" {
		t.Errorf("message_id = %q", params.Get("message_id"))
	}
}

func TestSendTextAndDryRun(t *testing.T) {
	var params url.Values
	n, _ := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		params = r.Form
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	if err := n.SendText(context.Background(), "-100123", "hola"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if params.Get("text") != "hola" {
		t.Errorf("text = %q", params.Get("text"))
	}

	// Without a token nothing reaches the network; it is written to the output sink.
	var out stringWriter
	cfg, err := config.Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	dry := New(cfg, nil, &out)
	if err := dry.SendText(context.Background(), "-1", "hola"); err != nil {
		t.Fatalf("dry-run SendText: %v", err)
	}
	if !strings.Contains(out.String(), "hola") {
		t.Errorf("dry-run output = %q", out.String())
	}
}

type stringWriter struct{ b []byte }

func (w *stringWriter) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }
func (w *stringWriter) String() string              { return string(w.b) }
