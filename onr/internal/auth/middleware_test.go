package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMiddlewareWithResolverUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(MiddlewareWithResolver("", func(context.Context, string) (AuthPrincipal, bool, error) {
		return AuthPrincipal{}, false, errors.New("redis down")
	}))
	r.GET("/ok", func(c *gin.Context) { c.String(200, "ok") })
	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.Header.Set("Authorization", "Bearer client-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestMiddlewareWithResolverPropagatesPrincipal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(MiddlewareWithResolver("", func(context.Context, string) (AuthPrincipal, bool, error) {
		return AuthPrincipal{
			AccessKeyID: "key-record",
			SubjectType: "account",
			SubjectID:   "acct-1",
		}, true, nil
	}))
	r.GET("/ok", func(c *gin.Context) {
		principal, ok := PrincipalFromContext(c)
		if !ok {
			c.String(http.StatusInternalServerError, "principal missing")
			return
		}
		if principal.AccessKeyID != "key-record" || principal.SubjectType != "account" || principal.SubjectID != "acct-1" {
			c.String(http.StatusInternalServerError, "principal=%+v", principal)
			return
		}
		if AccessKeyID(c) != "key-record" || SubjectType(c) != "account" || SubjectID(c) != "acct-1" {
			c.String(http.StatusInternalServerError, "getter mismatch")
			return
		}
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.Header.Set("Authorization", "Bearer ak-1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestMiddleware_TokenKey_AccessKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	match := func(accessKey string) (string, bool) {
		if accessKey == "ak-1" {
			return "client1", true
		}
		return "", false
	}
	r := gin.New()
	r.Use(Middleware("master", match))
	r.GET("/ok", func(c *gin.Context) {
		if got := c.GetString("onr.auth_subject_id"); got != "client1" {
			c.String(http.StatusInternalServerError, "subject=%s", got)
			return
		}
		if AccessKeyID(c) != "client1" || SubjectID(c) != "client1" {
			c.String(http.StatusInternalServerError, "legacy principal missing")
			return
		}
		c.String(200, "ok")
	})

	k64 := base64.RawURLEncoding.EncodeToString([]byte("ak-1"))
	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.Header.Set("Authorization", "Bearer onr:v1?k64="+k64+"&p=openai&m=gpt-4o-mini")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestMiddleware_TokenKey_UK64(t *testing.T) {
	gin.SetMode(gin.TestMode)

	match := func(accessKey string) (string, bool) {
		if accessKey == "ak-1" {
			return "client1", true
		}
		return "", false
	}
	r := gin.New()
	r.Use(Middleware("master", match))
	r.GET("/ok", func(c *gin.Context) {
		if TokenUpstreamKey(c) != "sk-upstream" {
			c.String(http.StatusInternalServerError, "bad upstream")
			return
		}
		if TokenModeFromContext(c) != TokenModeBYOK {
			c.String(http.StatusInternalServerError, "bad mode")
			return
		}
		c.String(200, "ok")
	})

	k64 := base64.RawURLEncoding.EncodeToString([]byte("ak-1"))
	uk64 := base64.RawURLEncoding.EncodeToString([]byte("sk-upstream"))
	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.Header.Set("Authorization", "Bearer onr:v1?k64="+k64+"&uk64="+uk64)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestMiddleware_AccessKey_WithoutMasterKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	match := func(accessKey string) (string, bool) {
		if accessKey == "ak-1" {
			return "client1", true
		}
		return "", false
	}
	r := gin.New()
	r.Use(Middleware("", match))
	r.GET("/ok", func(c *gin.Context) { c.String(200, "ok") })

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.Header.Set("Authorization", "Bearer ak-1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestMiddleware_TokenKey_BYOKWithoutK_DisabledByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(Middleware("", nil))
	r.GET("/ok", func(c *gin.Context) { c.String(200, "ok") })

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.Header.Set("Authorization", "Bearer onr:v1?uk=sk-upstream&p=openai")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestMiddleware_TokenKey_BYOKWithoutK_Enabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(Middleware("", nil, TokenKeyOptions{AllowBYOKWithoutK: true}))
	r.GET("/ok", func(c *gin.Context) {
		if TokenModeFromContext(c) != TokenModeBYOK {
			c.String(http.StatusInternalServerError, "bad mode")
			return
		}
		if TokenUpstreamKey(c) != "sk-upstream" {
			c.String(http.StatusInternalServerError, "bad upstream key")
			return
		}
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.Header.Set("Authorization", "Bearer onr:v1?uk=sk-upstream&p=openai")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}
