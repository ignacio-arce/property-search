package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"zonapropbot/internal/config"
)

func TestSetMyCommandsSendsTheCommandsAsJSON(t *testing.T) {
	var path string
	var params url.Values
	n, _ := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = r.ParseForm()
		params = r.Form
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	// Accented descriptions exercise the form encoding: a regression there would
	// silently corrupt the menu for every Spanish user.
	commands := []BotCommand{
		{Command: "start", Description: "Darte de alta o reanudar las notificaciones"},
		{Command: "rmurl", Description: "Borrar una búsqueda"},
	}
	if err := n.SetMyCommands(context.Background(), commands); err != nil {
		t.Fatalf("SetMyCommands: %v", err)
	}

	if path != "/bot123:abc/setMyCommands" {
		t.Errorf("path = %s", path)
	}
	var sent []BotCommand
	if err := json.Unmarshal([]byte(params.Get("commands")), &sent); err != nil {
		t.Fatalf("commands is not valid JSON: %v (%q)", err, params.Get("commands"))
	}
	if !reflect.DeepEqual(sent, commands) {
		t.Errorf("commands = %+v, want %+v", sent, commands)
	}
}

// main's WARN-and-continue contract depends on these errors reaching the caller,
// so an ok=false answer must not be swallowed as success.
func TestMenuSetupPropagatesAPIErrors(t *testing.T) {
	n, _ := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "Bad Request: command is invalid"})
	})

	if err := n.SetMyCommands(context.Background(), []BotCommand{{Command: "start", Description: "x"}}); err == nil {
		t.Error("SetMyCommands must surface an ok=false answer")
	}
	if err := n.SetChatMenuButton(context.Background()); err == nil {
		t.Error("SetChatMenuButton must surface an ok=false answer")
	}
}

func TestSetChatMenuButtonOpensTheCommandList(t *testing.T) {
	var path string
	var params url.Values
	n, _ := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = r.ParseForm()
		params = r.Form
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	if err := n.SetChatMenuButton(context.Background()); err != nil {
		t.Fatalf("SetChatMenuButton: %v", err)
	}
	if path != "/bot123:abc/setChatMenuButton" {
		t.Errorf("path = %s", path)
	}
	if params.Get("menu_button") != `{"type":"commands"}` {
		t.Errorf("menu_button = %q", params.Get("menu_button"))
	}
}

func TestMenuSetupDryRunWritesToOutput(t *testing.T) {
	var out stringWriter
	cfg, err := config.Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	dry := New(cfg, nil, &out)

	if err := dry.SetMyCommands(context.Background(), []BotCommand{{Command: "start", Description: "Darte de alta"}}); err != nil {
		t.Fatalf("dry-run SetMyCommands: %v", err)
	}
	if err := dry.SetChatMenuButton(context.Background()); err != nil {
		t.Fatalf("dry-run SetChatMenuButton: %v", err)
	}
	if !strings.Contains(out.String(), "setMyCommands") || !strings.Contains(out.String(), "setChatMenuButton") {
		t.Errorf("dry-run output = %q", out.String())
	}
}
