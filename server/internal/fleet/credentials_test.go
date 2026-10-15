package fleet

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/fleet/model"
)

// A permissive decoder or insecure-file acceptance would expose private material.
func TestLoadProfileStrictPrivateBoundary(t *testing.T) {
	captured := captureSlog(t)
	oldLog := log.Writer()
	log.SetOutput(captured)
	t.Cleanup(func() { log.SetOutput(oldLog) })
	defer func() {
		if strings.Contains(captured.String(), "fake-marker-secret") || strings.Contains(captured.String(), "mcn_marker") || strings.Contains(captured.String(), "private-marker.json") {
			t.Fatal("private loader logged marker material")
		}
	}()
	for _, tc := range []struct {
		name, raw string
		mode      os.FileMode
		valid     bool
	}{
		{"valid", `{"api_key":"fake-marker-secret","base_url":"https://provider.test/api/plan","model":"fake-model"}`, 0600, true},
		{"http", `{"api_key":"fake-marker-secret","base_url":"http://provider.test/api/plan"}`, 0600, true},
		{"readonly", `{"api_key":"fake-marker-secret"}`, 0400, true},
		{"world", `{"api_key":"fake-marker-secret"}`, 0644, false},
		{"no owner read", `{"api_key":"fake-marker-secret"}`, 0200, false},
		{"unknown", `{"api_key":"fake-marker-secret","owner_id":"marker"}`, 0600, false},
		{"bootstrap token", `{"api_key":"fake-marker-secret","node_token":"mcn_marker"}`, 0600, false},
		{"duplicate", `{"api_key":"fake-marker-secret","api_key":"another"}`, 0600, false},
		{"case alias", `{"API_KEY":"fake-marker-secret"}`, 0600, false},
		{"extra doc", `{"api_key":"fake-marker-secret"}{}`, 0600, false},
		{"null", `{"api_key":null}`, 0600, false},
		{"blank", `{"api_key":" "}`, 0600, false},
		{"control", `{"api_key":"fake-marker-secret\n"}`, 0600, false},
		{"blank model", `{"api_key":"fake-marker-secret","model":" "}`, 0600, false},
		{"null model", `{"api_key":"fake-marker-secret","model":null}`, 0600, false},
		{"model control", `{"api_key":"fake-marker-secret","model":"x\u007f"}`, 0600, false},
		{"blank url", `{"api_key":"fake-marker-secret","base_url":" "}`, 0600, false},
		{"userinfo", `{"api_key":"fake-marker-secret","base_url":"https://user:secret@provider.test"}`, 0600, false},
		{"query", `{"api_key":"fake-marker-secret","base_url":"https://provider.test?key=marker"}`, 0600, false},
		{"fragment", `{"api_key":"fake-marker-secret","base_url":"https://provider.test#marker"}`, 0600, false},
		{"relative url", `{"api_key":"fake-marker-secret","base_url":"/api/plan"}`, 0600, false},
		{"wrong scheme", `{"api_key":"fake-marker-secret","base_url":"file:///tmp/key"}`, 0600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private-marker.json")
			if e := os.WriteFile(path, []byte(tc.raw), tc.mode); e != nil {
				t.Fatal(e)
			}
			b, e := LoadProfile(path)
			if tc.valid {
				if e != nil || b.APIKey != "fake-marker-secret" || b.NodeToken != "" || b.ServerURL != "" || b.DaemonID != "" {
					t.Fatal("valid profile boundary")
				}
				if tc.name == "valid" && (b.BaseURL != "https://provider.test/api/plan" || b.Model != "fake-model") {
					t.Fatal("provider routing fields lost")
				}
			} else if !errors.Is(e, model.ErrProfileMissing) || strings.Contains(e.Error(), "marker") || strings.Contains(e.Error(), path) {
				t.Fatalf("unsanitized error=%v", e)
			}
		})
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "private")
	if e := os.WriteFile(path, []byte(`{"api_key":"fake-marker-secret"}`), 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(dir, "link")
	if e := os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{link, dir, "relative-private.json", filepath.Join(dir, "missing")} {
		if _, e := LoadProfile(bad); !errors.Is(e, model.ErrProfileMissing) {
			t.Fatalf("invalid path accepted=%v", e)
		}
	}
}
