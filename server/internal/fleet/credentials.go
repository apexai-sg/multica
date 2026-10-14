package fleet

import (
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/util"
)

// LoadProfile reads an explicit private file, never HOME, OAuth or environment credentials.
// Private profiles contain provider inputs only; runtime identity is supplied by trusted producers.
// All file/parser/validation errors are sanitized to avoid logging keys, values or private paths.
func LoadProfile(path string) (model.Bootstrap, error) {
	fail := func() (model.Bootstrap, error) { return model.Bootstrap{}, model.ErrProfileMissing }
	if !filepath.IsAbs(path) {
		return fail()
	}
	// The final-component boundary is enforced at open, not merely in a racy
	// Lstat; util.OpenPrivateFile owns the per-platform spelling.
	file, err := util.OpenPrivateFile(path)
	if err != nil {
		return fail()
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0400 == 0 || info.Mode().Perm()&0077 != 0 {
		return fail()
	}
	const maxProfileBytes = 1024 * 1024
	raw, err := io.ReadAll(io.LimitReader(file, maxProfileBytes+1))
	if err != nil || len(raw) > maxProfileBytes {
		return fail()
	}
	var private struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
	}
	fields, err := model.DecodeStrictObject(raw, &private)
	if err != nil || !validPrivateString(private.APIKey) {
		return fail()
	}
	if _, ok := fields["model"]; ok && !validPrivateString(private.Model) {
		return fail()
	}
	if _, ok := fields["base_url"]; ok {
		u, e := url.Parse(private.BaseURL)
		if e != nil || !validPrivateString(private.BaseURL) || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(private.BaseURL, "#") {
			return fail()
		}
	}
	return model.Bootstrap{APIKey: private.APIKey, BaseURL: private.BaseURL, Model: private.Model}, nil
}
func validPrivateString(value string) bool {
	return strings.TrimSpace(value) != "" && strings.IndexFunc(value, unicode.IsControl) < 0
}
