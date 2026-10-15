package execenv

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// defaultAuroraExpertsDir is the directory the runtime image creates and the
// import script fills. The tree is not compiled into the node binary.
const defaultAuroraExpertsDir = "/opt/aurora/experts"

// installAuroraExpertSkills copies one expert's skill tree from the runtime
// directory into the task skills directory. The brief names those directories.
// A missing default directory means this process is not a runtime node, so the
// copy is skipped. An explicit AURORA_EXPERTS_DIR that lacks the expert is an
// error: the model would otherwise be told to read files that are not there.
func installAuroraExpertSkills(skillsDir, skillID string, manifest *sidecarManifest) error {
	if skillID == "" {
		return nil
	}
	if !validAuroraExpertID(skillID) {
		return fmt.Errorf("aurora expert id %q is not a directory name", skillID)
	}
	root, skip, err := auroraExpertsRoot()
	if err != nil {
		return err
	}
	if skip {
		return nil
	}
	src := filepath.Join(root, skillID)
	info, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("aurora expert %q is not installed at %s: %w", skillID, src, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("aurora expert %q path %s is not a directory", skillID, src)
	}
	return copyAuroraTree(skillsDir, src, manifest)
}

func auroraExpertsRoot() (string, bool, error) {
	if dir := os.Getenv("AURORA_EXPERTS_DIR"); dir != "" {
		return dir, false, nil
	}
	info, err := os.Lstat(defaultAuroraExpertsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", true, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("aurora experts path %s is not a directory", defaultAuroraExpertsDir)
	}
	return defaultAuroraExpertsDir, false, nil
}

func validAuroraExpertID(id string) bool {
	return id != "" && !strings.Contains(id, "..") && !strings.ContainsAny(id, `/\\.`)
}

func copyAuroraTree(dstRoot, srcRoot string, manifest *sidecarManifest) error {
	return filepath.WalkDir(srcRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("aurora expert tree refuses symlink %s", path)
		}
		if entry.Name() == "_meta.json" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		dst := filepath.Join(dstRoot, rel)
		if entry.IsDir() {
			return recordMkdirAll(dst, 0o755, manifest)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("aurora expert tree refuses non-regular file %s", path)
		}
		return copyAuroraFile(dst, path, manifest)
	})
}

func copyAuroraFile(dst, src string, manifest *sidecarManifest) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	body, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	perm := info.Mode().Perm()
	if perm == 0 {
		perm = 0o644
	}
	// A shebang script is executed as ./scripts/<name>. The checkout and the
	// import both lose that bit under a normal umask, so the task copy sets it.
	if bytes.HasPrefix(body, []byte("#!")) {
		perm = 0o755
	}
	if err := recordMkdirAll(filepath.Dir(dst), 0o755, manifest); err != nil {
		return err
	}
	if err := recordWriteFile(dst, body, perm, manifest); err != nil {
		return err
	}
	// WriteFile is masked by umask. Chmod is what the model actually executes.
	return os.Chmod(dst, perm)
}
