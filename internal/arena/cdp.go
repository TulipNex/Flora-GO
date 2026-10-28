package arena

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// cdp.go — client Chrome DevTools Protocol seminimal mungkin: cukup untuk
// membuka satu tab, menyuntikkan cookie, dan menjalankan satu skrip di
// halaman arena.ai.
//
// Kenapa CDP langsung, bukan chromedp/playwright: repo sudah memakai
// github.com/coder/websocket (dependensi tidak resmi? lihat go.mod — dipakai
// jalur WhatsApp), jadi menambah CDP mentah tidak menambah dependensi baru
// dan tidak membawa ~50 MB modul otomasi yang hanya dipakai satu command.

// cdpClient membungkus satu koneksi websocket ke target DevTools.
type cdpClient struct {
	conn *websocket.Conn

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan cdpResponse
	closed   bool
	closeErr error // sebab koneksi berakhir (untuk diagnostik)
}

// cdpResponse adalah balasan satu perintah CDP.
type cdpResponse struct {
	Result json.RawMessage
	Error  *cdpError
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// cdpEnvelope adalah bentuk mentah pesan CDP (respons maupun event).
type cdpEnvelope struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *cdpError       `json:"error"`
	Method string          `json:"method"`
}

// dialCDP membuka koneksi ke salah satu endpoint target Chrome.
func dialCDP(ctx context.Context, wsURL string) (*cdpClient, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{})
	if err != nil {
		return nil, fmt.Errorf("gagal konek ke DevTools: %w", err)
	}
	// Cap pesan masuk: hasil Runtime.evaluate bisa memuat teks stream yang
	// panjang, tapi 32MB jauh lebih dari cukup dan mencegah pesan rusak
	// menghabiskan memori panel.
	conn.SetReadLimit(32 << 20)
	c := &cdpClient{conn: conn, pending: make(map[int64]chan cdpResponse)}
	go c.readLoop()
	return c, nil
}

// readLoop meneruskan balasan ke pemanggil yang menunggu id-nya. Event
// (Method terisi, ID 0) diabaikan — kita tidak memakai Page.loadEventFired
// dan sejenisnya, cukup polling readyState.
func (c *cdpClient) readLoop() {
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			c.fail(fmt.Errorf("koneksi DevTools terputus: %w", err))
			return
		}
		var env cdpEnvelope
		if err := json.Unmarshal(data, &env); err != nil {
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

// fail menutup semua penunggu supaya tidak ada call yang menggantung
// selamanya saat Chrome mati di tengah jalan.
func (c *cdpClient) fail(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	// Disimpan, bukan dibuang: close code websocket membedakan "ditutup rapi
	// oleh bot" (1001, mis. idle-stop) dari "proses Chrome mati" (1006/OOM).
	// Tanpa ini pesan "DevTools terputus" tidak bisa didiagnosis.
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

// dead melaporkan koneksi sudah tidak bisa dipakai.
func (c *cdpClient) dead() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// reason mengembalikan sebab koneksi berakhir (string kosong kalau belum).
func (c *cdpClient) reason() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closeErr == nil {
		return ""
	}
	return c.closeErr.Error()
}

// call mengirim satu perintah CDP dan menunggu balasannya.
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

// Close menutup koneksi websocket (Chrome tetap hidup; mematikan prosesnya
// urusan browser.stop).
func (c *cdpClient) Close() {
	c.fail(fmt.Errorf("ditutup pemanggil"))
	_ = c.conn.Close(websocket.StatusNormalClosure, "")
}
