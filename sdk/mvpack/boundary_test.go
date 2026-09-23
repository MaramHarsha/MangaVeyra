// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

package mvpack

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestSDKBoundary keeps sdk/ a clean MIT boundary: every Go file carries the
// MIT SPDX header and imports only the standard library or other sdk/
// packages, never AGPL code from the rest of the repository or third-party
// modules.
func TestSDKBoundary(t *testing.T) {
	const sdkPath = "github.com/MaramHarsha/MangaVeyra/sdk/"
	root := filepath.Join("..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(string(src), "// SPDX-License-Identifier: MIT\n") {
			t.Errorf("%s: missing the MIT SPDX header on the first line", path)
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			first, _, _ := strings.Cut(p, "/")
			if strings.Contains(first, ".") && !strings.HasPrefix(p, sdkPath) {
				t.Errorf("%s imports %q: sdk/ may only use the standard library and other sdk/ packages", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
