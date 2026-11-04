package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/cdmx-in/kera/internal/ids"
)

// Jupyter talks to a Jupyter Server: REST for kernels, the channels
// websocket (default JSON protocol) for execution.
type Jupyter struct {
	URL   string // e.g. http://kera-jupyter:8888
	Token string
	HTTP  *http.Client
}

// Output is what one cell produced.
type Output struct {
	Stdout, Stderr string
	Result         string   // text/plain of execute_result and display_data
	Images         [][]byte // PNG/JPEG from execute_result and display_data
	ImageTypes     []string
	Error          string // "ename: evalue" plus traceback, ANSI stripped
	TimedOut       bool
}

// Kernel is a started kernel with an open channels socket.
type Kernel struct {
	j       *Jupyter
	id      string
	session string
	conn    *websocket.Conn
}

func (j *Jupyter) client() *http.Client {
	if j.HTTP != nil {
		return j.HTTP
	}
	return http.DefaultClient
}

func (j *Jupyter) do(ctx context.Context, method, path string, body any, dst any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(j.URL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if j.Token != "" {
		req.Header.Set("Authorization", "token "+j.Token)
	}
	resp, err := j.client().Do(req)
	if err != nil {
		return fmt.Errorf("jupyter %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("jupyter %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if dst != nil {
		return json.NewDecoder(resp.Body).Decode(dst)
	}
	return nil
}

// Start creates a kernel and connects its channels. Close always deletes it.
func (j *Jupyter) Start(ctx context.Context) (*Kernel, error) {
	var created struct {
		ID string `json:"id"`
	}
	if err := j.do(ctx, http.MethodPost, "/api/kernels", map[string]any{}, &created); err != nil {
		return nil, err
	}
	if created.ID == "" {
		return nil, errors.New("jupyter: kernel id missing in response")
	}
	k := &Kernel{j: j, id: created.ID, session: ids.New()}
	u, err := url.Parse(strings.TrimRight(j.URL, "/") + "/api/kernels/" + url.PathEscape(created.ID) + "/channels")
	if err != nil {
		k.Close()
		return nil, err
	}
	u.Scheme = map[string]string{"https": "wss"}[u.Scheme]
	if u.Scheme == "" {
		u.Scheme = "ws"
	}
	u.RawQuery = url.Values{"session_id": {k.session}}.Encode()
	h := http.Header{}
	if j.Token != "" {
		h.Set("Authorization", "token "+j.Token)
	}
	conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPHeader: h, HTTPClient: j.client()})
	if err != nil {
		k.Close()
		return nil, fmt.Errorf("jupyter channels: %w", err)
	}
	conn.SetReadLimit(64 << 20) // base64 images
	k.conn = conn
	return k, nil
}

// Close closes the socket and deletes the kernel (best effort).
func (k *Kernel) Close() {
	if k.conn != nil {
		_ = k.conn.Close(websocket.StatusNormalClosure, "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = k.j.do(ctx, http.MethodDelete, "/api/kernels/"+url.PathEscape(k.id), nil, nil)
}

type msgHeader struct {
	MsgID    string `json:"msg_id"`
	MsgType  string `json:"msg_type"`
	Username string `json:"username"`
	Session  string `json:"session"`
	Date     string `json:"date"`
	Version  string `json:"version"`
}

type message struct {
	Header       msgHeader       `json:"header"`
	ParentHeader msgHeader       `json:"parent_header"`
	Metadata     map[string]any  `json:"metadata"`
	Content      json.RawMessage `json:"content"`
	Channel      string          `json:"channel"`
	Buffers      []any           `json:"buffers"`
}

// Exec runs code and collects its output until the kernel is idle again or
// timeout passes (then Output.TimedOut is set and partial output returned).
func (k *Kernel) Exec(ctx context.Context, code string, timeout time.Duration) (Output, error) {
	id := ids.New()
	content, _ := json.Marshal(map[string]any{"code": code, "silent": false, "store_history": false,
		"user_expressions": map[string]any{}, "allow_stdin": false, "stop_on_error": true})
	req := message{
		Header: msgHeader{MsgID: id, MsgType: "execute_request", Username: "kera", Session: k.session,
			Date: time.Now().UTC().Format(time.RFC3339Nano), Version: "5.3"},
		Metadata: map[string]any{}, Content: content, Channel: "shell", Buffers: []any{},
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := wsjson.Write(ctx, k.conn, req); err != nil {
		return Output{}, fmt.Errorf("jupyter execute: %w", err)
	}
	var out Output
	for {
		var m message
		if err := wsjson.Read(ctx, k.conn, &m); err != nil {
			if ctx.Err() != nil {
				out.TimedOut = true
				return out, nil
			}
			return out, fmt.Errorf("jupyter read: %w", err)
		}
		if m.ParentHeader.MsgID != id || (m.Channel != "" && m.Channel != "iopub") {
			continue
		}
		if done := out.add(m.Header.MsgType, m.Content); done {
			return out, nil
		}
	}
}

// add folds one iopub message into out; it reports true on status idle.
func (o *Output) add(kind string, raw json.RawMessage) bool {
	var c struct {
		Name           string         `json:"name"`
		Text           string         `json:"text"`
		Data           map[string]any `json:"data"`
		ExecutionState string         `json:"execution_state"`
		Ename          string         `json:"ename"`
		Evalue         string         `json:"evalue"`
		Traceback      []string       `json:"traceback"`
	}
	_ = json.Unmarshal(raw, &c)
	switch kind {
	case "stream":
		if c.Name == "stderr" {
			o.Stderr = keep(o.Stderr, c.Text)
		} else {
			o.Stdout = keep(o.Stdout, c.Text)
		}
	case "execute_result", "display_data":
		o.addData(c.Data)
	case "error":
		o.Error = stripANSI(c.Ename + ": " + c.Evalue + "\n" + strings.Join(c.Traceback, "\n"))
	case "status":
		return c.ExecutionState == "idle"
	}
	return false
}

func (o *Output) addData(data map[string]any) {
	for _, mt := range []string{"image/png", "image/jpeg"} {
		if s, ok := data[mt].(string); ok {
			if len(o.Images) >= maxHarvest {
				return // a plotting loop must not flood the user's files
			}
			if b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(s, "\n", "")); err == nil {
				o.Images = append(o.Images, b)
				o.ImageTypes = append(o.ImageTypes, mt)
				return // the image stands for this output; its text/plain is "<Figure ...>"
			}
		}
	}
	if s, ok := data["text/plain"].(string); ok {
		if o.Result != "" {
			s = "\n" + s
		}
		o.Result = keep(o.Result, s)
	}
}

// keep appends add to s but drops the middle once s grows past a few times
// what clip sends to the model, so a print loop cannot fill Kera's memory.
func keep(s, add string) string {
	s += add
	if len(s) > 4*maxOutputChars {
		s = s[:maxOutputChars] + s[len(s)-maxOutputChars:]
	}
	return s
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
