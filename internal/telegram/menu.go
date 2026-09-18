package telegram

import (
	"context"
	"encoding/json"
	"net/url"
)

// BotCommand is one entry of the command list Telegram shows in the menu button
// next to the message field. The names and descriptions are validated by the
// Bot API: a single malformed entry makes it reject the whole list.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetMyCommands publishes the bot's command list. Telegram shows it both when the
// user types "/" and in the menu button, so every option is reachable without
// typing. The call is global: it replaces the list for all users.
func (n *Notifier) SetMyCommands(ctx context.Context, commands []BotCommand) error {
	payload, err := json.Marshal(commands)
	if err != nil {
		return err
	}
	return n.postForm(ctx, "setMyCommands", url.Values{"commands": {string(payload)}})
}

// SetChatMenuButton pins the menu button to open the command list. It is the
// default, but setting it explicitly keeps the options reachable even if the
// button was previously pointed at a Mini App.
func (n *Notifier) SetChatMenuButton(ctx context.Context) error {
	return n.postForm(ctx, "setChatMenuButton", url.Values{
		"menu_button": {`{"type":"commands"}`},
	})
}
