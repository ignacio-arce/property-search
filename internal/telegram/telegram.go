package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"zonapropbot/internal/config"
	"zonapropbot/internal/model"
)

// imageFetcher downloads image bytes for a photo. The fetch.Client satisfies it,
// giving image downloads the same TLS fingerprint / proxy behaviour as page
// fetches.
type imageFetcher interface {
	Fetch(ctx context.Context, u string) ([]byte, error)
}

const (
	defaultMinSendDelay = 2500 * time.Millisecond
	// captionLimit is Telegram's cap on a photo caption, measured in UTF-16 code
	// units (an astral-plane emoji counts as two, a rune count does not).
	captionLimit = 1024
)

// Notifier sends listing alerts to a Telegram chat. Without a token it runs in
// dry-run mode, printing alerts to out instead of the network.
type Notifier struct {
	token    string
	chatID   string // default target when Notify is called with an empty chatID
	apiBase  string
	client   *http.Client
	img      imageFetcher
	out      io.Writer
	mu       sync.Mutex
	lastSent time.Time
	minDelay time.Duration
}

// New builds a Notifier. img may be nil (then photos are skipped). out receives
// dry-run output and defaults to a silent sink when nil. chatID is only a default
// target for local runs; in production the recipient comes from the database.
func New(cfg *config.Config, img imageFetcher, out io.Writer) *Notifier {
	if out == nil {
		out = io.Discard
	}
	return &Notifier{
		token:    cfg.TelegramBotToken,
		chatID:   cfg.TelegramChatID,
		apiBase:  "https://api.telegram.org",
		client:   &http.Client{Timeout: 30 * time.Second, Transport: noProxyTransport()},
		img:      img,
		out:      out,
		minDelay: defaultMinSendDelay,
	}
}

// noProxyTransport clones the default transport with proxying disabled. The bot
// must never inherit HTTP_PROXY from the environment: that variable is for
// Zonaprop traffic only (via ZONAPROP_PROXY), and routing Telegram calls through
// a rotating proxy would break them. It would also break calls to FlareSolverr,
// whose compose service name does not resolve at the proxy.
func noProxyTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return t
}

// dryRun is true without a bot token. It deliberately does not depend on the chat
// id: an operator who forgets to set a target must still get alerts sent or fail
// loudly, not silently print to stdout.
func (n *Notifier) dryRun() bool { return n.token == "" }

// Notify delivers one listing to chatID (empty falls back to the configured
// default). listingID is the database row id used to build the rating keyboard;
// pass 0 to send without one.
func (n *Notifier) Notify(ctx context.Context, chatID string, listingID int64, l model.Listing) error {
	target := chatID
	if target == "" {
		target = n.chatID
	}

	text := caption(l)
	markup := ratingKeyboard(listingID)

	if n.dryRun() {
		fmt.Fprintf(n.out, "DRY-RUN alert: %s\n", text)
		if markup != "" {
			fmt.Fprintf(n.out, "  keyboard: u:%d d:%d\n", listingID, listingID)
		}
		if l.PhotoURL != "" {
			fmt.Fprintf(n.out, "  photo: %s\n", l.PhotoURL)
		}
		return nil
	}

	var photo []byte
	if l.PhotoURL != "" && n.img != nil {
		if b, err := n.img.Fetch(ctx, l.PhotoURL); err == nil {
			photo = b
		}
	}

	if err := n.throttle(ctx); err != nil {
		return err
	}

	if len(photo) > 0 {
		err := n.sendPhoto(ctx, target, photo, text, markup)
		if err == nil {
			return nil
		}
		// Only fall back to a text message on a rejected request (a caption the API
		// refuses, an image it rejects). A rate limit or a network error must not be
		// retried as a second message: that would duplicate the listing.
		var apiErr *apiError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
			return err
		}
	}

	return n.sendMessage(ctx, target, text, markup)
}

// caption renders the card within Telegram's limit. The URL is reserved first:
// it is the one field the user always needs, and appending it last means naive
// truncation deletes the link before anything else.
func caption(l model.Listing) string {
	var head []string
	if l.Title != "" {
		head = append(head, l.Title)
	}
	if p := l.PriceLabel(); p != "" {
		head = append(head, p)
	}
	if s := l.SizeLabel(); s != "" {
		head = append(head, s)
	}
	if l.ExpensasLabel() != "" {
		head = append(head, l.ExpensasLabel()+" expensas")
	}
	if l.Location != "" {
		head = append(head, l.Location)
	}

	url := l.CanonicalURL
	// Reserve the URL plus the newline that joins it to the body.
	budget := captionLimit - utf16Len(url) - 1
	if budget < 1 {
		return truncateUTF16(url, captionLimit)
	}

	body := truncateUTF16(strings.Join(head, "\n"), budget)
	if body == "" {
		return url
	}
	return body + "\n" + url
}

