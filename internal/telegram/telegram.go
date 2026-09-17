package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"

	"zonapropbot/internal/config"
	"zonapropbot/internal/model"
)

// imageFetcher downloads image bytes for a photo. The fetch.Client satisfies it,
// giving image downloads the same TLS fingerprint / proxy behaviour as page
// fetches.
type imageFetcher interface {
	Fetch(ctx context.Context, u string) ([]byte, error)
}

const defaultMinSendDelay = 2500 * time.Millisecond

// Notifier sends listing alerts to a Telegram chat. Without credentials it runs
// in dry-run mode, printing alerts to out instead of the network.
type Notifier struct {
	token    string
	chatID   string
	apiBase  string
	client   *http.Client
	img      imageFetcher
	out      io.Writer
	mu       sync.Mutex
	lastSent time.Time
	minDelay time.Duration
}

// New builds a Notifier. img may be nil (then photos are skipped). out receives
// dry-run output and defaults to a silent sink when nil.
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

func (n *Notifier) dryRun() bool { return n.token == "" || n.chatID == "" }

// Notify delivers a listing alert. It downloads the photo (when available and
// an image fetcher is configured), then sends a photo message with a caption;
// if the image cannot be obtained it falls back to a text-only message.
func (n *Notifier) Notify(ctx context.Context, l model.Listing) error {
	caption := n.caption(l)

	if n.dryRun() {
		fmt.Fprintf(n.out, "DRY-RUN alert: %s\n", caption)
		if l.PhotoURL != "" {
			fmt.Fprintf(n.out, "  photo: %s\n", l.PhotoURL)
		}
		return nil
	}

	var photo []byte
	if l.PhotoURL != "" && n.img != nil {
		var err error
		photo, err = n.img.Fetch(ctx, l.PhotoURL)
		if err != nil {
			photo = nil
		}
	}

	if err := n.throttle(ctx); err != nil {
		return err
	}

	var body []byte
	var err error
	if len(photo) > 0 {
		body, err = n.sendPhoto(ctx, l, photo, caption)
	} else {
		body, err = n.sendMessage(ctx, l, caption)
	}
	if err != nil {
		return err
	}

	var resp struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("telegram: decode response: %w", err)
	}
	if !resp.OK {
		if resp.Parameters.RetryAfter > 0 {
			if err := sleepWithContext(ctx, time.Duration(resp.Parameters.RetryAfter)*time.Second); err != nil {
				return err
			}
			if len(photo) > 0 {
				_, err = n.sendPhoto(ctx, l, photo, caption)
			} else {
				_, err = n.sendMessage(ctx, l, caption)
			}
			return err
		}
		return fmt.Errorf("telegram: %s", resp.Description)
	}
	return nil
}

func (n *Notifier) caption(l model.Listing) string {
	var b strings.Builder
	b.WriteString(l.Title)
	b.WriteString("\n")
	if l.Price != "" {
		b.WriteString(l.Price)
		b.WriteString("\n")
	}
	line := strings.TrimSpace(strings.Join([]string{l.M2, l.Ambientes}, " · "))
	if line != "" {
		b.WriteString(line)
		b.WriteString("\n")
	}
	if l.Location != "" {
		b.WriteString(l.Location)
		b.WriteString("\n")
	}
	b.WriteString(l.URL)
	return b.String()
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

func (n *Notifier) sendPhoto(ctx context.Context, l model.Listing, photo []byte, caption string) ([]byte, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := addFormField(w, "chat_id", n.chatID); err != nil {
		return nil, err
	}
	if err := addFormField(w, "caption", caption); err != nil {
		return nil, err
	}
	fw, err := w.CreateFormFile("photo", "listing.jpg")
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(photo); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/bot%s/sendPhoto", n.apiBase, n.token), &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	return n.do(req)
}

func (n *Notifier) sendMessage(ctx context.Context, l model.Listing, text string) ([]byte, error) {
	form := fmt.Sprintf("chat_id=%s&text=%s", n.chatID, urlQueryEscape(text))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/bot%s/sendMessage", n.apiBase, n.token), strings.NewReader(form))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return n.do(req)
}

func (n *Notifier) do(req *http.Request) ([]byte, error) {
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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

func urlQueryEscape(s string) string {
	r := strings.NewReplacer(
		"%", "%25",
		"&", "%26",
		"=", "%3D",
		"+", "%2B",
		"\n", "%0A",
	)
	return r.Replace(s)
}
