package aurora

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedExpertTreeHasNoSubskillFiles(t *testing.T) {
	err := fs.WalkDir(expertBundle, "experts", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(path, "subskills") || strings.HasPrefix(path, "aurora-experts") {
			t.Errorf("compiled expert tree contains %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
