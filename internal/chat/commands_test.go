package chat

import (
	"regexp"
	"testing"
)

// A single malformed or duplicated entry makes Telegram reject the whole command
// list, so the catalog is validated against the Bot API rules before it is sent.
func TestBotCommandsAreValidForTelegram(t *testing.T) {
	named := regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	seen := map[string]bool{}
	for _, c := range BotCommands() {
		if !named.MatchString(c.Command) {
			t.Errorf("command %q is invalid: lowercase a-z, 0-9 and _ only, max 32 chars", c.Command)
		}
		if seen[c.Command] {
			t.Errorf("command %q is duplicated", c.Command)
		}
		seen[c.Command] = true
		if n := len([]rune(c.Description)); n < 1 || n > 256 {
			t.Errorf("description of %q is %d chars, want 1-256", c.Command, n)
		}
	}
}

// /help is the human-readable view of the same commands, so the menu and the help
// must list exactly the same set. Deriving the expectation from helpText keeps the
// two from drifting in either direction without duplicating the catalog here.
func TestBotCommandsMatchHelp(t *testing.T) {
	documented := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)(?:^|\s)/([a-z0-9_]+)`).FindAllStringSubmatch(helpText(), -1) {
		documented[m[1]] = true
	}

	inMenu := map[string]bool{}
	for _, c := range BotCommands() {
		inMenu[c.Command] = true
		if !documented[c.Command] {
			t.Errorf("command %q is in the menu but missing from /help", c.Command)
		}
	}
	for name := range documented {
		if !inMenu[name] {
			t.Errorf("command %q is documented in /help but missing from the menu", name)
		}
	}
}
