package ai

import (
	"testing"
	"time"
)

// TestRequestTimeoutFromEnv memastikan AI_REQUEST_TIMEOUT dibaca dengan benar
// dan nilai kosong/rusak jatuh ke default (bukan 0 = tanpa batas).
func TestRequestTimeoutFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", defaultRequestTimeout},
		{"45s", 45 * time.Second},
		{"3m", 3 * time.Minute},
		{"ngawur", defaultRequestTimeout},
		{"-5s", defaultRequestTimeout},
		{"0s", defaultRequestTimeout},
	}
	for _, c := range cases {
		t.Setenv("AI_REQUEST_TIMEOUT", c.env)
		if got := requestTimeout(); got != c.want {
			t.Errorf("AI_REQUEST_TIMEOUT=%q: dapat %v, mau %v", c.env, got, c.want)
		}
	}
}
