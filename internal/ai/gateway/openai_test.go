package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai/gateway"
)

func TestOpenAI_GenerateSuccess(t *testing.T) {
	require := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		var body struct {
			Model    string           `json:"model"`
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.Model != "qwen2.5-vl:3b" || len(body.Messages) != 1 {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}

		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"role": "assistant", "content": `{"tags":["dog","beach"]}`},
			}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := gateway.NewOpenAICompatible(gateway.OpenAIConfig{
		Endpoint:   srv.URL + "/v1",
		APIKey:     "secret",
		Timeout:    2 * time.Second,
		MaxRetries: 1,
	})
	got, err := c.Generate(context.Background(), gateway.Request{
		Model:  "qwen2.5-vl:3b",
		Prompt: "describe the photo",
		JPEG:   []byte{0xff, 0xd8, 0xff, 0xd9},
	})
	require.NoError(err)
	require.JSONEq(`{"tags":["dog","beach"]}`, got.Text)
}

func TestOpenAI_RetriesOn5xxThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"role": "assistant", "content": "ok"},
			}},
		})
	}))
	defer srv.Close()

	c := gateway.NewOpenAICompatible(gateway.OpenAIConfig{
		Endpoint:    srv.URL + "/v1",
		Timeout:     2 * time.Second,
		MaxRetries:  2,
		BackoffBase: 1 * time.Millisecond, // fast for tests
		BackoffCap:  5 * time.Millisecond,
	})
	got, err := c.Generate(context.Background(), gateway.Request{Model: "m", Prompt: "p", JPEG: []byte{0xff}})
	require.NoError(t, err)
	require.Equal(t, "ok", got.Text)
	require.EqualValues(t, 2, attempts.Load())
}

func TestOpenAI_4xxFailsImmediately(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, `{"error":"image too large"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	c := gateway.NewOpenAICompatible(gateway.OpenAIConfig{
		Endpoint: srv.URL + "/v1", Timeout: 2 * time.Second, MaxRetries: 3,
	})
	_, err := c.Generate(context.Background(), gateway.Request{Model: "m", Prompt: "p", JPEG: []byte{0xff}})
	require.Error(t, err)
	require.ErrorIs(t, err, gateway.ErrPermanent4xx)
	require.EqualValues(t, 1, attempts.Load(), "no retries on 4xx")
}

func TestOpenAI_429HonorsRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
		})
	}))
	defer srv.Close()

	c := gateway.NewOpenAICompatible(gateway.OpenAIConfig{
		Endpoint: srv.URL + "/v1", Timeout: 2 * time.Second, MaxRetries: 2,
		BackoffBase: 1 * time.Millisecond, BackoffCap: 5 * time.Millisecond,
	})
	got, err := c.Generate(context.Background(), gateway.Request{Model: "m", Prompt: "p", JPEG: []byte{0xff}})
	require.NoError(t, err)
	require.Equal(t, "ok", got.Text)
	require.EqualValues(t, 2, attempts.Load())
}

func TestOpenAI_HealthCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen2.5-vl:3b"}]}`))
	}))
	defer srv.Close()

	c := gateway.NewOpenAICompatible(gateway.OpenAIConfig{Endpoint: srv.URL + "/v1", Timeout: 2 * time.Second})
	require.NoError(t, c.HealthCheck(context.Background()))
}
