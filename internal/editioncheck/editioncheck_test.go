package editioncheck

import (
	"io/fs"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

func TestLocaleCatalogsLoad(t *testing.T) {
	n := 0
	err := fs.WalkDir(i18n.Embedded(), ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	})
	if err != nil || n == 0 {
		t.Fatalf("no locale catalogs found (%d): %v", n, err)
	}
}
