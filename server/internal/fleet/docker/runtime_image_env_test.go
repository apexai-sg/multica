package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/fleet/model"
)

// TestRuntimeImageEnvironmentStaysAdoptable guards the contract between the node
// image and the Fleet's environment allowlist. Every container the Fleet builds
// from that image inherits its ENV -- including the bootstrap helper, which runs
// with no Aurora profile and no extra environment -- and inspectEnvironment
// rejects any key it does not know.
//
// This is not hypothetical: #258 added AURORA_EXPERTS_DIR to the image without an
// allowlist entry, so validateHelper answered ErrForbidden for every helper and
// no node could ever be created, while the Fleet's operation rows only carried
// error_code=forbidden. #264 accepted the key; this test is what would have
// caught it in review.
func TestRuntimeImageEnvironmentStaysAdoptable(t *testing.T) {
	imageEnv := runtimeImageEnv(t)

	// The bootstrap helper: this image, no Aurora profile, nothing added.
	if !inspectEnvironment(imageEnv, 0, nil) {
		t.Fatalf("docker/runtime/Dockerfile bakes an environment the Fleet rejects for a bootstrap helper: %v", imageEnv)
	}

	// The node container: the same image plus exactly what the Fleet adds.
	cfg := auroraConfig()
	nodeEnv := append([]string{}, imageEnv...)
	nodeEnv = append(nodeEnv,
		"FLEET_NODE_MAX_RUNS=1",
		model.AuroraManagedEnv+"=1",
		model.AuroraServerURLEnv+"="+cfg.Aurora.ServerURL,
		model.AuroraEnrollmentFileEnv+"="+model.AuroraEnrollmentFile,
		model.AuroraClaudePathEnv+"="+model.AuroraClaudePath,
		model.AuroraAnthropicBaseURLEnv+"="+cfg.Aurora.AnthropicBaseURL,
		model.AuroraAnthropicModelEnv+"="+cfg.Aurora.AnthropicModel,
	)
	if !inspectEnvironment(nodeEnv, 1, cfg.Aurora) {
		t.Fatalf("docker/runtime/Dockerfile bakes an environment the Fleet rejects for an Aurora node: %v", imageEnv)
	}
}

// runtimeImageEnv reads the ENV pairs the node image declares.
//
// Only the forms the file actually uses are parsed: "ENV K=V" with one or more
// pairs on the line. Anything else -- a continued line, or the legacy "ENV K V"
// spelling -- fails the test rather than being skipped, so this guard cannot go
// quiet while the file changes shape.
func runtimeImageEnv(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "docker", "runtime", "Dockerfile")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var env []string
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ENV ") {
			continue
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
		t.Fatalf("%s declares no ENV; this guard would be vacuous", path)
	}
	return env
}
