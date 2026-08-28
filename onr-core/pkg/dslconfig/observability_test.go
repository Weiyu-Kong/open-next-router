package dslconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeObservabilityProvider(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "example.conf")
	content := `provider "example" {
	defaults { upstream_config { base_url = "https://example.com"; } }
` + body + "\n}\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateProviderFile_ObservabilityUpstreamRequestID(t *testing.T) {
	path := writeObservabilityProvider(t, `
	observability {
		upstream_request_id "x-request-id" "OpenAI-Request-ID";
	}
`)
	pf, err := ValidateProviderFile(path)
	if err != nil {
		t.Fatalf("ValidateProviderFile: %v", err)
	}
	if pf.Observability.UpstreamRequestID == nil {
		t.Fatal("expected upstream request ID rule")
	}
	want := []string{"x-request-id", "OpenAI-Request-ID"}
	if got := pf.Observability.UpstreamRequestID.Headers; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("headers=%v want=%v", got, want)
	}
}

func TestValidateProviderFile_ObservabilityUpstreamRequestIDJSON(t *testing.T) {
	path := writeObservabilityProvider(t, `
	observability {
		upstream_request_id_json "$.data.id";
	}
`)
	pf, err := ValidateProviderFile(path)
	if err != nil {
		t.Fatalf("ValidateProviderFile: %v", err)
	}
	if pf.Observability.UpstreamRequestIDJSON == nil {
		t.Fatal("expected upstream request ID JSON rule")
	}
	if got := pf.Observability.UpstreamRequestIDJSON.Path; got != "$.data.id" {
		t.Fatalf("path=%q want $.data.id", got)
	}
}

func TestValidateProviderFile_ObservabilityRejectsInvalidRules(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"missing semicolon", `observability { upstream_request_id "x-request-id" }`, "expected ';' after upstream_request_id"},
		{"invalid header", `observability { upstream_request_id "bad header"; }`, "invalid upstream request ID header name"},
		{"duplicate header", `observability { upstream_request_id "X-Request-ID" "x-request-id"; }`, "duplicate upstream request ID header"},
		{"duplicate header directive", `observability { upstream_request_id "x-request-id"; upstream_request_id "request-id"; }`, "duplicate upstream_request_id directive"},
		{"json path must be string", `observability { upstream_request_id_json $.id; }`, "expects one JSONPath string"},
		{"invalid json path", `observability { upstream_request_id_json "id"; }`, "invalid upstream request ID JSONPath"},
		{"extra json path", `observability { upstream_request_id_json "$.id" "$.other"; }`, "expects one JSONPath string followed by ';'"},
		{"missing json semicolon", `observability { upstream_request_id_json "$.id" }`, "expects one JSONPath string followed by ';'"},
		{"duplicate json directive", `observability { upstream_request_id_json "$.id"; upstream_request_id_json "$.request_id"; }`, "duplicate upstream_request_id_json directive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateProviderFile(writeObservabilityProvider(t, tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateProviderFile_ObservabilityRejectsEmptyRule(t *testing.T) {
	_, err := ValidateProviderFile(writeObservabilityProvider(t, `
	observability {
		upstream_request_id;
	}
`))
	if err == nil || !strings.Contains(err.Error(), "requires at least one header name") {
		t.Fatalf("error=%v want missing-header validation error", err)
	}
}

func TestValidateProviderFile_ObservabilityMissingIsUnset(t *testing.T) {
	pf, err := ValidateProviderFile(writeObservabilityProvider(t, ""))
	if err != nil {
		t.Fatalf("ValidateProviderFile: %v", err)
	}
	if pf.Observability.UpstreamRequestID != nil {
		t.Fatalf("expected unset rule, got %#v", pf.Observability.UpstreamRequestID)
	}
	if pf.Observability.UpstreamRequestIDJSON != nil {
		t.Fatalf("expected unset JSON rule, got %#v", pf.Observability.UpstreamRequestIDJSON)
	}
}
