package onrserver

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/r9s-ai/open-next-router/onr-core/pkg/dslconfig"
	"github.com/r9s-ai/open-next-router/onr-core/pkg/requestid"
	"github.com/r9s-ai/open-next-router/onr-core/pkg/trafficdump"
	"github.com/r9s-ai/open-next-router/onr/internal/auth"
	"github.com/r9s-ai/open-next-router/onr/internal/logx"
	"github.com/r9s-ai/open-next-router/onr/internal/proxy"
	"github.com/r9s-ai/open-next-router/pkg/billing"
	"github.com/r9s-ai/open-next-router/pkg/config"
)

// NewRouter returns a non-nil Gin engine.
func NewRouter(
	cfg *config.Config,
	st *state,
	reg *dslconfig.Registry,
	pclient *proxy.Client,
	accessLogger *log.Logger,
	accessLoggerColor bool,
	requestIDHeaderKey string,
	accessFormatter *logx.AccessLogFormatter,
	billingClients ...*billing.Ledger,
) *gin.Engine {
	var billingClient *billing.Ledger
	if len(billingClients) > 0 {
		billingClient = billingClients[0]
	}
	resolvedRequestIDHeaderKey := requestid.ResolveHeaderKey(requestIDHeaderKey)
	r := gin.New()
	r.Use(requestIDMiddleware(resolvedRequestIDHeaderKey))
	if cfg.Logging.AccessLog {
		r.Use(requestLoggerWithColor(
			accessLogger,
			accessLoggerColor,
			resolvedRequestIDHeaderKey,
			cfg.Logging.AppNameInfer.Enabled,
			cfg.Logging.AppNameInfer.Unknown,
			accessFormatter,
		))
	}
	r.Use(gin.Recovery())
	if cfg.TrafficDump.Enabled {
		r.Use(trafficDumpMiddleware(cfg, resolvedRequestIDHeaderKey))
	}

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	r.GET("/readyz", func(c *gin.Context) {
		checks := gin.H{"redis": "disabled", "billing": "disabled"}
		status := http.StatusOK
		if st.redis != nil {
			if err := st.redis.Ping(c.Request.Context()); err != nil {
				checks["redis"] = "unavailable"
				status = http.StatusServiceUnavailable
			} else {
				checks["redis"] = "ok"
			}
		}
		if st.redis != nil && billingClient != nil && billingClient.Enabled() {
			checks["billing"] = "ok"
			pending, deadLetter, err := st.redis.BillingStats(c.Request.Context())
			if err != nil {
				checks["billing"] = "unavailable"
				status = http.StatusServiceUnavailable
			} else {
				checks["billing"] = gin.H{"pending": pending, "dead_letter": deadLetter}
			}
		}
		c.JSON(status, gin.H{"ok": status == http.StatusOK, "checks": checks})
	})

	secured := r.Group("/")
	secured.Use(auth.MiddlewareWithResolver(
		cfg.Auth.APIKey,
		func(ctx context.Context, accessKey string) (auth.AuthPrincipal, bool, error) {
			if st.redis != nil && cfg.Redis.AccessKeyMode != "file_only" {
				record, err := st.redis.LookupAccessKey(ctx, accessKey)
				if err != nil {
					return auth.AuthPrincipal{}, false, err
				}
				if record != nil {
					subjectType := strings.TrimSpace(record.SubjectType)
					if subjectType == "" {
						subjectType = "api_key"
					}
					subjectID := strings.TrimSpace(record.SubjectID)
					if subjectID == "" {
						// Legacy Redis records used the access key name as the subject.
						subjectID = strings.TrimSpace(record.Name)
					}
					accountID := strings.TrimSpace(record.AccountID)
					if accountID == "" {
						accountID = subjectID
					}
					return auth.AuthPrincipal{
						AccessKeyID:         strings.TrimSpace(record.Name),
						AccountID:           accountID,
						SubjectType:         subjectType,
						SubjectID:           subjectID,
						RoutePolicyID:       strings.TrimSpace(record.RoutePolicyID),
						AllowedProviders:    append([]string(nil), record.AllowedProviders...),
						AllowedModels:       append([]string(nil), record.AllowedModels...),
						ProviderKeyBindings: cloneStringMap(record.ProviderKeyBindings),
					}, true, nil
				}
				if cfg.Redis.AccessKeyMode == "redis_only" {
					return auth.AuthPrincipal{}, false, nil
				}
			}
			ks := st.Keys()
			if ks == nil {
				return auth.AuthPrincipal{}, false, nil
			}
			ak, ok := ks.MatchAccessKey(accessKey)
			if !ok || ak == nil {
				return auth.AuthPrincipal{}, false, nil
			}
			return auth.AuthPrincipal{
				AccessKeyID:         strings.TrimSpace(ak.Name),
				AccountID:           strings.TrimSpace(ak.Name),
				SubjectID:           strings.TrimSpace(ak.Name),
				ProviderKeyBindings: cloneStringMap(ak.ProviderKeyBindings),
			}, true, nil
		},
		auth.TokenKeyOptions{
			AllowBYOKWithoutK: cfg.Auth.TokenKey.AllowBYOKWithoutK,
		},
	))

	secured.GET("/admin/providers", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"providers": reg.ListProviderNames(),
		})
	})

	v1 := secured.Group("/v1")
	v1.POST("/completions", makeHandler(cfg, st, pclient, "completions", resolvedRequestIDHeaderKey, billingClient))
	v1.POST("/chat/completions", makeHandler(cfg, st, pclient, "chat.completions", resolvedRequestIDHeaderKey, billingClient))
	v1.POST("/responses", makeHandler(cfg, st, pclient, "responses", resolvedRequestIDHeaderKey, billingClient))
	v1.POST("/embeddings", makeHandler(cfg, st, pclient, "embeddings", resolvedRequestIDHeaderKey, billingClient))
	v1.POST("/images/generations", makeHandler(cfg, st, pclient, "images.generations", resolvedRequestIDHeaderKey, billingClient))
	v1.POST("/images/edits", makeHandler(cfg, st, pclient, "images.edits", resolvedRequestIDHeaderKey, billingClient))
	v1.POST("/audio/speech", makeHandler(cfg, st, pclient, "audio.speech", resolvedRequestIDHeaderKey, billingClient))
	v1.POST("/audio/transcriptions", makeHandler(cfg, st, pclient, "audio.transcriptions", resolvedRequestIDHeaderKey, billingClient))
	v1.POST("/audio/translations", makeHandler(cfg, st, pclient, "audio.translations", resolvedRequestIDHeaderKey, billingClient))
	v1.POST("/messages", makeHandler(cfg, st, pclient, "claude.messages", resolvedRequestIDHeaderKey, billingClient))
	v1.GET("/models", func(c *gin.Context) {
		c.JSON(http.StatusOK, st.ModelRouter().ToOpenAIListAt(st.StartedAtUnix()))
	})

	v1beta := secured.Group("/v1beta")
	// Gemini-style model listing.
	v1beta.GET("/models", func(c *gin.Context) {
		type geminiModel struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		}
		ids := st.ModelRouter().Models()
		out := make([]geminiModel, 0, len(ids))
		for _, id := range ids {
			out = append(out, geminiModel{Name: id, DisplayName: id})
		}
		c.JSON(http.StatusOK, gin.H{
			"models":        out,
			"nextPageToken": nil,
		})
	})
	// Gemini native API paths: /v1beta/models/{model}:generateContent
	// (Stage 1) Only generateContent / streamGenerateContent are supported.
	v1beta.POST("/models/*path", makeGeminiHandler(cfg, st, pclient, resolvedRequestIDHeaderKey, billingClient))

	return r
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func requestIDMiddleware(headerKey string) gin.HandlerFunc {
	headerKey = requestid.ResolveHeaderKey(headerKey)
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader(headerKey))
		if id == "" {
			id = requestid.Gen()
		}
		c.Header(headerKey, id)
		c.Set(headerKey, id)
		c.Next()
	}
}

