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

// --- Notification verification -------------------------------------------------

const (
	antomTestNotifyMethod           = http.MethodPost
	antomTestPaymentNotifyPath      = "/api/aurora/billing/antom/notify/payment"
	antomTestSubscriptionNotifyPath = "/api/aurora/billing/antom/notify/subscription"
	antomTestNotifyRequestTime      = "1730000000000"
)

// antomTestPaymentBody is a notifyPayment (subscription) callback for the first
// period of a subscription. It carries paymentTime, which the billing path
// deliberately ignores: the notification's own clock is not trustworthy.
func antomTestPaymentBody() []byte {
	return []byte(`{
		"result": {"resultStatus": "S", "resultCode": "SUCCESS", "resultMessage": "success"},
		"paymentId": "202610281200051100001234567890",
		"subscriptionId": "S2026102800001",
		"subscriptionRequestId": "aurora-sub-test",
		"periodStartTime": "2026-10-28T12:00:00+08:00",
		"periodEndTime": "2026-11-28T12:00:00+08:00",
		"paymentTime": "2026-10-28T12:00:05+08:00",
		"phaseNo": "1",
		"paymentAmount": {"currency": "SGD", "value": "1900"}
	}`)
}

// antomTestSubscriptionBody is a notifySubscription callback. It carries no
// period fields because the message has none the billing path may trust.
func antomTestSubscriptionBody() []byte {
	return []byte(`{
		"result": {"resultStatus": "S", "resultCode": "SUCCESS", "resultMessage": "success"},
		"subscriptionRequestId": "aurora-sub-test",
		"subscriptionId": "S2026102800001",
		"subscriptionStatus": "TERMINATED",
		"subscriptionNotificationType": "TERMINATE",
		"periodRule": {"periodType": "MONTH", "periodCount": "1"}
	}`)
}

// antomNotificationSignature signs a notification body the way Antom does. The
// client id is a parameter so a test can prove the header is compared with the
// configured value instead of merely being covered by the signature.
func antomNotificationSignature(t *testing.T, client *AntomClient, method, path, clientID string, body []byte) string {
	t.Helper()
	digest := sha256.Sum256(antomSignContent(method, path, clientID, antomTestNotifyRequestTime, body))
	signature, err := rsa.SignPKCS1v15(rand.Reader, client.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign notification: %v", err)
	}
	return fmt.Sprintf("algorithm=RSA256,keyVersion=%s,signature=%s",
		client.cfg.KeyVersion, url.QueryEscape(base64.StdEncoding.EncodeToString(signature)))
}

// antomNotificationHeaders builds the headers Antom sends with a notification,
// signed for the configured client.
func antomNotificationHeaders(t *testing.T, client *AntomClient, method, path string, body []byte) http.Header {
	t.Helper()
	return antomNotificationHeadersFor(t, client, method, path, client.cfg.ClientID, body)
}

func antomNotificationHeadersFor(t *testing.T, client *AntomClient, method, path, clientID string, body []byte) http.Header {
	t.Helper()
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	headers.Set("client-id", clientID)
	headers.Set("request-time", antomTestNotifyRequestTime)
	headers.Set("signature", antomNotificationSignature(t, client, method, path, clientID, body))
	return headers
}

func TestVerifyNotificationAcceptsSignedPayment(t *testing.T) {
	client := newTestAntomClient(t)
	body := antomTestPaymentBody()
	headers := antomNotificationHeaders(t, client, antomTestNotifyMethod, antomTestPaymentNotifyPath, body)

	notification, err := client.VerifyNotification(antomTestNotifyMethod, antomTestPaymentNotifyPath, headers, body)
	if err != nil {
		t.Fatalf("VerifyNotification: %v", err)
	}
	if notification.Type != "payment" {
		t.Errorf("Type = %q, want payment", notification.Type)
	}
	if notification.Status != "S" {
		t.Errorf("Status = %q, want the body's result.resultStatus S", notification.Status)
	}
	if notification.RequestID != "aurora-sub-test" {
		t.Errorf("RequestID = %q, want subscriptionRequestId", notification.RequestID)
	}
	if notification.SubscriptionID != "S2026102800001" {
		t.Errorf("SubscriptionID = %q", notification.SubscriptionID)
	}
	if notification.PaymentID != "202610281200051100001234567890" {
		t.Errorf("PaymentID = %q", notification.PaymentID)
	}
	if notification.PhaseNo != "1" {
		t.Errorf("PhaseNo = %q, want 1", notification.PhaseNo)
	}
	if notification.NotificationType != "" {
		t.Errorf("NotificationType = %q, want empty for a payment notification", notification.NotificationType)
	}

	sgt := time.FixedZone("SGT", 8*60*60)
	if want := time.Date(2026, 10, 28, 12, 0, 0, 0, sgt); !notification.PeriodStart.Equal(want) {
		t.Errorf("PeriodStart = %v, want %v", notification.PeriodStart, want)
	}
	if want := time.Date(2026, 11, 28, 12, 0, 0, 0, sgt); !notification.PeriodEnd.Equal(want) {
		t.Errorf("PeriodEnd = %v, want %v", notification.PeriodEnd, want)
	}
}

