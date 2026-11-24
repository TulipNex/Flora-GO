package native

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadWebPageNoGateway(t *testing.T) {
	t.Setenv("FLORA_GATEWAY_URL", "")
	res, err := ReadWebPage(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !strings.Contains(res, "belum dikonfigurasi") {
		t.Errorf("Expected not configured message, got %s", res)
	}
}

func TestReadWebPageMock(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/reader" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-API-Key") != "secret" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"success": true,
			"data": {
				"title": "Judul Artikel Keren",
				"byline": "Penulis Handal",
				"content": "Ini paragraf artikel yang sangat informatif dalam format Markdown."
			}
		}`))
	}))
	defer ts.Close()

	t.Setenv("FLORA_GATEWAY_URL", ts.URL)
	t.Setenv("FLORA_GATEWAY_KEY", "secret")

	res, err := ReadWebPage(context.Background(), "https://example.com/test")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !strings.Contains(res, "# Judul Artikel Keren") {
		t.Errorf("Expected title, got: %s", res)
	}
	if !strings.Contains(res, "Penulis Handal") {
		t.Errorf("Expected byline, got: %s", res)
	}
	if !strings.Contains(res, "paragraf artikel") {
		t.Errorf("Expected content, got: %s", res)
	}
}
