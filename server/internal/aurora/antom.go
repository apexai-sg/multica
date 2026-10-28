package aurora

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AntomConfig is the merchant side of the Antom (Ant International "Ams")
// payment channel. Empty optional fields take the production defaults; a
// configuration that cannot sign or verify is rejected by NewAntomClient, so a
// half-configured channel fails at startup rather than at the first payment.
type AntomConfig struct {
	// ClientID identifies the merchant at Antom and is part of every signature.
	ClientID string
	// MerchantPrivateKey signs outgoing API calls.
	MerchantPrivateKey string
	// AntomPublicKey verifies incoming notifications.
	AntomPublicKey string
	// KeyVersion is the signing key version Antom expects back in the header.
	KeyVersion string
	// APIBase is the Antom gateway origin; empty selects the Singapore gateway.
	APIBase string
	// Currency is the settlement currency; empty selects SGD.
	Currency string
}

const (
	antomDefaultAPIBase    = "https://open-sea-global.alipay.com"
	antomDefaultCurrency   = "SGD"
	antomDefaultKeyVersion = "1"
	antomMinKeyBits        = 2048
)

// antomAPIBaseAllowed pins the official Antom gateways. Every request carries
// the merchant private key's signature, so any other origin would receive
// signed payment requests. The comparison covers the whole origin, which also
// rejects lookalikes such as
// "https://open-sea-global.alipay.com.evil.example.com".
func antomAPIBaseAllowed(base string) bool {
	switch base {
	case "https://open-sea-global.alipay.com",
		"https://open-na-global.alipay.com",
		"https://open-de-global.alipay.com",
		"https://open.antglobal-us.com",
		"https://open-sea.alipay.com",
		"https://open-na.alipay.com":
		return true
	}
	return false
}

// AntomClient signs Antom API requests with the merchant key and keeps the
// Antom key for notification verification. The fields stay unexported so a
// caller cannot swap a key or re-point the gateway after construction.
type AntomClient struct {
	cfg        AntomConfig
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
	http       *http.Client
}

// NewAntomClient builds the client or explains which part of the configuration
// is unusable. Callers treat the error as "the Antom channel is unavailable"
// rather than falling back to a partially configured client.
func NewAntomClient(cfg AntomConfig) (*AntomClient, error) {
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("antom config: ClientID is required")
	}
	if cfg.MerchantPrivateKey == "" {
		return nil, fmt.Errorf("antom config: MerchantPrivateKey is required")
	}
	if cfg.AntomPublicKey == "" {
		return nil, fmt.Errorf("antom config: AntomPublicKey is required")
	}

	if cfg.APIBase == "" {
		cfg.APIBase = antomDefaultAPIBase
	}
	cfg.APIBase = strings.TrimRight(cfg.APIBase, "/")
	if !antomAPIBaseAllowed(cfg.APIBase) {
		return nil, fmt.Errorf("antom config: APIBase %q is not an official Antom gateway", cfg.APIBase)
	}
	if cfg.Currency == "" {
		cfg.Currency = antomDefaultCurrency
	}
	if cfg.KeyVersion == "" {
		cfg.KeyVersion = antomDefaultKeyVersion
	}

	privateKey, err := parseAntomPrivateKey(cfg.MerchantPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("antom config: MerchantPrivateKey: %w", err)
	}
	publicKey, err := parseAntomPublicKey(cfg.AntomPublicKey)
	if err != nil {
		return nil, fmt.Errorf("antom config: AntomPublicKey: %w", err)
	}

	return &AntomClient{
		cfg:        cfg,
		privateKey: privateKey,
		publicKey:  publicKey,
		http: &http.Client{
			Timeout: 30 * time.Second,
			// Antom answers in place. Following a redirect would replay the
			// signed body and signature headers to an origin the signature
			// never authorized, so the redirect is returned as the response.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// antomKeyDER accepts the two shapes merchant consoles export: a PEM block or
// the bare base64 DER that block would wrap.
func antomKeyDER(value string) ([]byte, error) {
	if block, _ := pem.Decode([]byte(value)); block != nil {
		return block.Bytes, nil
	}
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("key is neither PEM nor base64 DER")
	}
	return der, nil
}

func parseAntomPrivateKey(value string) (*rsa.PrivateKey, error) {
	der, err := antomKeyDER(value)
	if err != nil {
		return nil, err
	}
	// Antom's console exports PKCS#1, but PKCS#8 is the modern container and
	// keytool/openssl users produce it, so both are accepted.
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		if err := antomCheckKeySize(key.N.BitLen()); err != nil {
			return nil, err
		}
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is %T, want RSA", parsed)
	}
	if err := antomCheckKeySize(key.N.BitLen()); err != nil {
		return nil, err
	}
	return key, nil
}

func parseAntomPublicKey(value string) (*rsa.PublicKey, error) {
	der, err := antomKeyDER(value)
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is %T, want RSA", parsed)
	}
	if err := antomCheckKeySize(key.N.BitLen()); err != nil {
		return nil, err
	}
	return key, nil
}

// antomCheckKeySize rejects a key too weak to carry a payment signature. Antom
// signs with 2048-bit RSA, so a shorter key is a configuration error rather
// than something to discover on the first signed request.
func antomCheckKeySize(bits int) error {
	if bits >= antomMinKeyBits {
		return nil
	}
	return fmt.Errorf("RSA key is %d bits, need at least %d", bits, antomMinKeyBits)
}

// antomSignContent builds the exact bytes Antom signs for a request or a
// notification: "<method> <path>\n<client-id>.<request-time>.<body>". The
// space, the newline and the dots are separators in the protocol, so the body
// must be the exact bytes that travel on the wire.
func antomSignContent(method, path, clientID, requestTime string, body []byte) []byte {
	head := method + " " + path + "\n" + clientID + "." + requestTime + "."
	content := make([]byte, 0, len(head)+len(body))
	content = append(content, head...)
	return append(content, body...)
}

// signatureHeader returns the Antom "signature" header for one request:
// sha256withRSA over antomSignContent, base64-encoded and then query-escaped
// because the header is a comma-separated key/value list in which a raw "+",
// "/" or "=" would break parsing.
func (c *AntomClient) signatureHeader(method, path, requestTime string, body []byte) (string, error) {
	digest := sha256.Sum256(antomSignContent(method, path, c.cfg.ClientID, requestTime, body))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("antom sign: %w", err)
	}
	escaped := url.QueryEscape(base64.StdEncoding.EncodeToString(signature))
	return fmt.Sprintf("algorithm=RSA256,keyVersion=%s,signature=%s", c.cfg.KeyVersion, escaped), nil
}
