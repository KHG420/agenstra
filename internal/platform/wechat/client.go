package wechat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Origin is the default authenticated iLink API origin.
const Origin = "https://ilinkai.weixin.qq.com"

// Version is the iLink channel protocol version sent by this plugin.
const Version = "2.4.10"

var hostPattern = regexp.MustCompile(`^ilinkai[0-9]*\.weixin\.qq\.com$`)

// The protocol has used both JSON numbers and strings for message identities.
// Keep either representation exact; never pass through a floating-point value.
// ID preserves numeric and string platform message identities exactly.
type ID string

// UnmarshalJSON accepts a bounded platform number or string identity.
func (id *ID) UnmarshalJSON(raw []byte) error {
	var text string
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
	} else {
		var value json.Number
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		text = value.String()
	}
	if len(text) > 256 {
		return errors.New("wechat_message_invalid")
	}
	*id = ID(text)
	return nil
}

// String returns the original identity without numeric conversion.
func (id ID) String() string { return string(id) }

// Plugin implements WeChat QR login, polling, typing and native message delivery.
// Configure its client before concurrent use and close it after callers stop.
type Plugin struct {
	client    *http.Client
	transport *http.Transport
}

// SendError records an explicit platform rejection, including its protocol codes.
type SendError struct {
	Ret, Code int
}

// Error returns the stable delivery error code without upstream private data.
func (*SendError) Error() string { return "wechat_send_failed" }

// Credentials binds one bot to one peer. Keep it in trusted encrypted host storage.
type Credentials struct {
	QR        string `json:"qr,omitempty"`
	QRContent string `json:"qr_content,omitempty"`
	Token     string `json:"token,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	BotID     string `json:"bot_id,omitempty"`
	PeerID    string `json:"peer_id,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
}

// Login contains QR login state and credentials returned after confirmation.
type Login struct {
	Ret          int    `json:"ret"`
	ErrCode      int    `json:"errcode"`
	Status       string `json:"status"`
	Token        string `json:"bot_token"`
	BotID        string `json:"ilink_bot_id"`
	PeerID       string `json:"ilink_user_id"`
	BaseURL      string `json:"baseurl"`
	RedirectHost string `json:"redirect_host"`
}

// Item contains incoming text or a platform-provided voice transcription.
type Item struct {
	Type int `json:"type"`
	Text struct {
		Text string `json:"text"`
	} `json:"text_item"`
	Voice struct {
		Text string `json:"text"`
	} `json:"voice_item"`
}

// Incoming is one platform message with its reply context and exact identity.
type Incoming struct {
	MessageID    ID     `json:"message_id"`
	ClientID     string `json:"client_id"`
	From         string `json:"from_user_id"`
	To           string `json:"to_user_id"`
	GroupID      string `json:"group_id"`
	Type         int    `json:"message_type"`
	State        int    `json:"message_state"`
	ContextToken string `json:"context_token"`
	Items        []Item `json:"item_list"`
}

// Updates contains a bounded batch and the cursor to persist after processing.
type Updates struct {
	Ret      int        `json:"ret"`
	ErrCode  int        `json:"errcode"`
	Messages []Incoming `json:"msgs"`
	Cursor   string     `json:"get_updates_buf"`
}