func trafficDumpMiddleware(cfg *config.Config, requestIDHeaderKey string) gin.HandlerFunc {
	requestIDHeaderKey = requestid.ResolveHeaderKey(requestIDHeaderKey)
	tdcfg := trafficdump.Config{
		Enabled:     cfg.TrafficDump.Enabled,
		Dir:         cfg.TrafficDump.Dir,
		FilePath:    cfg.TrafficDump.FilePath,
		MaxBytes:    cfg.TrafficDump.MaxBytes,
		MaskSecrets: cfg.TrafficDump.MaskSecrets,
		Sections:    cfg.TrafficDump.Sections,
	}
	return func(c *gin.Context) {
		rec, err := trafficdump.StartWithHeaderKey(c, tdcfg, requestIDHeaderKey)
		if err != nil {
			log.Printf(
				"[ONR] WARN | traffic_dump | traffic dump start failed, skip for request | error=%v path=%s",
				err,
				c.Request.URL.Path,
			)
			c.Next()
			return
		}
		c.Next()
		rec.Close()
		if werr := rec.Err(); werr != nil {
			log.Printf(
				"[ONR] WARN | traffic_dump | traffic dump write failed | request_id=%s error=%v path=%s",
				trafficdump.RequestIDWithHeaderKey(c, requestIDHeaderKey),
				werr,
				c.Request.URL.Path,
			)
		}
	}
}
