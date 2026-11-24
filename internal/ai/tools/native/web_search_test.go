package native

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFormatSearchResults(t *testing.T) {
	items := []SearchResultItem{
		{
			Title:   "Judul Satu",
			URL:     "https://example.com/1",
			Content: "Ini adalah ringkasan konten satu.",
		},
		{
			Title:   "Judul Dua",
			URL:     "https://example.com/2",
			Content: "Ini adalah ringkasan konten dua.",
		},
	}

	out := formatSearchResults("Flora Gateway", "golang tutorial", items)
	if !strings.Contains(out, "Flora Gateway") {
		t.Errorf("Expected Flora Gateway in output, got %s", out)
	}
	if !strings.Contains(out, "Judul Satu") || !strings.Contains(out, "https://example.com/1") {
		t.Errorf("Expected item 1 in output, got %s", out)
	}
}

func TestSearchWebWithFallbackNoKeys(t *testing.T) {
	t.Setenv("FLORA_GATEWAY_URL", "")
	t.Setenv("TAVILY_API_KEY", "")
	t.Setenv("BRAVE_API_KEY", "")

	res, err := SearchWebWithFallback(context.Background(), "test")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !strings.Contains(res, "belum dikonfigurasi") {
		t.Errorf("Expected not configured message, got %s", res)
	}
}

func TestSearchFloraGatewayMock(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/search" {
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
			"data": [
				{
					"title": "Hasil Golang",
					"url": "https://go.dev",
					"snippet": "Go adalah bahasa pemrograman open source."
				}
			]
		}`))
	}))
	defer ts.Close()

	t.Setenv("FLORA_GATEWAY_URL", ts.URL)
	t.Setenv("FLORA_GATEWAY_KEY", "secret")

	res, err := SearchWebWithFallback(context.Background(), "golang")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !strings.Contains(res, "Flora Gateway") || !strings.Contains(res, "Hasil Golang") || !strings.Contains(res, "https://go.dev") {
		t.Errorf("Expected gateway result in output, got: %s", res)
	}
}