// New creates a protocol plugin with bounded requests and redirect protection.
// A supplied HTTP client remains caller-owned and is never modified.
func New(client *http.Client) *Plugin {
	if client != nil {
		return &Plugin{client: client}
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.MaxConnsPerHost, t.MaxIdleConnsPerHost, t.MaxIdleConns = 72, 8, 16
	t.ResponseHeaderTimeout, t.IdleConnTimeout = 40*time.Second, 45*time.Second
	return &Plugin{transport: t, client: &http.Client{Transport: t, Timeout: 42 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Hosts are constrained before credentials are attached, including platform redirects.
// ValidateBaseURL accepts the platform HTTPS API hosts before attaching credentials.
func ValidateBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("wechat_host_invalid")
	}
	h := strings.ToLower(u.Hostname())
	if !hostPattern.MatchString(h) {
		return "", errors.New("wechat_host_invalid")
	}
	return "https://" + h, nil
}

func (a *Plugin) request(ctx context.Context, base, path, token string, body any, out any) error {
	if len(token) > 16384 {
		return errors.New("wechat_request_invalid")
	}
	if _, err := ValidateBaseURL(base); err != nil {
		return err
	}
	var input io.Reader
	method := http.MethodGet
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil || len(raw) > 128<<10 {
			return errors.New("wechat_request_invalid")
		}
		input, method = bytes.NewReader(raw), http.MethodPost
	}
	r, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, input)
	if err != nil {
		return errors.New("wechat_request_invalid")
	}
	r.Header.Set("iLink-App-Id", "bot")
	r.Header.Set("iLink-App-ClientVersion", strconv.Itoa(2<<16|4<<8|10))
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("AuthorizationType", "ilink_bot_token")
		var random [4]byte
		if _, err = rand.Read(random[:]); err != nil {
			return err
		}
		uin := strconv.FormatUint(uint64(binary.BigEndian.Uint32(random[:])), 10)
		r.Header.Set("X-WECHAT-UIN", base64.StdEncoding.EncodeToString([]byte(uin)))
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := a.do(r)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("wechat_unavailable")
	} // Never propagate URLs containing QR/PIN.
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	closeErr := resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return errors.New("wechat_unavailable")
	}
	if readErr != nil || closeErr != nil || len(raw) > 2<<20 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("wechat_response_invalid")
	}
	// sendtyping is a notification. The platform also acknowledges it with an
	// empty successful body, while other endpoints require their JSON result.
	if path == "/ilink/bot/sendtyping" && len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("wechat_response_invalid")
	}
	return nil
}

func baseInfo() map[string]string {
	return map[string]string{"channel_version": Version, "bot_agent": "Agenstra/0.1"}
}

// QRCode starts a login and returns data for the host QR binding view.
func (a *Plugin) QRCode(ctx context.Context) (Credentials, error) {
	var result struct {
		Ret     int    `json:"ret"`
		QR      string `json:"qrcode"`
		Content string `json:"qrcode_img_content"`
	}
	err := a.request(ctx, Origin, "/ilink/bot/get_bot_qrcode?bot_type=3", "", map[string]any{"local_token_list": []string{}}, &result)
	if err != nil {
		return Credentials{}, err
	}
	if result.Ret != 0 || result.QR == "" || len(result.QR) > 512 || result.Content == "" || len(result.Content) > 271 {
		return Credentials{}, errors.New("wechat_qr_unavailable")
	}
	return Credentials{QR: result.QR, QRContent: result.Content, BaseURL: Origin}, nil
}

// LoginStatus polls an existing QR login, optionally with a submitted verification code.
func (a *Plugin) LoginStatus(ctx context.Context, c Credentials, pin string) (Login, error) {
	query := url.Values{"qrcode": {c.QR}}
	if pin != "" {
		query.Set("verify_code", pin)
	}
	var result Login
	err := a.request(ctx, c.BaseURL, "/ilink/bot/get_qrcode_status?"+query.Encode(), "", nil, &result)
	if err == nil && (result.Ret != 0 || result.ErrCode != 0) {
		err = errors.New("wechat_login_failed")
	}
	return result, err
}

// PollUpdates performs one cancellable long poll using the saved cursor.
func (a *Plugin) PollUpdates(ctx context.Context, c Credentials) (Updates, error) {
	var result Updates
	err := a.request(ctx, c.BaseURL, "/ilink/bot/getupdates", c.Token, map[string]any{"get_updates_buf": c.Cursor, "base_info": baseInfo()}, &result)
	if err == nil && (result.Ret == -14 || result.ErrCode == -14) {
		return result, errors.New("wechat_session_expired")
	}
	if err == nil && (result.Ret != 0 || result.ErrCode != 0) {
		err = errors.New("wechat_poll_failed")
	}
	if len(result.Messages) > 256 || len(result.Cursor) > 64<<10 {
		return result, errors.New("wechat_response_invalid")
	}
	return result, err
}

