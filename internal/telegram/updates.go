package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Update is the subset of a Telegram update the bot reacts to.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// Message is a subset of a Telegram message.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      *Chat  `json:"chat"`
	Text      string `json:"text"`
}

// CallbackQuery is an inline-keyboard press.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

// User is the person behind an update, which is who the data belongs to.
type User struct {
	ID int64 `json:"id"`
}

// Chat is the conversation, which is where replies go.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// PollTimeout is how long a getUpdates call may block server-side.
const PollTimeout = 50 * time.Second

// GetUpdates long-polls for new updates starting at offset. It uses a dedicated
// client whose timeout exceeds the poll timeout: reusing the 30s send client would
// have it kill every long poll and produce an endless stream of spurious errors.
func (n *Notifier) GetUpdates(ctx context.Context, offset int64) ([]Update, error) {
	form := url.Values{
		"timeout":         {strconv.Itoa(int(PollTimeout.Seconds()))},
		"offset":          {strconv.FormatInt(offset, 10)},
		"allowed_updates": {`["message","callback_query"]`},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/bot%s/getUpdates", n.apiBase, n.token), nil)
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = form.Encode()

	httpResp, err := n.pollClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram: getUpdates: %w", err)
	}
	defer httpResp.Body.Close()

	var decoded struct {
		OK          bool     `json:"ok"`
		Result      []Update `json:"result"`
		Description string   `json:"description"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("telegram: decode getUpdates: %w", err)
	}
	if !decoded.OK {
		return nil, fmt.Errorf("telegram: getUpdates: %s", decoded.Description)
	}
	return decoded.Result, nil
}

// AnswerCallbackQuery clears the spinner on the user's client. Without it Telegram
// shows a progress spinner and then an error toast, which is exactly the friction
// that makes people stop rating.
func (n *Notifier) AnswerCallbackQuery(ctx context.Context, callbackID, text string) error {
	form := url.Values{"callback_query_id": {callbackID}}
	if text != "" {
		form.Set("text", text)
	}
	return n.postForm(ctx, "answerCallbackQuery", form)
}

// ClearRatingKeyboard removes the inline keyboard from a message, so a rating
// cannot be pressed twice from the scrollback days later.
func (n *Notifier) ClearRatingKeyboard(ctx context.Context, chatID string, messageID int64) error {
	form := url.Values{
		"chat_id":      {chatID},
		"message_id":   {strconv.FormatInt(messageID, 10)},
		"reply_markup": {`{"inline_keyboard":[]}`},
	}
	return n.postForm(ctx, "editMessageReplyMarkup", form)
}

// SendText sends a plain message, used for command replies.
func (n *Notifier) SendText(ctx context.Context, chatID, text string) error {
	if n.dryRun() {
		fmt.Fprintf(n.out, "DRY-RUN text to %s: %s\n", chatID, text)
		return nil
	}
	return n.sendMessage(ctx, chatID, text, "")
}

func (n *Notifier) postForm(ctx context.Context, method string, form url.Values) error {
	if n.dryRun() {
		fmt.Fprintf(n.out, "DRY-RUN %s: %s\n", method, form.Encode())
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/bot%s/%s", n.apiBase, n.token, method), nil)
	if err != nil {
		return err
	}
	req.URL.RawQuery = form.Encode()
	return n.post(req)
}