func TestVerifyNotificationAcceptsSignedSubscription(t *testing.T) {
	client := newTestAntomClient(t)
	body := antomTestSubscriptionBody()
	headers := antomNotificationHeaders(t, client, antomTestNotifyMethod, antomTestSubscriptionNotifyPath, body)

	notification, err := client.VerifyNotification(antomTestNotifyMethod, antomTestSubscriptionNotifyPath, headers, body)
	if err != nil {
		t.Fatalf("VerifyNotification: %v", err)
	}
	if notification.Type != "subscription" {
		t.Errorf("Type = %q, want subscription", notification.Type)
	}
	if notification.Status != "TERMINATED" {
		t.Errorf("Status = %q, want subscriptionStatus TERMINATED", notification.Status)
	}
	if notification.NotificationType != "TERMINATE" {
		t.Errorf("NotificationType = %q, want subscriptionNotificationType TERMINATE", notification.NotificationType)
	}
	if notification.RequestID != "aurora-sub-test" {
		t.Errorf("RequestID = %q, want subscriptionRequestId", notification.RequestID)
	}
	if notification.SubscriptionID != "S2026102800001" {
		t.Errorf("SubscriptionID = %q", notification.SubscriptionID)
	}
	if notification.PaymentID != "" {
		t.Errorf("PaymentID = %q, want empty for a subscription notification", notification.PaymentID)
	}
	// notifySubscription carries no trustworthy time fields, so the parser must
	// not invent them.
	if !notification.PeriodStart.IsZero() || !notification.PeriodEnd.IsZero() {
		t.Errorf("period times = %v/%v, want zero for a subscription notification", notification.PeriodStart, notification.PeriodEnd)
	}
}

func TestVerifyNotificationRejectsBadSignature(t *testing.T) {
	client := newTestAntomClient(t)
	body := antomTestPaymentBody()
	headers := antomNotificationHeaders(t, client, antomTestNotifyMethod, antomTestPaymentNotifyPath, body)

	tampered := append([]byte(nil), body...)
	tampered[len(tampered)-2] ^= 0x01

	notification, err := client.VerifyNotification(antomTestNotifyMethod, antomTestPaymentNotifyPath, headers, tampered)
	if err == nil {
		t.Fatal("tampered body was accepted")
	}
	if notification != (AntomNotification{}) {
		t.Errorf("failed verification returned %+v, want the zero notification", notification)
	}
}

func TestVerifyNotificationRejectsWrongClientID(t *testing.T) {
	client := newTestAntomClient(t)
	body := antomTestPaymentBody()
	// The signature is valid for the foreign client id, so only the explicit
	// comparison with the configured client can reject this notification.
	headers := antomNotificationHeadersFor(t, client, antomTestNotifyMethod, antomTestPaymentNotifyPath, "SANDBOX_OTHER", body)

	if _, err := client.VerifyNotification(antomTestNotifyMethod, antomTestPaymentNotifyPath, headers, body); err == nil {
		t.Fatal("notification signed for another client id was accepted")
	}
}

