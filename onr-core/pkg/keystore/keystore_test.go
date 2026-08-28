package keystore

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_AccessKeys_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.yaml")
	if err := os.WriteFile(path, []byte(`
access_keys:
  - name: "client-a"
    value: ""
providers: {}
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Setenv("ONR_ACCESS_KEY_CLIENT_A", "ak-xxx")
	st, err := Load(path)
	if err != nil {
		t.Fatalf("Load err=%v", err)
	}
	if ak, ok := st.MatchAccessKey("ak-xxx"); !ok || ak == nil {
		t.Fatalf("expected match")
	}
}

func TestLoad_CtyunCredentialEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.yaml")
	if err := os.WriteFile(path, []byte(`providers:
  ctyun:
    keys:
      - name: primary
        value: ""
        ctyun_access_key: ""
        ctyun_secure_key: ""
        ctyun_user_id: ""
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ONR_UPSTREAM_KEY_CTYUN_PRIMARY", "app-key")
	t.Setenv("ONR_CTYUN_PRIMARY_ACCESS_KEY", "access-key")
	t.Setenv("ONR_CTYUN_PRIMARY_SECURE_KEY", "secure-key")
	t.Setenv("ONR_CTYUN_PRIMARY_USER_ID", "user-id")
	st, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	k, ok := st.KeyByName("ctyun", "primary")
	if !ok || k.Value != "app-key" || k.CtyunAccessKey != "access-key" || k.CtyunSecureKey != "secure-key" || k.CtyunUserID != "user-id" {
		t.Fatalf("unexpected ctyun key: %#v", k)
	}
}

func TestLoad_Empty_All(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.yaml")
	if err := os.WriteFile(path, []byte(`providers: {}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatalf("expected error")
	}
}

func TestStore_RotationAndAccessors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.yaml")
	if err := os.WriteFile(path, []byte(`
providers:
  openai:
    keys:
      - name: "k1"
        value: "v1"
      - name: "k2"
        value: "v2"
access_keys:
  - name: "client-a"
    value: "ak-1"
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	st, err := Load(path)
	if err != nil {
		t.Fatalf("Load err=%v", err)
	}
	if !st.HasProvider(" OPENAI ") {
		t.Fatalf("expected provider openai")
	}
	if st.HasProvider("unknown") {
		t.Fatalf("unexpected provider")
	}

	k1, ok := st.NextKey("openai")
	if !ok || k1 == nil || k1.Value != "v1" {
		t.Fatalf("next #1: %#v %v", k1, ok)
	}
	k2, ok := st.NextKey("openai")
	if !ok || k2 == nil || k2.Value != "v2" {
		t.Fatalf("next #2: %#v %v", k2, ok)
	}
	k3, ok := st.NextKey("openai")
	if !ok || k3 == nil || k3.Value != "v1" {
		t.Fatalf("next #3: %#v %v", k3, ok)
	}
	if _, ok := st.NextKey("none"); ok {
		t.Fatalf("unexpected key for none")
	}

	aks := st.AccessKeys()
	if len(aks) != 1 || aks[0].Name != "client-a" {
		t.Fatalf("unexpected access keys: %#v", aks)
	}
	aks[0].Value = "mutated"
	aks2 := st.AccessKeys()
	if aks2[0].Value != "ak-1" {
		t.Fatalf("AccessKeys should return copy")
	}
}

func TestLoad_CredentialFileKeyWithoutValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.yaml")
	if err := os.WriteFile(path, []byte(`
providers:
  vertex:
    keys:
      - name: "vertex-sa"
        credential_file: " /etc/onr/vertex-sa.json "
        location: " US-CENTRAL1 "
        base_url_override: " https://us-central1-aiplatform.googleapis.com/ "
access_keys:
  - name: "client-a"
    value: "ak-1"
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	st, err := Load(path)
	if err != nil {
		t.Fatalf("Load err=%v", err)
	}
	k, ok := st.NextKey("vertex")
	if !ok || k == nil {
		t.Fatalf("expected vertex key")
	}
	if k.Value != "" {
		t.Fatalf("Value=%q want empty", k.Value)
	}
	if k.CredentialFile != "/etc/onr/vertex-sa.json" {
		t.Fatalf("CredentialFile=%q", k.CredentialFile)
	}
	if k.Location != "us-central1" {
		t.Fatalf("Location=%q", k.Location)
	}
	if k.BaseURLOverride != "https://us-central1-aiplatform.googleapis.com/" {
		t.Fatalf("BaseURLOverride=%q", k.BaseURLOverride)
	}
}

func TestEnvHelpers(t *testing.T) {
	if got := normalizeProvider(" OpenAI "); got != "openai" {
		t.Fatalf("normalizeProvider=%q", got)
	}
	if got := envVarForUpstreamKey("openai", "main-key", 0); got != "ONR_UPSTREAM_KEY_OPENAI_MAIN_KEY" {
		t.Fatalf("envVarForUpstreamKey(named)=%q", got)
	}
	if got := envVarForUpstreamKey("openai", "", 1); got != "ONR_UPSTREAM_KEY_OPENAI_2" {
		t.Fatalf("envVarForUpstreamKey(index)=%q", got)
	}
	if got := envVarForAccessKey("team-a", 0); got != "ONR_ACCESS_KEY_TEAM_A" {
		t.Fatalf("envVarForAccessKey(named)=%q", got)
	}
	if got := envVarForAccessKey("", 1); got != "ONR_ACCESS_KEY_2" {
		t.Fatalf("envVarForAccessKey(index)=%q", got)
	}
	if got := sanitizeEnvToken("A-b.c"); got != "A____" {
		t.Fatalf("sanitizeEnvToken=%q", got)
	}
}

func TestEncryptDecrypt(t *testing.T) {
	t.Setenv("ONR_MASTER_KEY", "12345678901234567890123456789012")
	enc, err := Encrypt("hello")
	if err != nil {
		t.Fatalf("Encrypt err=%v", err)
	}
	if !strings.HasPrefix(enc, "ENC[v1:aesgcm:") {
		t.Fatalf("unexpected encrypted format: %q", enc)
	}
	got, err := Decrypt(enc)
	if err != nil {
		t.Fatalf("Decrypt err=%v", err)
	}
	if got != "hello" {
		t.Fatalf("unexpected plaintext: %q", got)
	}

	raw, err := decryptIfNeeded("plain")
	if err != nil || raw != "plain" {
		t.Fatalf("raw decrypt result=%q err=%v", raw, err)
	}
}

func TestDecryptIfNeeded_Errors(t *testing.T) {
	t.Setenv("ONR_MASTER_KEY", "12345678901234567890123456789012")
	if _, err := Decrypt("plain"); err == nil {
		t.Fatalf("expected ENC format error")
	}
	if _, err := decryptIfNeeded("ENC[v1:aesgcm:AA=A]"); err == nil {
		t.Fatalf("expected invalid base64 error")
	}

	short := "ENC[v1:aesgcm:" + base64.StdEncoding.EncodeToString([]byte{1, 2, 3}) + "]"
	if _, err := decryptIfNeeded(short); err == nil {
		t.Fatalf("expected short ciphertext error")
	}

	enc, err := Encrypt("secret")
	if err != nil {
		t.Fatalf("Encrypt err=%v", err)
	}
	t.Setenv("ONR_MASTER_KEY", "abcdefghijklmnopqrstuvwxyzABCDEF")
	if _, err := decryptIfNeeded(enc); err == nil {
		t.Fatalf("expected decrypt failed error")
	}
}

func TestLoadMasterKey(t *testing.T) {
	t.Setenv("ONR_MASTER_KEY", "")
	if _, err := loadMasterKey(); err == nil {
		t.Fatalf("expected missing key error")
	}

	t.Setenv("ONR_MASTER_KEY", "short")
	if _, err := loadMasterKey(); err == nil {
		t.Fatalf("expected invalid key error")
	}

	raw := []byte("12345678901234567890123456789012")
	t.Setenv("ONR_MASTER_KEY", base64.StdEncoding.EncodeToString(raw))
	k, err := loadMasterKey()
	if err != nil {
		t.Fatalf("loadMasterKey err=%v", err)
	}
	if string(k) != string(raw) {
		t.Fatalf("unexpected key")
	}
}

func TestLoad_EncryptedAndEnvOverride(t *testing.T) {
	t.Setenv("ONR_MASTER_KEY", "12345678901234567890123456789012")
	provEnc, err := Encrypt("upstream-secret")
	if err != nil {
		t.Fatalf("Encrypt provider err=%v", err)
	}
	akEnc, err := Encrypt("access-secret")
	if err != nil {
		t.Fatalf("Encrypt access err=%v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "keys.yaml")
	if err := os.WriteFile(path, []byte(`
providers:
  openai:
    keys:
      - name: "main"
        value: "ignored"
access_keys:
  - name: "client-a"
    value: "ignored"
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Setenv("ONR_UPSTREAM_KEY_OPENAI_MAIN", provEnc)
	t.Setenv("ONR_ACCESS_KEY_CLIENT_A", akEnc)

	st, err := Load(path)
	if err != nil {
		t.Fatalf("Load err=%v", err)
	}
	k, ok := st.NextKey("openai")
	if !ok || k == nil || k.Value != "upstream-secret" {
		t.Fatalf("unexpected upstream key: %#v %v", k, ok)
	}
	if ak, ok := st.MatchAccessKey("access-secret"); !ok || ak == nil {
		t.Fatalf("expected access key match")
	}
}
