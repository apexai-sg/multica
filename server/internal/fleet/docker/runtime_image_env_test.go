package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// TestRuntimeImageEnvironmentStaysAdoptable guards the contract between the node
// image and the Fleet's environment allowlist: every container the Fleet builds
// from that image inherits the image's ENV -- including the bootstrap helper,
// which runs with no Aurora profile and nothing added -- and inspectEnvironment
// rejects any key it does not know. A variable the image gains without an
// allowlist entry therefore fails every node create with ErrForbidden.
func TestRuntimeImageEnvironmentStaysAdoptable(t *testing.T) {
	imageEnv := runtimeImageEnv(t)

	// The bootstrap helper: this image, no Aurora profile, nothing added.
	if !inspectEnvironment(imageEnv, 0, nil) {
		t.Fatalf("docker/runtime/Dockerfile bakes an environment the Fleet rejects for a bootstrap helper: %v", imageEnv)
	}

	// The node container: the image plus what the provider adds, merged the way
	// the container actually starts (the container's own value wins per key, and
	// no key appears twice). The container side comes from nodeEnv, so a key the
	// provider learns to set is covered here without editing this test.
	cfg := auroraConfig()
	n := auroraNode()
	if !inspectEnvironment(mergeEnv(imageEnv, (&Provider{cfg: cfg}).nodeEnv(n)), n.Resources.MaxRuns, cfg.Aurora) {
		t.Fatalf("docker/runtime/Dockerfile bakes an environment the Fleet rejects for an Aurora node: %v", imageEnv)
	}
}

// mergeEnv models the environment a container starts with: the image's
// declarations overridden by the container's own, one entry per key.
func mergeEnv(imageEnv, containerEnv []string) []string {
	at := map[string]int{}
	var merged []string
	for _, entry := range imageEnv {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		at[key] = len(merged)
		merged = append(merged, entry)
	}
	for _, entry := range containerEnv {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if i, seen := at[key]; seen {
			merged[i] = entry
			continue
		}
		at[key] = len(merged)
		merged = append(merged, entry)
	}
	return merged
}

// runtimeImageEnv reads the ENV pairs the final stage of the node image declares.
//
// Only the forms the file actually uses are parsed: "ENV K=V" with one or more
// pairs on the line. Anything else -- a continued line, or the legacy "ENV K V"
// spelling -- fails the test rather than being skipped, so this guard cannot go
// quiet while the file changes shape. Stages before the last FROM are build
// stages and never reach a container, so they are ignored.
func runtimeImageEnv(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "docker", "runtime", "Dockerfile")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(raw), "\n")
	final := 0
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "FROM ") {
			final = i
		}
	}
	var env []string
	for i := final; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		// Dockerfile keywords are case-insensitive, so an "env" spelling must not
		// slip past this guard: anything introduced by ENV -- however cased -- is
		// either parsed below or fails.
		if len(line) < 3 || !strings.EqualFold(line[:3], "ENV") || (len(line) > 3 && !unicode.IsSpace(rune(line[3]))) {
			continue
		}
		if !strings.HasPrefix(line, "ENV ") {
			t.Fatalf("%s:%d: ENV spelling %q is not parsed; extend runtimeImageEnv", path, i+1, line)
		}
		decl := strings.TrimSpace(strings.TrimPrefix(line, "ENV "))
		if strings.HasSuffix(decl, "\\") {
			t.Fatalf("%s:%d: continued ENV lines are not parsed; extend runtimeImageEnv", path, i+1)
		}
		for _, field := range strings.Fields(decl) {
			if _, _, ok := strings.Cut(field, "="); !ok {
				t.Fatalf("%s:%d: ENV entry %q is not KEY=VALUE; extend runtimeImageEnv", path, i+1, field)
			}
			env = append(env, field)
		}
	}
	if len(env) == 0 {
		t.Fatalf("%s declares no ENV in its final stage; this guard would be vacuous", path)
	}
	return env
}
