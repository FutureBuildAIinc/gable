// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func openRouterAnswering(t *testing.T, status int) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"stand-in answer"}}`))
	}))
	defer srv.Close()
	c := NewClient("or-" + "test-key-not-real").WithBaseURL(srv.URL)
	return c.TestConnection(context.Background())
}

// Only a 401 is OpenRouter's refusal of the key: a 403 there means the
// model's moderation flagged the input, and the key is fine.
func TestOpenRouterRefusalIsA401Only(t *testing.T) {
	if err := openRouterAnswering(t, http.StatusUnauthorized); !errors.Is(err, ErrKeyRefused) {
		t.Errorf("401 = %v, want a refusal of the key", err)
	}
	if err := openRouterAnswering(t, http.StatusForbidden); err == nil || errors.Is(err, ErrKeyRefused) {
		t.Errorf("403 = %v, want an error that is not a refusal of the key", err)
	}
}

// Anthropic's 401 and 403 (permission_error) are both a key problem.
func TestAnthropicRefusalIs401And403(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"permission_error","message":"stand-in"}}`))
		}))
		c := NewClient("").WithProvider(ProviderAnthropic).WithAnthropicKey("test-" + "key-not-real").WithAnthropicBaseURL(srv.URL)
		err := c.TestConnection(context.Background())
		srv.Close()
		if !errors.Is(err, ErrKeyRefused) {
			t.Errorf("%d = %v, want a refusal of the key", status, err)
		}
	}
}

// A provider that cannot be reached is not a refusal and not a bare transport
// error: it is an answer in words with its own type.
func TestAnUnreachableProviderAnswersInWords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	c := NewClient("").WithProvider(ProviderAnthropic).WithAnthropicKey("test-" + "key-not-real").WithAnthropicBaseURL(url)
	err := c.TestConnection(context.Background())
	var u interface{ Words() string }
	if !errors.As(err, &u) {
		t.Fatalf("err = %v, want an unreachable answer carrying its words", err)
	}
	words := u.Words()
	if want := "The key is saved. Anthropic could not be reached to test it; press Test connection later."; words != want {
		t.Errorf("words = %q, want %q", words, want)
	}
}
