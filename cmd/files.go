package cmd

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// defaultInput is the light frame directory this tool was written for.
const defaultInput = "img/registered/Light_BIN-1_4096x4096_EXPOSURE-600.00s_FILTER-L_mono"

// collect resolves roots into a sorted, de-duplicated list of .xisf files.
// Directories are walked recursively.
func collect(roots []string) ([]string, error) {
	var paths []string
	seen := make(map[string]bool)

	add := func(path string) {
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = filepath.Clean(path)
		}
		if seen[abs] {
			return
		}
		seen[abs] = true
		paths = append(paths, path)
	}

	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if isXISF(root) {
				add(root)
			}
			continue
		}

		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !isXISF(path) {
				return nil
			}
			add(path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	slices.Sort(paths)
	return paths, nil
}

func isXISF(path string) bool {
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") {
		// Skip ._foo.xisf resource forks and other hidden files.
		return false
	}
	return strings.EqualFold(filepath.Ext(base), ".xisf")
}

// fileCount is a filename and how many images carry it.
type fileCount struct {
	name  string
	count int
}

// duplicateNames returns the non-empty names used more than once, sorted. The
// report identifies images by filename alone, so callers warn about these to
// avoid ambiguous rows.
func duplicateNames(names []string) []fileCount {
	counts := make(map[string]int, len(names))
	for _, name := range names {
		if name != "" {
			counts[name]++
		}
	}

	var dups []fileCount
	for name, count := range counts {
		if count > 1 {
			dups = append(dups, fileCount{name: name, count: count})
		}
	}
	slices.SortFunc(dups, func(a, b fileCount) int { return strings.Compare(a.name, b.name) })
	return dups
}