func TestVerifyNotificationRejectsWrongKeyVersion(t *testing.T) {
	client := newTestAntomClient(t)
	body := antomTestPaymentBody()
	headers := antomNotificationHeaders(t, client, antomTestNotifyMethod, antomTestPaymentNotifyPath, body)

	// keyVersion is not part of the signed content, so the signature stays valid
	// and only the explicit version check can reject this header.
	wrong := strings.Replace(headers.Get("signature"), "keyVersion="+client.cfg.KeyVersion, "keyVersion=9", 1)
	if !strings.Contains(wrong, "keyVersion=9") {
		t.Fatalf("test setup did not change the key version: %q", wrong)
	}
	headers.Set("signature", wrong)

	if _, err := client.VerifyNotification(antomTestNotifyMethod, antomTestPaymentNotifyPath, headers, body); err == nil {
		t.Fatal("notification with a foreign key version was accepted")
	}
}

func TestVerifyNotificationRejectsUnknownAlgorithm(t *testing.T) {
	client := newTestAntomClient(t)
	body := antomTestPaymentBody()
	headers := antomNotificationHeaders(t, client, antomTestNotifyMethod, antomTestPaymentNotifyPath, body)

	wrong := strings.Replace(headers.Get("signature"), "algorithm=RSA256", "algorithm=RSA1024", 1)
	if !strings.Contains(wrong, "algorithm=RSA1024") {
		t.Fatalf("test setup did not change the algorithm: %q", wrong)
	}
	headers.Set("signature", wrong)

	if _, err := client.VerifyNotification(antomTestNotifyMethod, antomTestPaymentNotifyPath, headers, body); err == nil {
		t.Fatal("notification with an unknown algorithm was accepted")
	}
}

func TestVerifyNotificationRejectsMalformedSignatureHeader(t *testing.T) {
	client := newTestAntomClient(t)
	body := antomTestPaymentBody()
	headers := antomNotificationHeaders(t, client, antomTestNotifyMethod, antomTestPaymentNotifyPath, body)
	valid := headers.Get("signature")

	cases := []struct {
		name   string
		header string
	}{
		{"missing header", ""},
		{"missing algorithm", strings.Replace(valid, "algorithm=RSA256,", "", 1)},
		{"missing key version", strings.Replace(valid, ",keyVersion="+client.cfg.KeyVersion, "", 1)},
		{"missing signature value", strings.SplitN(valid, ",signature=", 2)[0]},
		{"repeated algorithm", valid + ",algorithm=RSA256"},
		{"unknown field", valid + ",digest=SHA256"},
		{"empty value", strings.SplitN(valid, ",signature=", 2)[0] + ",signature="},
		{"not key value", valid + ",garbage"},
		{"signature is not base64", "algorithm=RSA256,keyVersion=" + client.cfg.KeyVersion + ",signature=%21%21"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clone := headers.Clone()
			clone.Set("signature", tc.header)
			if _, err := client.VerifyNotification(antomTestNotifyMethod, antomTestPaymentNotifyPath, clone, body); err == nil {
				t.Fatalf("signature header %q was accepted", tc.header)
			}
		})
	}
}

func TestVerifyNotificationRejectsUnrecognizedBody(t *testing.T) {
	client := newTestAntomClient(t)
	// Correctly signed bodies that are neither a payment nor a subscription
	// notification: only the parser can reject them.
	bodies := [][]byte{
		[]byte(`{"result":{"resultStatus":"S"},"subscriptionRequestId":"aurora-sub-test"}`),
		[]byte(`not json at all`),
	}
	for _, body := range bodies {
		headers := antomNotificationHeaders(t, client, antomTestNotifyMethod, antomTestPaymentNotifyPath, body)
		if _, err := client.VerifyNotification(antomTestNotifyMethod, antomTestPaymentNotifyPath, headers, body); err == nil {
			t.Errorf("body %s was accepted as a notification", body)
		}
	}
}

