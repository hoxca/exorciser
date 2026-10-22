package cmd

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestNormalizeFilter(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "L", want: "L"},
		{in: "r", want: "R"},
		{in: " FILTER-B ", want: "B"},
		{in: "FILTER_h", want: "H"},
		{in: "G", want: "G"},
		{in: "S", want: "S"},
		{in: "O", want: "O"},
		{in: "", wantErr: true},
		{in: "LR", wantErr: true},
		{in: "X", wantErr: true},
		{in: "FILTER-LIGHT", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := normalizeFilter(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeFilter(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeFilter(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeFilter(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFiltersInPath(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		output bool
		want   []string
	}{
		{
			name: "registered hyphen",
			path: "/data/Light_BIN-1_4096x4096_EXPOSURE-600.00s_FILTER-L_mono/frame.xisf",
			want: []string{"L"},
		},
		{
			name: "registered underscore",
			path: "FILTER_R/frame.xisf",
			want: []string{"R"},
		},
		{
			name: "lowercase",
			path: "x/filter-b_mono/frame.xisf",
			want: []string{"B"},
		},
		{
			name: "filter at end",
			path: "x/FILTER-O",
			want: []string{"O"},
		},
		{
			name: "no token",
			path: "frames/frame.xisf",
		},
		{
			name: "longer word is not a filter",
			path: "FILTER-LIGHT/frame.xisf",
		},
		{
			name:   "bare output directory",
			path:   "output/R/artefacts.csv",
			output: true,
			want:   []string{"R"},
		},
		{
			name:   "output with filter token",
			path:   "output/FILTER-H/artefacts.csv",
			output: true,
			want:   []string{"H"},
		},
		{
			name:   "output without filter",
			path:   "output/artefacts.csv",
			output: true,
		},
		{
			name: "conflicting filters",
			path: "data/FILTER-L_mono/FILTER-R_mono/frame.xisf",
			want: []string{"L", "R"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filtersInPath(tt.path, tt.output)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("filtersInPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestResolveInputFilter(t *testing.T) {
	l := filepath.Join("data", "FILTER-L_mono", "l.xisf")
	r := filepath.Join("data", "FILTER-R_mono", "r.xisf")
	flat := filepath.Join("data", "flat.xisf")

	tests := []struct {
		name       string
		paths      []string
		explicit   string
		wantFilter string
		wantPaths  []string
		wantErr    bool
	}{
		{
			name:       "one filter",
			paths:      []string{l},
			wantFilter: "L",
			wantPaths:  []string{l},
		},
		{
			name:    "mixed filters need a selection",
			paths:   []string{l, r},
			wantErr: true,
		},
		{
			name:    "flat input needs an explicit filter",
			paths:   []string{flat},
			wantErr: true,
		},
		{
			name:       "explicit filter selects from a parent",
			paths:      []string{l, r},
			explicit:   "R",
			wantFilter: "R",
			wantPaths:  []string{r},
		},
		{
			name:       "explicit filter allows a flat input",
			paths:      []string{flat},
			explicit:   "L",
			wantFilter: "L",
			wantPaths:  []string{flat},
		},
		{
			name:     "explicit filter with no matching files",
			paths:    []string{l},
			explicit: "R",
			wantErr:  true,
		},
		{
			name:     "invalid explicit filter",
			paths:    []string{l},
			explicit: "X",
			wantErr:  true,
		},
		{
			name:    "conflicting filters in one path",
			paths:   []string{filepath.Join("data", "FILTER-L_mono", "FILTER-R_mono", "x.xisf")},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter, paths, err := resolveInputFilter(tt.paths, tt.explicit)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveInputFilter(%v, %q) = %q, %v, want error",
						tt.paths, tt.explicit, filter, paths)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveInputFilter(%v, %q): %v", tt.paths, tt.explicit, err)
			}
			if filter != tt.wantFilter {
				t.Fatalf("filter = %q, want %q", filter, tt.wantFilter)
			}
			if !slices.Equal(paths, tt.wantPaths) {
				t.Fatalf("paths = %v, want %v", paths, tt.wantPaths)
			}
		})
	}
}

func TestOutputFilterDir(t *testing.T) {
	if got, want := outputFilterDir("out", "L"), filepath.Join("out", "L"); got != want {
		t.Errorf("outputFilterDir(\"out\", \"L\") = %q, want %q", got, want)
	}
	if got, want := outputFilterDir("", "R"), filepath.Join("output", "R"); got != want {
		t.Errorf("outputFilterDir(\"\", \"R\") = %q, want %q", got, want)
	}
}
