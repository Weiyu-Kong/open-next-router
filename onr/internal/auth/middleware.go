package auth

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type AccessKeyMatcher func(accessKey string) (name string, ok bool)
type AccessKeyResolver func(ctx context.Context, accessKey string) (principal AuthPrincipal, ok bool, err error)

// AuthPrincipal identifies the authenticated caller and its billing subject.
// SubjectType and SubjectID are intentionally separate from AccessKeyID because
// multiple access keys may belong to the same billing subject.
type AuthPrincipal struct {
	AccessKeyID string
	SubjectType string
	SubjectID   string
}

const (
	ctxAuthPrincipal    = "onr.auth_principal"
	ctxAuthAccessKeyID  = "onr.auth_access_key_id"
	ctxAuthSubjectType  = "onr.auth_subject_type"
	ctxAuthSubjectID    = "onr.auth_subject_id"
)

type TokenKeyOptions struct {
	AllowBYOKWithoutK bool
}

func Middleware(masterKey string, matchAccessKey AccessKeyMatcher, tokenOpts ...TokenKeyOptions) gin.HandlerFunc {
	var resolver AccessKeyResolver
	if matchAccessKey != nil {
		resolver = func(_ context.Context, accessKey string) (AuthPrincipal, bool, error) {
			name, ok := matchAccessKey(accessKey)
			return AuthPrincipal{
				AccessKeyID: strings.TrimSpace(name),
				SubjectID:   strings.TrimSpace(name),
			}, ok, nil
		}
	}
	return MiddlewareWithResolver(masterKey, resolver, tokenOpts...)
}

func MiddlewareWithResolver(masterKey string, resolveAccessKey AccessKeyResolver, tokenOpts ...TokenKeyOptions) gin.HandlerFunc {
	expected := strings.TrimSpace(masterKey)
	allowBYOKWithoutK := false
	if len(tokenOpts) > 0 {
		allowBYOKWithoutK = tokenOpts[0].AllowBYOKWithoutK
	}
	return func(c *gin.Context) {
		got := ""
		if v := strings.TrimSpace(c.GetHeader("Authorization")); strings.HasPrefix(v, "Bearer ") {
			got = strings.TrimSpace(strings.TrimPrefix(v, "Bearer "))
		}
		if got == "" {
			got = strings.TrimSpace(c.GetHeader("x-api-key"))
		}
		if got == "" {
			got = strings.TrimSpace(c.GetHeader("x-goog-api-key"))
		}

		// Legacy: exact match master key.
		if expected != "" && subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1 {
			c.Next()
			return
		}
		if resolveAccessKey != nil {
			if principal, ok, err := resolveAccessKey(c.Request.Context(), got); err != nil {
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
					"message": "authentication service is unavailable",
					"type":    "auth_error",
					"code":    "authentication_unavailable",
				}})
				return
			} else if ok {
				setPrincipal(c, principal)
				c.Next()
				return
			}
		}

		if ok, err := authenticateToken(c, got, expected, resolveAccessKey, allowBYOKWithoutK); err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
				"message": "authentication service is unavailable",
				"type":    "auth_error",
				"code":    "authentication_unavailable",
			}})
			return
		} else if ok {
			c.Next()
			return
		}

		{
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"message": "unauthorized",
					"type":    "invalid_request_error",
					"code":    "invalid_api_key",
				},
			})
			return
		}
	}
}