func TestVerifyNotificationParsesRenewalPayment(t *testing.T) {
	client := newTestAntomClient(t)
	body := []byte(`{
		"result": {"resultStatus": "S", "resultCode": "SUCCESS"},
		"paymentId": "202611281200051100001234567891",
		"subscriptionId": "S2026102800001",
		"subscriptionRequestId": "aurora-sub-test",
		"periodStartTime": "2026-11-28T12:00:00+08:00",
		"periodEndTime": "2026-12-28T12:00:00.5+08:00",
		"phaseNo": "2",
		"paymentAmount": {"currency": "SGD", "value": "1900"}
	}`)
	headers := antomNotificationHeaders(t, client, antomTestNotifyMethod, antomTestPaymentNotifyPath, body)

	notification, err := client.VerifyNotification(antomTestNotifyMethod, antomTestPaymentNotifyPath, headers, body)
	if err != nil {
		t.Fatalf("VerifyNotification: %v", err)
	}
	if notification.PhaseNo != "2" {
		t.Errorf("PhaseNo = %q, want 2", notification.PhaseNo)
	}
	sgt := time.FixedZone("SGT", 8*60*60)
	if want := time.Date(2026, 11, 28, 12, 0, 0, 0, sgt); !notification.PeriodStart.Equal(want) {
		t.Errorf("PeriodStart = %v, want %v", notification.PeriodStart, want)
	}
	if want := time.Date(2026, 12, 28, 12, 0, 0, 500*int(time.Millisecond), sgt); !notification.PeriodEnd.Equal(want) {
		t.Errorf("PeriodEnd = %v, want %v (fractional seconds)", notification.PeriodEnd, want)
	}
}

func TestVerifyNotificationLeavesUnparsableTimesZero(t *testing.T) {
	client := newTestAntomClient(t)
	body := []byte(`{
		"result": {"resultStatus": "S"},
		"paymentId": "202610281200051100001234567890",
		"subscriptionId": "S2026102800001",
		"subscriptionRequestId": "aurora-sub-test",
		"periodStartTime": "not-a-timestamp",
		"periodEndTime": "2026-11-28T12:00:00.123+08:00",
		"phaseNo": "1"
	}`)
	headers := antomNotificationHeaders(t, client, antomTestNotifyMethod, antomTestPaymentNotifyPath, body)

	notification, err := client.VerifyNotification(antomTestNotifyMethod, antomTestPaymentNotifyPath, headers, body)
	if err != nil {
		t.Fatalf("VerifyNotification: %v", err)
	}
	if !notification.PeriodStart.IsZero() {
		t.Errorf("PeriodStart = %v, want the zero time for an unparsable timestamp", notification.PeriodStart)
	}
	sgt := time.FixedZone("SGT", 8*60*60)
	if want := time.Date(2026, 11, 28, 12, 0, 0, 123*int(time.Millisecond), sgt); !notification.PeriodEnd.Equal(want) {
		t.Errorf("PeriodEnd = %v, want %v", notification.PeriodEnd, want)
	}
}

// A payment notification without result.resultStatus carries no charge result.
// Accepting it would hand an empty status to the billing handler, whose
// non-"S" branch marks the subscription past_due — downgrading a paying user
// over a malformed payload. The parser must surface the defect instead.
func TestVerifyNotificationRejectsPaymentWithoutResultStatus(t *testing.T) {
	client := newTestAntomClient(t)
	bodies := map[string][]byte{
		"missing result object": []byte(`{
			"paymentId": "202610281200051100001234567890",
			"subscriptionId": "S2026102800001",
			"subscriptionRequestId": "aurora-sub-test",
			"periodStartTime": "2026-10-28T12:00:00+08:00",
			"periodEndTime": "2026-11-28T12:00:00+08:00",
			"phaseNo": "1"
		}`),
		"empty resultStatus": []byte(`{
			"result": {"resultCode": "SUCCESS"},
			"paymentId": "202610281200051100001234567890",
			"subscriptionId": "S2026102800001",
			"subscriptionRequestId": "aurora-sub-test",
			"phaseNo": "1"
		}`),
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			headers := antomNotificationHeaders(t, client, antomTestNotifyMethod, antomTestPaymentNotifyPath, body)
			notification, err := client.VerifyNotification(antomTestNotifyMethod, antomTestPaymentNotifyPath, headers, body)
			if err == nil {
				t.Fatalf("payment notification without resultStatus was accepted as %+v", notification)
			}
			if !strings.Contains(err.Error(), "result.resultStatus") {
				t.Errorf("error %q does not name the missing result.resultStatus", err)
			}
			if notification != (AntomNotification{}) {
				t.Errorf("failed verification returned %+v, want the zero notification", notification)
			}
		})
	}
}