// SendText sends one bounded text message using the supplied stable client identity.
// Transport failures leave the remote outcome uncertain.
func (a *Plugin) SendText(ctx context.Context, c Credentials, id, contextToken, text string) error {
	if text == "" || len(text) > 2000 || !utf8.ValidString(text) {
		return errors.New("wechat_text_invalid")
	}
	return a.sendItems(ctx, c, id, contextToken, []any{map[string]any{"type": 1, "text_item": map[string]string{"text": text}}})
}

func (a *Plugin) sendItems(ctx context.Context, c Credentials, id, contextToken string, items []any) error {
	if id == "" || len(id) > 256 || len(contextToken) > 16384 || c.PeerID == "" || len(c.PeerID) > 256 {
		return errors.New("wechat_request_invalid")
	}
	var result struct {
		Ret     int `json:"ret"`
		ErrCode int `json:"errcode"`
	}
	msg := map[string]any{"from_user_id": "", "to_user_id": c.PeerID, "client_id": id, "message_type": 2, "message_state": 2, "context_token": contextToken,
		"item_list": items}
	if err := a.request(ctx, c.BaseURL, "/ilink/bot/sendmessage", c.Token, map[string]any{"msg": msg, "base_info": baseInfo()}, &result); err != nil {
		return err
	}
	if result.Ret == -14 || result.ErrCode == -14 {
		return errors.New("wechat_session_expired")
	}
	if result.Ret != 0 || result.ErrCode != 0 {
		return &SendError{Ret: result.Ret, Code: result.ErrCode}
	}
	return nil
}

// TypingTicket reads the typing configuration for the bound peer and reply context.
func (a *Plugin) TypingTicket(ctx context.Context, c Credentials, contextToken string) (string, error) {
	var result struct {
		Ret    int    `json:"ret"`
		Code   int    `json:"errcode"`
		Ticket string `json:"typing_ticket"`
	}
	err := a.request(ctx, c.BaseURL, "/ilink/bot/getconfig", c.Token, map[string]any{
		"ilink_user_id": c.PeerID, "context_token": contextToken, "base_info": baseInfo(),
	}, &result)
	if err != nil {
		return "", err
	}
	if result.Ret == -14 || result.Code == -14 {
		return "", errors.New("wechat_session_expired")
	}
	if result.Ret != 0 || result.Code != 0 || result.Ticket == "" || len(result.Ticket) > 16384 {
		return "", errors.New("wechat_typing_unavailable")
	}
	return result.Ticket, nil
}

// Typing sends or clears the peer typing state without changing message delivery.
func (a *Plugin) Typing(ctx context.Context, c Credentials, ticket string, active bool) error {
	status := 2
	if active {
		status = 1
	}
	var result struct {
		Ret  int `json:"ret"`
		Code int `json:"errcode"`
	}
	if err := a.request(ctx, c.BaseURL, "/ilink/bot/sendtyping", c.Token, map[string]any{
		"ilink_user_id": c.PeerID, "typing_ticket": ticket, "status": status, "base_info": baseInfo(),
	}, &result); err != nil {
		return err
	}
	if result.Ret == -14 || result.Code == -14 {
		return errors.New("wechat_session_expired")
	}
	if result.Ret != 0 || result.Code != 0 {
		return errors.New("wechat_typing_unavailable")
	}
	return nil
}

// Close releases only the idle connections created by New with a nil client.
func (a *Plugin) Close() {
	if a.transport != nil {
		a.transport.CloseIdleConnections()
	}
}

func (a *Plugin) do(request *http.Request) (*http.Response, error) {
	client := *a.client
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Timeout <= 0 || client.Timeout > 42*time.Second {
		client.Timeout = 42 * time.Second
	}
	return client.Do(request)
}