func authenticateToken(c *gin.Context, got, expected string, resolver AccessKeyResolver, allowBYOKWithoutK bool) (bool, error) {
	if !IsTokenKey(got) {
		return false, nil
	}
	claims, accessKey := parseToken(got, allowBYOKWithoutK)
	if claims == nil {
		return false, nil
	}
	var err error
	ok := false
	var principal AuthPrincipal
	if strings.TrimSpace(accessKey) != "" {
		ok = expected != "" && subtle.ConstantTimeCompare([]byte(accessKey), []byte(expected)) == 1
		if !ok && resolver != nil {
			principal, ok, err = resolver(c.Request.Context(), accessKey)
			if err != nil {
				return false, err
			}
		}
	} else if allowBYOKWithoutK && claims.Mode == TokenModeBYOK && strings.TrimSpace(claims.UpstreamKey) != "" {
		ok = true
	}
	if !ok {
		return false, nil
	}
	if ok && principal != (AuthPrincipal{}) {
		setPrincipal(c, principal)
	}
	if claims.Provider != "" {
		c.Set(ctxTokenProvider, claims.Provider)
	}
	if claims.ModelOverride != "" {
		c.Set(ctxTokenModelOverride, claims.ModelOverride)
	}
	if claims.UpstreamKey != "" {
		c.Set(ctxTokenUpstreamKey, claims.UpstreamKey)
	}
	c.Set(ctxTokenMode, string(claims.Mode))
	return true, nil
}

func setPrincipal(c *gin.Context, principal AuthPrincipal) {
	if c == nil {
		return
	}
	principal.AccessKeyID = strings.TrimSpace(principal.AccessKeyID)
	principal.SubjectType = strings.TrimSpace(principal.SubjectType)
	principal.SubjectID = strings.TrimSpace(principal.SubjectID)
	c.Set(ctxAuthPrincipal, principal)
	if principal.AccessKeyID != "" {
		c.Set(ctxAuthAccessKeyID, principal.AccessKeyID)
	}
	if principal.SubjectType != "" {
		c.Set(ctxAuthSubjectType, principal.SubjectType)
	}
	if principal.SubjectID != "" {
		c.Set(ctxAuthSubjectID, principal.SubjectID)
	}
}

// PrincipalFromContext returns the authenticated principal, when one was set.
func PrincipalFromContext(c *gin.Context) (AuthPrincipal, bool) {
	if c == nil {
		return AuthPrincipal{}, false
	}
	value, ok := c.Get(ctxAuthPrincipal)
	if !ok {
		return AuthPrincipal{}, false
	}
	principal, ok := value.(AuthPrincipal)
	return principal, ok
}

// AccessKeyID returns the non-secret access key identifier from the request context.
func AccessKeyID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.GetString(ctxAuthAccessKeyID))
}

// SubjectType returns the authenticated billing subject type from the request context.
func SubjectType(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.GetString(ctxAuthSubjectType))
}

// SubjectID returns the authenticated billing subject ID from the request context.
func SubjectID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.GetString(ctxAuthSubjectID))
}

func parseToken(got string, allowBYOKWithoutK bool) (*TokenClaims, string) {
	claims, accessKey, err := ParseTokenKeyV1WithOptions(got, TokenParseOptions{AllowBYOKWithoutK: allowBYOKWithoutK})
	if err != nil {
		return nil, ""
	}
	return claims, accessKey
}

// TokenProvider requires a non-nil Gin context from the auth middleware path.
func TokenProvider(c *gin.Context) string {
	return strings.ToLower(strings.TrimSpace(c.GetString(ctxTokenProvider)))
}

// TokenModelOverride requires a non-nil Gin context from the auth middleware path.
func TokenModelOverride(c *gin.Context) string {
	return strings.TrimSpace(c.GetString(ctxTokenModelOverride))
}

// TokenUpstreamKey requires a non-nil Gin context from the auth middleware path.
func TokenUpstreamKey(c *gin.Context) string {
	return strings.TrimSpace(c.GetString(ctxTokenUpstreamKey))
}

// TokenModeFromContext requires a non-nil Gin context from the auth middleware path.
func TokenModeFromContext(c *gin.Context) TokenMode {
	v := strings.ToLower(strings.TrimSpace(c.GetString(ctxTokenMode)))
	switch v {
	case string(TokenModeBYOK):
		return TokenModeBYOK
	default:
		return TokenModeONR
	}
}
