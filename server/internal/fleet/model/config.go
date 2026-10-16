package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
)

// Config is administrator-owned public configuration. Credentials belong in Bootstrap.
type Config struct {
	Namespace string          `json:"namespace"`
	FleetID   string          `json:"fleet_id"`
	Image     string          `json:"image"`
	APIURL    string          `json:"api_url"`
	Specs     map[string]Spec `json:"specs"`
	MaxNodes  int             `json:"max_nodes"`
	// NetworkPool optionally pins the address space node networks are created in,
	// as one IPv4 CIDR of /24 or wider (for example "10.213.0.0/16"). Each node
	// then owns one deterministic /24 of it. Empty keeps Docker's automatic pool,
	// which can overlap the host's own LAN: one node network took 192.168.0.0/20
	// and swallowed the route to the operator's 192.168.15.0/24, leaving that LAN
	// unreachable until the network was deleted.
	NetworkPool string `json:"network_pool"`
	// Aurora, when present, selects the managed-sandbox execution profile.
	// The tag carries no omitempty because DecodeStrictObject matches exact
	// json tag values when it rejects unknown administrator keys.
	Aurora *AuroraConfig `json:"aurora"`
}

// parseNetworkPool accepts one IPv4 CIDR wide enough to hold at least one /24.
func parseNetworkPool(value string) (*net.IPNet, error) {
	_, pool, err := net.ParseCIDR(strings.TrimSpace(value))
	if err != nil || pool.IP.To4() == nil {
		return nil, fmt.Errorf("%w: network_pool must be an IPv4 CIDR", ErrInvalidRequest)
	}
	ones, bits := pool.Mask.Size()
	if bits != 32 || ones > 24 {
		return nil, fmt.Errorf("%w: network_pool must be /24 or wider", ErrInvalidRequest)
	}
	return pool, nil
}

type Spec struct {
	CPUs        int   `json:"cpus"`
	MemoryBytes int64 `json:"memory_bytes"`
	Pids        int64 `json:"pids"`
	MaxRuns     int   `json:"max_runs"`
}

// LoadConfig reads only the explicitly supplied JSON file, without environment or home discovery.
func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return DecodeConfig(raw)
}

// DecodeConfig parses checked bytes with the same strict schema and resource defaults.
func DecodeConfig(raw []byte) (Config, error) {
	cfg := Config{MaxNodes: 2}
	fields, err := DecodeStrictObject(raw, &cfg)
	if err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(cfg.Namespace) == "" || strings.TrimSpace(cfg.FleetID) == "" ||
		strings.TrimSpace(cfg.Image) == "" || strings.TrimSpace(cfg.APIURL) == "" || cfg.MaxNodes <= 0 || len(cfg.Specs) == 0 {
		return Config{}, fmt.Errorf("%w: required config identity, image, API URL, specs and positive node limit", ErrInvalidRequest)
	}
	if cfg.NetworkPool != "" {
		pool, err := parseNetworkPool(cfg.NetworkPool)
		if err != nil {
			return Config{}, err
		}
		cfg.NetworkPool = pool.String()
	}
	if cfg.Aurora != nil {
		if err := cfg.Aurora.Validate(); err != nil {
			return Config{}, err
		}
	}
	var specs map[string]json.RawMessage
	if _, err := DecodeStrictObject(fields["specs"], &specs); err != nil {
		return Config{}, ErrInvalidRequest
	}
	for name, rawSpec := range specs {
		if strings.TrimSpace(name) == "" {
			return Config{}, fmt.Errorf("%w: spec name required", ErrInvalidRequest)
		}
		spec := Spec{CPUs: 2, MemoryBytes: 4 * 1024 * 1024 * 1024, Pids: 256, MaxRuns: 1}
		if _, err := DecodeStrictObject(rawSpec, &spec); err != nil {
			return Config{}, err
		}
		if spec.CPUs <= 0 || spec.MemoryBytes <= 0 || spec.Pids <= 0 || spec.MaxRuns <= 0 {
			return Config{}, fmt.Errorf("%w: resource limits must be positive", ErrInvalidRequest)
		}
		cfg.Specs[name] = spec
	}
	return cfg, nil
}

// DecodeStrictObject rejects unknown/duplicate fields, explicit nulls and extra JSON documents.
// Parser diagnostics are not returned, so untrusted field names/values cannot leak into logs.
func DecodeStrictObject(raw []byte, dst any) (map[string]json.RawMessage, error) {
	reader := json.NewDecoder(bytes.NewReader(raw))
	if token, err := reader.Token(); err != nil || token != json.Delim('{') {
		return nil, ErrInvalidRequest
	}
	fields := make(map[string]json.RawMessage)
	for reader.More() {
		token, err := reader.Token()
		if err != nil {
			return nil, ErrInvalidRequest
		}
		key, ok := token.(string)
		if !ok {
			return nil, ErrInvalidRequest
		}
		if _, exists := fields[key]; exists {
			return nil, ErrInvalidRequest
		}
		var value json.RawMessage
		if err := reader.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, ErrInvalidRequest
		}
		fields[key] = value
	}
	if token, err := reader.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrInvalidRequest
	}
	if err := reader.Decode(new(any)); err != io.EOF {
		return nil, ErrInvalidRequest
	}
	// encoding/json otherwise accepts case-insensitive aliases of struct fields.
	// Derive exact keys from the schema tags instead of maintaining a second schema.
	if schema := reflect.TypeOf(dst).Elem(); schema.Kind() == reflect.Struct {
		allowed := make(map[string]bool, schema.NumField())
		for i := 0; i < schema.NumField(); i++ {
			allowed[schema.Field(i).Tag.Get("json")] = true
		}
		for key := range fields {
			if !allowed[key] {
				return nil, ErrInvalidRequest
			}
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return nil, ErrInvalidRequest
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, ErrInvalidRequest
	}
	return fields, nil
}
