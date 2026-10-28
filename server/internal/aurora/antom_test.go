package aurora

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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

// antomTestAmount mirrors the wire form of an Antom amount, declared here so
// the assertions read the JSON instead of reusing the implementation's type.
type antomTestAmount struct {
	Currency string `json:"currency"`
	Value    string `json:"value"`
}

// antomTestRequest is one request captured by the fake gateway.
type antomTestRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// newAntomTestGateway starts a fake Antom gateway that answers with response and
// records the one request it receives. The client is pointed at it after
// construction because the real constructor only accepts official origins.
func newAntomTestGateway(t *testing.T, response string) (*AntomClient, <-chan antomTestRequest) {
	t.Helper()
	return newAntomTestGatewayFor(t, newTestAntomClient(t), response)
}

func newAntomTestGatewayFor(t *testing.T, client *AntomClient, response string) (*AntomClient, <-chan antomTestRequest) {
	t.Helper()
	requests := make(chan antomTestRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read gateway request: %v", err)
		}
		requests <- antomTestRequest{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body}
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, response); err != nil {
			t.Errorf("write gateway response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	client.cfg.APIBase = server.URL
	return client, requests
}

// verifyAntomSignedRequest replays Antom's own signature check over a recorded
// request, so the test fails if the client sends an unsigned or mis-signed body.
func verifyAntomSignedRequest(t *testing.T, client *AntomClient, got antomTestRequest) {
	t.Helper()
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if clientID := got.header.Get("client-id"); clientID != client.cfg.ClientID {
		t.Errorf("client-id = %q, want %q", clientID, client.cfg.ClientID)
	}
	requestTime := got.header.Get("request-time")
	if requestTime == "" {
		t.Fatal("request-time header is missing")
	}
	prefix := fmt.Sprintf("algorithm=RSA256,keyVersion=%s,signature=", client.cfg.KeyVersion)
	header := got.header.Get("signature")
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
	digest := sha256.Sum256(antomSignContent(got.method, got.path, client.cfg.ClientID, requestTime, got.body))
	if err := rsa.VerifyPKCS1v15(client.publicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("request signature does not verify: %v", err)
	}
}

func antomTestSubscriptionRequest() AntomSubscriptionRequest {
	return AntomSubscriptionRequest{
		RequestID:             "aurora-sub-test",
		Tier:                  "creator",
		Amount:                "1900",
		Currency:              "SGD",
		RedirectURL:           "https://aod.example/billing/complete",
		PaymentNotifyURL:      "https://api.example/api/aurora/billing/antom/notify/payment",
		SubscriptionNotifyURL: "https://api.example/api/aurora/billing/antom/notify/subscription",
		StartTime:             time.Date(2026, 10, 28, 12, 0, 0, 0, time.FixedZone("SGT", 8*60*60)),
	}
}

func TestCreateSubscriptionPostsSignedRequest(t *testing.T) {
	client, requests := newAntomTestGateway(t, `{"result":{"resultStatus":"S","resultCode":"SUCCESS"},"normalUrl":"https://checkout.example/x"}`)

	checkoutURL, err := client.CreateSubscription(context.Background(), antomTestSubscriptionRequest())
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if checkoutURL != "https://checkout.example/x" {
		t.Errorf("checkout URL = %q, want the normalUrl from the response", checkoutURL)
	}

	got := <-requests
	if got.path != "/ams/api/v1/subscriptions/create" {
		t.Errorf("path = %q, want /ams/api/v1/subscriptions/create", got.path)
	}
	verifyAntomSignedRequest(t, client, got)

	var body struct {
		SubscriptionRequestID   string `json:"subscriptionRequestId"`
		SubscriptionDescription string `json:"subscriptionDescription"`
		SubscriptionRedirectURL string `json:"subscriptionRedirectUrl"`
		SubscriptionStartTime   string `json:"subscriptionStartTime"`
		PeriodRule              struct {
			PeriodType  string `json:"periodType"`
			PeriodCount string `json:"periodCount"`
		} `json:"periodRule"`
		PaymentMethod struct {
			PaymentMethodType string `json:"paymentMethodType"`
		} `json:"paymentMethod"`
		PaymentNotificationURL      string `json:"paymentNotificationUrl"`
		SubscriptionNotificationURL string `json:"subscriptionNotificationUrl"`
		OrderInfo                   struct {
			OrderAmount antomTestAmount `json:"orderAmount"`
		} `json:"orderInfo"`
		PaymentAmount antomTestAmount `json:"paymentAmount"`
		Env           struct {
			TerminalType string `json:"terminalType"`
		} `json:"env"`
		SettlementStrategy map[string]string `json:"settlementStrategy"`
	}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body.SubscriptionRequestID != "aurora-sub-test" {
		t.Errorf("subscriptionRequestId = %q", body.SubscriptionRequestID)
	}
	if body.SubscriptionDescription != "creator monthly" {
		t.Errorf("subscriptionDescription = %q", body.SubscriptionDescription)
	}
	if body.SubscriptionRedirectURL != "https://aod.example/billing/complete" {
		t.Errorf("subscriptionRedirectUrl = %q", body.SubscriptionRedirectURL)
	}
	if body.SubscriptionStartTime != "2026-10-28T12:00:00+08:00" {
		t.Errorf("subscriptionStartTime = %q, want RFC 3339 with the caller's offset", body.SubscriptionStartTime)
	}
	if body.PeriodRule.PeriodType != "MONTH" || body.PeriodRule.PeriodCount != "1" {
		t.Errorf("periodRule = %+v, want MONTH/1", body.PeriodRule)
	}
	if body.PaymentNotificationURL != "https://api.example/api/aurora/billing/antom/notify/payment" {
		t.Errorf("paymentNotificationUrl = %q", body.PaymentNotificationURL)
	}
	if body.SubscriptionNotificationURL != "https://api.example/api/aurora/billing/antom/notify/subscription" {
		t.Errorf("subscriptionNotificationUrl = %q", body.SubscriptionNotificationURL)
	}
	if body.PaymentAmount.Value != "1900" || body.PaymentAmount.Currency != "SGD" {
		t.Errorf("paymentAmount = %+v, want 1900 SGD (minor units)", body.PaymentAmount)
	}
	if body.OrderInfo.OrderAmount != body.PaymentAmount {
		t.Errorf("orderInfo.orderAmount = %+v, want the same amount as paymentAmount", body.OrderInfo.OrderAmount)
	}
	if body.Env.TerminalType != "WEB" {
		t.Errorf("env.terminalType = %q, want WEB", body.Env.TerminalType)
	}
	if len(body.SettlementStrategy) != 0 {
		t.Errorf("settlementStrategy = %+v, want an empty object when no settlement currency is configured", body.SettlementStrategy)
	}
}

func TestCreateSubscriptionSendsConfiguredSettlementCurrency(t *testing.T) {
	priv, pub := newTestAntomKeys(t)
	client, err := NewAntomClient(AntomConfig{
		ClientID:           "SANDBOX_1",
		MerchantPrivateKey: priv,
		AntomPublicKey:     pub,
		SettlementCurrency: " sgd ",
	})
	if err != nil {
		t.Fatalf("NewAntomClient: %v", err)
	}
	if client.cfg.SettlementCurrency != "SGD" {
		t.Fatalf("SettlementCurrency = %q, want the normalized code SGD", client.cfg.SettlementCurrency)
	}
	client, requests := newAntomTestGatewayFor(t, client, `{"result":{"resultStatus":"S","resultCode":"SUCCESS"},"normalUrl":"https://checkout.example/x"}`)

	if _, err := client.CreateSubscription(context.Background(), antomTestSubscriptionRequest()); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	got := <-requests
	var body struct {
		SettlementStrategy map[string]string `json:"settlementStrategy"`
	}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body.SettlementStrategy["settlementCurrency"] != "SGD" {
		t.Errorf("settlementStrategy = %+v, want settlementCurrency SGD", body.SettlementStrategy)
	}
}

func TestCreateSubscriptionRejectsInvalidAmount(t *testing.T) {
	client, _ := newAntomTestGateway(t, `{"result":{"resultStatus":"S","resultCode":"SUCCESS"},"normalUrl":"https://checkout.example/x"}`)

	for _, amount := range []string{"", "19.00", "1900.00", "-1900", "0", "000"} {
		req := antomTestSubscriptionRequest()
		req.Amount = amount
		if _, err := client.CreateSubscription(context.Background(), req); err == nil {
			t.Errorf("amount %q was accepted", amount)
		}
	}
}

func TestCreateSubscriptionRejectsFailedResult(t *testing.T) {
	client, _ := newAntomTestGateway(t, `{"result":{"resultStatus":"F","resultCode":"PARAM_ILLEGAL","resultMessage":"bad request"}}`)

	if _, err := client.CreateSubscription(context.Background(), antomTestSubscriptionRequest()); err == nil {
		t.Fatal("failed result was accepted")
	} else if !strings.Contains(err.Error(), "PARAM_ILLEGAL") {
		t.Fatalf("error %q does not name the Antom result code", err)
	}
}

func TestCreateSubscriptionRejectsMissingNormalURL(t *testing.T) {
	client, _ := newAntomTestGateway(t, `{"result":{"resultStatus":"S","resultCode":"SUCCESS"}}`)

	if _, err := client.CreateSubscription(context.Background(), antomTestSubscriptionRequest()); err == nil {
		t.Fatal("successful result without normalUrl was accepted")
	}
}

func TestCancelSubscriptionSendsCANCEL(t *testing.T) {
	client, requests := newAntomTestGateway(t, `{"result":{"resultStatus":"S","resultCode":"SUCCESS"}}`)

	if err := client.CancelSubscription(context.Background(), "S1"); err != nil {
		t.Fatalf("CancelSubscription: %v", err)
	}

	got := <-requests
	if got.path != "/ams/api/v1/subscriptions/cancel" {
		t.Errorf("path = %q, want /ams/api/v1/subscriptions/cancel", got.path)
	}
	verifyAntomSignedRequest(t, client, got)

	var body struct {
		SubscriptionID   string `json:"subscriptionId"`
		CancellationType string `json:"cancellationType"`
	}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body.SubscriptionID != "S1" {
		t.Errorf("subscriptionId = %q, want S1", body.SubscriptionID)
	}
	if body.CancellationType != "CANCEL" {
		t.Errorf("cancellationType = %q, want CANCEL", body.CancellationType)
	}
}

func TestCancelSubscriptionRejectsFailedResult(t *testing.T) {
	client, _ := newAntomTestGateway(t, `{"result":{"resultStatus":"F","resultCode":"PROCESS_FAIL","resultMessage":"failed"}}`)

	if err := client.CancelSubscription(context.Background(), "S1"); err == nil {
		t.Fatal("failed result was accepted")
	} else if !strings.Contains(err.Error(), "PROCESS_FAIL") {
		t.Fatalf("error %q does not name the Antom result code", err)
	}
}

func TestInquirySubscriptionReadsState(t *testing.T) {
	client, requests := newAntomTestGateway(t, `{"result":{"resultStatus":"S"},"subscription":{"subscriptionId":"S1","status":"ACTIVE","currentPeriodStartTime":"2026-10-28T12:00:00+08:00","currentPeriodEndTime":"2026-11-28T12:00:00+08:00","paymentAmount":{"currency":"SGD","value":"1900"}}}`)

	state, err := client.InquirySubscription(context.Background(), "S1")
	if err != nil {
		t.Fatalf("InquirySubscription: %v", err)
	}
	if state.SubscriptionID != "S1" || state.Status != "ACTIVE" {
		t.Errorf("state = %+v, want S1/ACTIVE", state)
	}
	want := time.Date(2026, 11, 28, 12, 0, 0, 0, time.FixedZone("SGT", 8*60*60))
	if !state.CurrentPeriodEnd.Equal(want) {
		t.Errorf("CurrentPeriodEnd = %v, want %v", state.CurrentPeriodEnd, want)
	}

	got := <-requests
	if got.path != "/ams/api/v1/subscriptions/inquireSubscription" {
		t.Errorf("path = %q, want /ams/api/v1/subscriptions/inquireSubscription", got.path)
	}
	verifyAntomSignedRequest(t, client, got)

	var body struct {
		SubscriptionID string `json:"subscriptionId"`
	}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body.SubscriptionID != "S1" {
		t.Errorf("subscriptionId = %q, want S1", body.SubscriptionID)
	}
}

func TestInquirySubscriptionRejectsMissingSubscription(t *testing.T) {
	for _, response := range []string{
		`{"result":{"resultStatus":"S"}}`,
		`{"result":{"resultStatus":"S"},"subscription":{}}`,
	} {
		client, _ := newAntomTestGateway(t, response)
		if _, err := client.InquirySubscription(context.Background(), "S1"); err == nil {
			t.Errorf("response %s was accepted as a subscription state", response)
		}
	}
}
