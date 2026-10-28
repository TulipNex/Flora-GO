package reactionwa

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// cdp.go — client Chrome DevTools Protocol seminimal mungkin: cukup untuk
// membuka satu tab /Free/ dan menjalankan satu skrip untuk memanen token
// Turnstile. Pola ini sama dengan internal/arena (tidak menambah dependensi
// otomasi browser baru).

type cdpClient struct {
	conn *websocket.Conn

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan cdpResponse
	closed   bool
	closeErr error
}

type cdpResponse struct {
	Result json.RawMessage
	Error  *cdpError
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cdpEnvelope struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *cdpError       `json:"error"`
	Method string          `json:"method"`
}

func dialCDP(ctx context.Context, wsURL string) (*cdpClient, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: gagal konek ke DevTools: %v", ErrBrowser, err)
	}
	conn.SetReadLimit(8 << 20)
	c := &cdpClient{conn: conn, pending: make(map[int64]chan cdpResponse)}
	go c.readLoop()
	return c, nil
}

func (c *cdpClient) readLoop() {
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			c.fail(fmt.Errorf("koneksi DevTools terputus: %w", err))
			return
		}
		var env cdpEnvelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		if env.ID == 0 {
			continue
		}
		c.mu.Lock()
		ch := c.pending[env.ID]
		delete(c.pending, env.ID)
		c.mu.Unlock()
		if ch == nil {
			continue
		}
		ch <- cdpResponse{Result: env.Result, Error: env.Error}
	}
}

func (c *cdpClient) fail(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if err != nil {
		c.closeErr = err
	}
	pending := c.pending
	c.pending = make(map[int64]chan cdpResponse)
	c.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
}

func (c *cdpClient) reason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closeErr == nil {
		return ""
	}
	return c.closeErr.Error()
}

func (c *cdpClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("DevTools sudah tertutup")
	}
	c.nextID++
	id := c.nextID
	ch := make(chan cdpResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	payload := map[string]any{"id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("gagal encode %s: %w", method, err)
	}
	writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := c.conn.Write(writeCtx, websocket.MessageText, raw); err != nil {
		return nil, fmt.Errorf("gagal kirim %s: %w", method, err)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			if reason := c.reason(); reason != "" {
				return nil, fmt.Errorf("DevTools terputus saat %s (%s)", method, reason)
			}
			return nil, fmt.Errorf("DevTools terputus saat %s", method)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("CDP %s: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (c *cdpClient) close() {
	c.fail(fmt.Errorf("ditutup pemanggil"))
	_ = c.conn.Close(websocket.StatusNormalClosure, "")
}
