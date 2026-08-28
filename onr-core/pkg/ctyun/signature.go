// Package ctyun contains protocol helpers for the Ctyun provider.
package ctyun

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// SignInput describes a Ctyun EOP request. Body must be the exact bytes sent.
type SignInput struct {
	AccessKey string
	SecureKey string
	RequestID string
	EOPDate   string
	Body      []byte
	Query     url.Values
}

// Sign returns the EOP headers required by Ctyun monitor APIs.
func Sign(in SignInput) (map[string]string, error) {
	if strings.TrimSpace(in.AccessKey) == "" || strings.TrimSpace(in.SecureKey) == "" {
		return nil, fmt.Errorf("ctyun access key and secure key are required")
	}
	if strings.TrimSpace(in.RequestID) == "" || len(strings.TrimSpace(in.EOPDate)) < 8 {
		return nil, fmt.Errorf("ctyun request ID and eop date are required")
	}
	canonicalHeaders := "ctyun-eop-request-id:" + in.RequestID + "\neop-date:" + in.EOPDate + "\n"
	signatureString := canonicalHeaders + "\n" + canonicalQuery(in.Query) + "\n" + fmt.Sprintf("%x", sha256.Sum256(in.Body))
	ktime := hmacSHA256([]byte(in.SecureKey), []byte(in.EOPDate))
	kak := hmacSHA256(ktime, []byte(in.AccessKey))
	kdate := hmacSHA256(kak, []byte(in.EOPDate[:8]))
	signature := base64.StdEncoding.EncodeToString(hmacSHA256(kdate, []byte(signatureString)))
	return map[string]string{
		"Content-Type":         "application/json",
		"ctyun-eop-request-id": in.RequestID,
		"eop-date":             in.EOPDate,
		"Eop-Authorization":    in.AccessKey + " Headers=ctyun-eop-request-id;eop-date Signature=" + signature,
	}, nil
}

// NewEOPDate returns the timestamp format required by the monitor API.
func NewEOPDate(now time.Time) string { return now.UTC().Add(8 * time.Hour).Format("20060102T150405Z") }

func hmacSHA256(key, message []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(message)
	return mac.Sum(nil)
}

func canonicalQuery(query url.Values) string {
	if len(query) == 0 {
		return ""
	}
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		for _, value := range query[key] {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(escapeQuery(key))
			b.WriteByte('=')
			b.WriteString(escapeQuery(value))
		}
	}
	return b.String()
}

func escapeQuery(value string) string { return strings.ReplaceAll(url.QueryEscape(value), "+", "%20") }
