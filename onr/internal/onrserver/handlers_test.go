package onrserver

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/r9s-ai/open-next-router/onr/internal/auth"
	"github.com/r9s-ai/open-next-router/pkg/config"
)

func TestInspectRequestBody_ImagesEditsMultipart(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("model", "gpt-image-1"); err != nil {
		t.Fatalf("WriteField model: %v", err)
	}
	if err := w.WriteField("prompt", "retouch"); err != nil {
		t.Fatalf("WriteField prompt: %v", err)
	}
	fw, err := w.CreateFormFile("image", "sample.png")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write([]byte("fake-image")); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rec := httptest.NewRecorder()
	gc, _ := gin.CreateTestContext(rec)
	gc.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	gc.Request.Header.Set("Content-Type", w.FormDataContentType())

	bodyBytes, stream, model, err := inspectRequestBody(gc, "images.edits")
	if err != nil {
		t.Fatalf("inspectRequestBody error: %v", err)
	}
	if len(bodyBytes) == 0 {
		t.Fatalf("expected body bytes")
	}
	if stream {
		t.Fatalf("expected stream=false")
	}
	if got, want := model, "gpt-image-1"; got != want {
		t.Fatalf("model=%q want=%q", got, want)
	}

	rootAny, ok := gc.Get(ctxKeyRequestRoot)
	if !ok {
		t.Fatalf("expected cached request root")
	}
	root, _ := rootAny.(map[string]any)
	if root == nil {
		t.Fatalf("expected parsed multipart root")
	}
	if got, want := root["model"], "gpt-image-1"; got != want {
		t.Fatalf("root model=%v want=%v", got, want)
	}
	if got, want := root["prompt"], "retouch"; got != want {
		t.Fatalf("root prompt=%v want=%v", got, want)
	}
}

func TestMakeHandlerRejectsDisallowedProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(auth.MiddlewareWithResolver("", func(context.Context, string) (auth.AuthPrincipal, bool, error) {
		return auth.AuthPrincipal{AccessKeyID: "key-a", SubjectType: "api_key", SubjectID: "key-a", AllowedProviders: []string{"anthropic"}}, true, nil
	}))
	r.POST("/v1/chat/completions", makeHandler(&config.Config{}, &state{}, nil, "chat.completions", "X-Onr-Request-Id", nil))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-4o-mini"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-onr-provider", "openai")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got := out["error"].(map[string]any)["code"]; got != "provider_not_allowed" {
		t.Fatalf("code=%v", got)
	}
}

func TestMakeGeminiHandlerRejectsDisallowedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(auth.MiddlewareWithResolver("", func(context.Context, string) (auth.AuthPrincipal, bool, error) {
		return auth.AuthPrincipal{AccessKeyID: "key-a", SubjectType: "api_key", SubjectID: "key-a", AllowedProviders: []string{"openai"}, AllowedModels: []string{"gpt-4o-mini"}}, true, nil
	}))
	r.POST("/v1beta/models/*path", makeGeminiHandler(&config.Config{}, &state{}, nil, "X-Onr-Request-Id", nil))

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", bytes.NewReader([]byte(`{"contents":[]}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-onr-provider", "openai")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got := out["error"].(map[string]any)["code"]; got != "model_not_allowed" {
		t.Fatalf("code=%v", got)
	}
}
