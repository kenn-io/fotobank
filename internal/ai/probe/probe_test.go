package probe_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai/probe"
)

func TestVisionProbeOKSendsMultimodalPayload(t *testing.T) {
	r := require.New(t)
	t.Setenv("VISION_KEY", "secret")
	var sawAuth bool
	var sawImage bool
	var sawPrompt bool
	var handlerErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		sawAuth = req.Header.Get("Authorization") == "Bearer secret"
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			handlerErr = err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body["model"] != "vision-model" {
			handlerErr = fmt.Errorf("model = %v", body["model"])
		}
		if body["max_tokens"] != float64(8) {
			handlerErr = fmt.Errorf("max_tokens = %v", body["max_tokens"])
		}
		if body["temperature"] != float64(0) {
			handlerErr = fmt.Errorf("temperature = %v", body["temperature"])
		}
		messages := body["messages"].([]any)
		content := messages[0].(map[string]any)["content"].([]any)
		for _, part := range content {
			p := part.(map[string]any)
			if p["type"] == "text" && p["text"] == "Reply with the single word 'ok'." {
				sawPrompt = true
			}
			if p["type"] == "image_url" {
				imageURL := p["image_url"].(map[string]any)["url"].(string)
				sawImage = strings.HasPrefix(imageURL, "data:image/jpeg;base64,")
			}
		}
		writeJSON(w, `{"model":"vision-model","choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	got := probe.Vision(context.Background(), probe.VisionConfig{
		Endpoint:  srv.URL + "/v1",
		Model:     "vision-model",
		APIKeyEnv: "VISION_KEY",
	})
	r.True(got.OK)
	r.NoError(handlerErr)
	r.Equal(probe.ClassOK, got.Classification)
	r.True(sawAuth)
	r.True(sawImage)
	r.True(sawPrompt)
	r.Empty(got.Warnings)
	r.NotNil(got.ModelEchoed)
	r.Equal("vision-model", *got.ModelEchoed)
}

func TestVisionProbeModelEchoWarningsAreSoft(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing", `{"choices":[{"message":{"content":"ok"}}]}`},
		{"different", `{"model":"other","choices":[{"message":{"content":"ok"}}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := require.New(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				writeJSON(w, tt.body)
			}))
			defer srv.Close()

			got := probe.Vision(context.Background(), probe.VisionConfig{
				Endpoint: srv.URL + "/v1",
				Model:    "vision-model",
			})
			r.True(got.OK)
			r.Equal(probe.ClassOK, got.Classification)
			r.NotEmpty(got.Warnings)
		})
	}
}

func TestEmbedProbeOKValidatesDimensionAndOmitsAuthWhenEnvEmpty(t *testing.T) {
	r := require.New(t)
	var sawAuth string
	var handlerErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		sawAuth = req.Header.Get("Authorization")
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			handlerErr = err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body["input"] != "test" {
			handlerErr = fmt.Errorf("input = %v", body["input"])
		}
		if body["model"] != "embed-model" {
			handlerErr = fmt.Errorf("model = %v", body["model"])
		}
		writeEmbed(w, 3)
	}))
	defer srv.Close()

	got := probe.Embed(context.Background(), probe.EmbedConfig{
		Endpoint:  srv.URL + "/v1",
		Model:     "embed-model",
		Dimension: 3,
	})
	r.True(got.OK)
	r.NoError(handlerErr)
	r.Equal(probe.ClassOK, got.Classification)
	r.Empty(sawAuth)
}

func TestProbeClassifications(t *testing.T) {
	tests := []struct {
		name    string
		section string
		handler http.HandlerFunc
		want    probe.Classification
	}{
		{
			name:    "auth failed status",
			section: "vision",
			handler: func(w http.ResponseWriter, req *http.Request) { http.Error(w, "no", http.StatusUnauthorized) },
			want:    probe.ClassAuthFailed,
		},
		{
			name:    "provider error",
			section: "vision",
			handler: func(w http.ResponseWriter, req *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) },
			want:    probe.ClassProviderError,
		},
		{
			name:    "model mismatch",
			section: "vision",
			handler: func(w http.ResponseWriter, req *http.Request) { http.Error(w, "model not found", http.StatusNotFound) },
			want:    probe.ClassModelMismatch,
		},
		{
			name:    "malformed vision",
			section: "vision",
			handler: func(w http.ResponseWriter, req *http.Request) { writeJSON(w, `{"choices":[]}`) },
			want:    probe.ClassMalformedResponse,
		},
		{
			name:    "dimension mismatch",
			section: "embed",
			handler: func(w http.ResponseWriter, req *http.Request) { writeEmbed(w, 2) },
			want:    probe.ClassDimensionMismatch,
		},
		{
			name:    "malformed embed",
			section: "embed",
			handler: func(w http.ResponseWriter, req *http.Request) { writeJSON(w, `{"data":[]}`) },
			want:    probe.ClassMalformedResponse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := require.New(t)
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()
			var got probe.Result
			if tt.section == "vision" {
				got = probe.Vision(context.Background(), probe.VisionConfig{Endpoint: srv.URL + "/v1", Model: "m"})
			} else {
				got = probe.Embed(context.Background(), probe.EmbedConfig{Endpoint: srv.URL + "/v1", Model: "m", Dimension: 3})
			}
			r.False(got.OK)
			r.Equal(tt.want, got.Classification)
			r.NotEmpty(got.Detail)
		})
	}
}

func TestProbeUnsetAPIKeyEnvFailsWithoutRequest(t *testing.T) {
	r := require.New(t)
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		called = true
	}))
	defer srv.Close()

	got := probe.Embed(context.Background(), probe.EmbedConfig{
		Endpoint:  srv.URL + "/v1",
		Model:     "m",
		Dimension: 3,
		APIKeyEnv: "MISSING_KEY",
	})
	r.False(got.OK)
	r.Equal(probe.ClassAuthFailed, got.Classification)
	r.False(called)
	r.Contains(got.Detail, "$MISSING_KEY")
}

func TestProbeUnreachableAndTimeout(t *testing.T) {
	r := require.New(t)
	unreachable := probe.Embed(context.Background(), probe.EmbedConfig{
		Endpoint:  "http://127.0.0.1:1/v1",
		Model:     "m",
		Dimension: 3,
	})
	r.False(unreachable.OK)
	r.Equal(probe.ClassUnreachable, unreachable.Classification)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	timeout := probe.Vision(ctx, probe.VisionConfig{Endpoint: srv.URL + "/v1", Model: "m"})
	r.False(timeout.OK)
	r.Equal(probe.ClassTimeout, timeout.Classification)
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

func writeEmbed(w http.ResponseWriter, dim int) {
	vals := make([]string, dim)
	for i := range vals {
		vals[i] = "0.1"
	}
	writeJSON(w, `{"model":"embed-model","data":[{"index":0,"embedding":[`+strings.Join(vals, ",")+`]}]}`)
}
