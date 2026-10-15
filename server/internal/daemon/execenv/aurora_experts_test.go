package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallAuroraExpertSkillsCopiesReferencesAndSkipsProvenance(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AURORA_EXPERTS_DIR", root)
	expert := filepath.Join(root, "ecommerce-bidding-strategy", "ecommerce-bidding-strategy-google-ads")
	writeExpertFile(t, filepath.Join(expert, "SKILL.md"), "---\nname: ecommerce-bidding-strategy-google-ads\n---\n\n# ads\n")
	writeExpertFile(t, filepath.Join(expert, "references", "notes.md"), "# notes\n")
	writeExpertFile(t, filepath.Join(expert, ".learnings", "CHARACTERS.md"), "# characters\n")
	writeExpertFile(t, filepath.Join(expert, "_meta.json"), "{}\n")
	script := filepath.Join(expert, "scripts", "init-novel.sh")
	writeExpertFile(t, script, "#!/bin/bash\necho hi\n")
	if err := os.Chmod(script, 0o644); err != nil {
		t.Fatal(err)
	}

	skillsDir := filepath.Join(t.TempDir(), ".claude", "skills")
	if err := installAuroraExpertSkills(skillsDir, "ecommerce-bidding-strategy", nil); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(skillsDir, "ecommerce-bidding-strategy-google-ads", "references", "notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# notes\n" {
		t.Fatalf("notes = %q", got)
	}
	if _, err := os.ReadFile(filepath.Join(skillsDir, "ecommerce-bidding-strategy-google-ads", ".learnings", "CHARACTERS.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "ecommerce-bidding-strategy-google-ads", "_meta.json")); !os.IsNotExist(err) {
		t.Fatalf("_meta.json was copied: %v", err)
	}
	info, err := os.Stat(filepath.Join(skillsDir, "ecommerce-bidding-strategy-google-ads", "scripts", "init-novel.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("script mode = %o, want execute bit", info.Mode().Perm())
	}
	notes, err := os.Stat(filepath.Join(skillsDir, "ecommerce-bidding-strategy-google-ads", "references", "notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if notes.Mode().Perm()&0o111 != 0 {
		t.Fatalf("notes mode = %o, want no execute bit", notes.Mode().Perm())
	}
}

func TestInstallAuroraExpertSkillsRequiresTheRequestedExpert(t *testing.T) {
	t.Setenv("AURORA_EXPERTS_DIR", t.TempDir())
	err := installAuroraExpertSkills(t.TempDir(), "missing-expert", nil)
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("error = %v, want not installed", err)
	}
}

func TestInstallAuroraExpertSkillsRejectsASymlink(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AURORA_EXPERTS_DIR", root)
	expert := filepath.Join(root, "demo")
	if err := os.MkdirAll(expert, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(expert, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	err := installAuroraExpertSkills(t.TempDir(), "demo", nil)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v, want symlink refusal", err)
	}
}

func TestReuseDeclinesWhenAuroraExpertInstallFails(t *testing.T) {
	t.Setenv("AURORA_EXPERTS_DIR", t.TempDir())
	root := t.TempDir()
	workDir := filepath.Join(root, "ws", "task")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	env := Reuse(ReuseParams{
		WorkDir:  workDir,
		Provider: "claude",
		Task:     TaskContextForEnv{AuroraSkillID: "ecommerce-bidding-strategy"},
	}, testLogger())
	if env != nil {
		t.Fatal("Reuse continued after the expert tree was missing")
	}
}

func writeExpertFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
