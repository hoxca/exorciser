package cmd

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	defaultFilter = "L"
	filterLetters = "LRGBSHO"
)

// filterPattern matches the filter token in a directory component. The
// registered directories use names such as ..._FILTER-L_mono, where the filter
// letter sits between the hyphen and the underscore. FILTER_L is accepted too.
var filterPattern = regexp.MustCompile(`(?i)(?:^|[_-])FILTER[-_]([LRGBSHO])(?:[_-]|$)`)

func normalizeFilter(value string) (string, error) {
	original := strings.TrimSpace(value)
	value = strings.ToUpper(original)
	value = strings.TrimPrefix(value, "FILTER-")
	value = strings.TrimPrefix(value, "FILTER_")
	if len(value) != 1 || !strings.Contains(filterLetters, value) {
		return "", fmt.Errorf("filter must be one of L, R, G, B, S, H or O, got %q", original)
	}
	return value, nil
}

func bareFilter(name string) (string, bool) {
	name = strings.ToUpper(strings.TrimSpace(name))
	if len(name) != 1 || !strings.Contains(filterLetters, name) {
		return "", false
	}
	return name, true
}

// filtersInPath returns every filter named by a path, sorted. Input frames are
// recognised by FILTER-L / FILTER_L directory components. Output paths may also
// use a bare filter directory such as output/L.
func filtersInPath(path string, output bool) []string {
	seen := make(map[string]bool)
	var filters []string
	add := func(letter string) {
		letter = strings.ToUpper(letter)
		if seen[letter] {
			return
		}
		seen[letter] = true
		filters = append(filters, letter)
	}

	clean := filepath.Clean(path)
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		for _, match := range filterPattern.FindAllStringSubmatch(part, -1) {
			add(match[1])
		}
	}
	if output {
		if letter, ok := bareFilter(filepath.Base(filepath.Dir(clean))); ok {
			add(letter)
		}
		if letter, ok := bareFilter(filepath.Base(clean)); ok {
			add(letter)
		}
	}
	slices.Sort(filters)
	return filters
}

// resolveInputFilter selects the files belonging to one filter. When explicit
// is empty the filter is read from the input directories. An explicit filter
// also allows flat input directories, which have no FILTER-<filter> component.
func resolveInputFilter(paths []string, explicit string) (string, []string, error) {
	if len(paths) == 0 {
		return "", nil, errors.New("no .xisf files to analyse")
	}

	explicitFilter := ""
	if explicit != "" {
		var err error
		explicitFilter, err = normalizeFilter(explicit)
		if err != nil {
			return "", nil, fmt.Errorf("--filter: %w", err)
		}
	}

	selected := make([]string, 0, len(paths))
	found := make(map[string]bool)
	skipped := 0
	for _, path := range paths {
		filters := filtersInPath(filepath.Dir(path), false)
		if len(filters) > 1 {
			return "", nil, fmt.Errorf("%s contains conflicting filters %s",
				filepath.Dir(path), strings.Join(filters, ", "))
		}

		if len(filters) == 0 {
			if explicitFilter == "" {
				return "", nil, fmt.Errorf("cannot determine filter from %s: expected a FILTER-<L|R|G|B|S|H|O> directory; pass --filter", path)
			}
			selected = append(selected, path)
			continue
		}

		letter := filters[0]
		if explicitFilter != "" && letter != explicitFilter {
			skipped++
			continue
		}
		selected = append(selected, path)
		found[letter] = true
	}

	if explicitFilter != "" {
		if skipped > 0 {
			slog.Info("files from other filters skipped", "filter", explicitFilter, "files", skipped)
		}
		if len(selected) == 0 {
			return "", nil, fmt.Errorf("no files for filter %s", explicitFilter)
		}
		return explicitFilter, selected, nil
	}

	if len(found) == 0 {
		return "", nil, errors.New("cannot determine filter from the input; pass --filter")
	}
	if len(found) > 1 {
		filters := make([]string, 0, len(found))
		for filter := range found {
			filters = append(filters, filter)
		}
		slices.Sort(filters)
		return "", nil, fmt.Errorf("input spans filters %s; run one filter at a time or pass --filter",
			strings.Join(filters, ", "))
	}
	for filter := range found {
		return filter, selected, nil
	}
	return "", nil, errors.New("cannot determine filter from the input; pass --filter")
}

// outputFilterDir returns the per-filter directory below an output root.
func outputFilterDir(root, filter string) string {
	if root == "" {
		root = "output"
	}
	return filepath.Join(root, filter)
}