// ratingKeyboard builds the inline 👍/👎 markup. callback_data carries the
// database id, not the Zonaprop id, because the callback handler needs the row it
// should attach the rating to — and because Telegram caps callback_data at 64
// bytes.
func ratingKeyboard(listingID int64) string {
	if listingID == 0 {
		return ""
	}
	payload := map[string]any{
		"inline_keyboard": [][]map[string]string{{
			{"text": "👍", "callback_data": "u:" + strconv.FormatInt(listingID, 10)},
			{"text": "👎", "callback_data": "d:" + strconv.FormatInt(listingID, 10)},
		}},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func (n *Notifier) throttle(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.minDelay <= 0 {
		return nil
	}
	wait := n.minDelay - time.Since(n.lastSent)
	if wait <= 0 {
		n.lastSent = time.Now()
		return nil
	}
	if err := sleepWithContext(ctx, wait); err != nil {
		return err
	}
	n.lastSent = time.Now()
	return nil
}

func (n *Notifier) sendPhoto(ctx context.Context, chatID string, photo []byte, text, markup string) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, field := range []struct{ name, value string }{
		{"chat_id", chatID},
		{"caption", text},
	} {
		if err := addFormField(w, field.name, field.value); err != nil {
			return err
		}
	}
	if markup != "" {
		if err := addFormField(w, "reply_markup", markup); err != nil {
			return err
		}
	}
	fw, err := w.CreateFormFile("photo", "listing.jpg")
	if err != nil {
		return err
	}
	if _, err := fw.Write(photo); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/bot%s/sendPhoto", n.apiBase, n.token), &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	return n.post(req)
}

func (n *Notifier) sendMessage(ctx context.Context, chatID, text, markup string) error {
	form := url.Values{"chat_id": {chatID}, "text": {text}}
	if markup != "" {
		form.Set("reply_markup", markup)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/bot%s/sendMessage", n.apiBase, n.token), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return n.post(req)
}

// apiError is a non-OK answer from the Bot API.
type apiError struct {
	StatusCode  int
	Description string
	RetryAfter  int
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram: %d %s", e.StatusCode, e.Description)
}

type apiResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
	Result json.RawMessage `json:"result"`
}

// post sends req and decodes the API envelope. On a 429 it waits the requested
// retry_after and retries once, decoding the retry's answer too: the previous
// version returned only the transport error, so a second 429/400 looked like
// success and the caller recorded a listing as delivered that never was.
func (n *Notifier) post(req *http.Request) error {
	resp, status, err := n.do(req)
	if err != nil {
		return err
	}
	if resp.Parameters.RetryAfter > 0 {
		if err := sleepWithContext(req.Context(), time.Duration(resp.Parameters.RetryAfter)*time.Second); err != nil {
			return err
		}
		cloned := req.Clone(req.Context())
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return err
			}
			cloned.Body = body
		}
		return n.post(cloned)
	}
	if !resp.OK {
		return &apiError{StatusCode: status, Description: resp.Description}
	}
	return nil
}

func (n *Notifier) do(req *http.Request) (*apiResponse, int, error) {
	httpResp, err := n.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("telegram: %w", err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20))
	if err != nil {
		return nil, httpResp.StatusCode, fmt.Errorf("telegram: read response: %w", err)
	}

	var decoded apiResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, httpResp.StatusCode, fmt.Errorf("telegram: decode response (%d): %w", httpResp.StatusCode, err)
	}
	return &decoded, httpResp.StatusCode, nil
}

func addFormField(w *multipart.Writer, name, value string) error {
	fw, err := w.CreateFormField(name)
	if err != nil {
		return err
	}
	_, err = io.WriteString(fw, value)
	return err
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// truncateUTF16 cuts s to at most limit UTF-16 code units without splitting a
// rune, so a multi-byte character never produces an invalid message.
func truncateUTF16(s string, limit int) string {
	if utf16Len(s) <= limit {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		size := 1
		if r > 0xFFFF {
			size = 2
		}
		if used+size > limit {
			break
		}
		b.WriteRune(r)
		used += size
	}
	return strings.TrimSpace(b.String())
}
