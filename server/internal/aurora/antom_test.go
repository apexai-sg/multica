package aurora

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/url"
	"strings"
	"testing"
)

// newTestAntomKeys mints a throwaway RSA pair per call and returns it in the
// PEM shapes the constructor accepts. A committed test private key is still a
// committed key, so tests generate their own instead of carrying one.
func newTestAntomKeys(t *testing.T) (privPEM, pubPEM string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	privPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	pubPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	return privPEM, pubPEM
}

func newTestAntomClient(t *testing.T) *AntomClient {
	t.Helper()
	priv, pub := newTestAntomKeys(t)
	client, err := NewAntomClient(AntomConfig{
		ClientID:           "SANDBOX_1",
		MerchantPrivateKey: priv,
		AntomPublicKey:     pub,
	})
	if err != nil {
		t.Fatalf("NewAntomClient: %v", err)
	}
	return client
}

func TestNewAntomClientRequiresCredentials(t *testing.T) {
	priv, pub := newTestAntomKeys(t)
	base := AntomConfig{ClientID: "SANDBOX_1", MerchantPrivateKey: priv, AntomPublicKey: pub}

	if _, err := NewAntomClient(base); err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}

	cases := []struct {
		name  string
		field string
		clear func(*AntomConfig)
	}{
		{"client id", "ClientID", func(c *AntomConfig) { c.ClientID = "" }},
		{"merchant private key", "MerchantPrivateKey", func(c *AntomConfig) { c.MerchantPrivateKey = "" }},
		{"antom public key", "AntomPublicKey", func(c *AntomConfig) { c.AntomPublicKey = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.clear(&cfg)
			_, err := NewAntomClient(cfg)
			if err == nil {
				t.Fatalf("config without %s was accepted", tc.field)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("error %q does not name %s", err, tc.field)
			}
		})
	}
}

// The merchant private key signs payment requests, so the gateway origin must
// be one Antom operates: an unexpected host would receive signed requests.
// Exact matching is the point of the lookalike case below.
func TestNewAntomClientRejectsNonOfficialBaseURL(t *testing.T) {
	priv, pub := newTestAntomKeys(t)
	for _, base := range []string{
		"https://evil.example.com",
		"https://open-sea-global.alipay.com.evil.example.com",
	} {
		_, err := NewAntomClient(AntomConfig{
			ClientID:           "SANDBOX_1",
			MerchantPrivateKey: priv,
			AntomPublicKey:     pub,
			APIBase:            base,
		})
		if err == nil {
			t.Fatalf("base URL %q was accepted", base)
		}
	}
}

func TestNewAntomClientDefaults(t *testing.T) {
	client := newTestAntomClient(t)
	if client.cfg.APIBase != "https://open-sea-global.alipay.com" {
		t.Errorf("APIBase = %q", client.cfg.APIBase)
	}
	if client.cfg.Currency != "SGD" {
		t.Errorf("Currency = %q", client.cfg.Currency)
	}
	if client.cfg.KeyVersion != "1" {
		t.Errorf("KeyVersion = %q", client.cfg.KeyVersion)
	}
}

// Merchant consoles hand the keys out either as PEM or as bare base64 DER;
// both must load or production configuration silently depends on the console's
// export format.
func TestNewAntomClientAcceptsBase64Keys(t *testing.T) {
	privPEM, pubPEM := newTestAntomKeys(t)
	privBlock, _ := pem.Decode([]byte(privPEM))
	pubBlock, _ := pem.Decode([]byte(pubPEM))
	if privBlock == nil || pubBlock == nil {
		t.Fatal("test key helper did not produce PEM blocks")
	}
	if _, err := NewAntomClient(AntomConfig{
		ClientID:           "SANDBOX_1",
		MerchantPrivateKey: base64.StdEncoding.EncodeToString(privBlock.Bytes),
		AntomPublicKey:     base64.StdEncoding.EncodeToString(pubBlock.Bytes),
	}); err != nil {
		t.Fatalf("base64 DER keys rejected: %v", err)
	}
}

func TestAntomSignContentIsExact(t *testing.T) {
	got := antomSignContent("POST", "/ams/api/v1/subscriptions/create", "SANDBOX_1", "1685599933871", []byte(`{"a":1}`))
	want := []byte("POST /ams/api/v1/subscriptions/create\nSANDBOX_1.1685599933871.{\"a\":1}")
	if !bytes.Equal(got, want) {
		t.Fatalf("sign content = %q, want %q", got, want)
	}
}

func TestAntomSignatureHeaderRoundTrips(t *testing.T) {
	client := newTestAntomClient(t)
	if client.cfg.KeyVersion != "1" {
		t.Fatalf("KeyVersion = %q, want default", client.cfg.KeyVersion)
	}

	const (
		method      = "POST"
		path        = "/ams/api/v1/subscriptions/create"
		requestTime = "1685599933871"
	)
	body := []byte(`{"subscriptionRequestId":"aurora-sub-test"}`)

	header, err := client.signatureHeader(method, path, requestTime, body)
	if err != nil {
		t.Fatalf("signatureHeader: %v", err)
	}
	prefix := "algorithm=RSA256,keyVersion=1,signature="
	if !strings.HasPrefix(header, prefix) {
		t.Fatalf("signature header = %q, want prefix %q", header, prefix)
	}
	unescaped, err := url.QueryUnescape(strings.TrimPrefix(header, prefix))
	if err != nil {
		t.Fatalf("unescape signature: %v", err)
	}
	signature, err := base64.StdEncoding.DecodeString(unescaped)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}

	verify := func(body []byte) error {
		digest := sha256.Sum256(antomSignContent(method, path, client.cfg.ClientID, requestTime, body))
		return rsa.VerifyPKCS1v15(client.publicKey, crypto.SHA256, digest[:], signature)
	}
	if err := verify(body); err != nil {
		t.Fatalf("signature does not verify against the configured public key: %v", err)
	}

	tampered := append([]byte(nil), body...)
	tampered[len(tampered)-2] ^= 0x01
	if err := verify(tampered); err == nil {
		t.Fatal("signature verified a tampered body")
	}
}

func TestNewAntomClientRejectsWeakKey(t *testing.T) {
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate weak key: %v", err)
	}
	_, pub := newTestAntomKeys(t)
	weakPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(weak)}))

	_, err = NewAntomClient(AntomConfig{
		ClientID:           "SANDBOX_1",
		MerchantPrivateKey: weakPEM,
		AntomPublicKey:     pub,
	})
	if err == nil {
		t.Fatal("1024-bit private key was accepted")
	}
	if !strings.Contains(err.Error(), "2048") {
		t.Fatalf("error %q does not state the 2048-bit minimum", err)
	}
}
