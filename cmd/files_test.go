package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCollect(t *testing.T) {
	root := t.TempDir()
	files := []string{
		"a.xisf",
		"b.XISF",
		"skip.xdrz",
		"._a.xisf",
		filepath.Join("sub", "c.xisf"),
	}
	for _, name := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := collect([]string{root})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	want := []string{"a.xisf", "b.XISF", "c.xisf"}
	if len(got) != len(want) {
		t.Fatalf("collect = %v, want %d files", got, len(want))
	}
	for i, name := range want {
		if base := filepath.Base(got[i]); base != name {
			t.Errorf("collect[%d] = %q, want %q", i, base, name)
		}
	}

	// The same file reached twice is counted once.
	deduped, err := collect([]string{root, root, filepath.Join(root, "a.xisf")})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(deduped) != len(want) {
		t.Fatalf("deduplicated collect = %v, want %d files", deduped, len(want))
	}

	if _, err := collect([]string{filepath.Join(root, "missing")}); err == nil {
		t.Fatal("collect on a missing root: want error")
	}
}

func TestDuplicateNames(t *testing.T) {
	names := []string{"b.xisf", "a.xisf", "a.xisf", "c.xisf", ""}
	got := duplicateNames(names)
	if len(got) != 1 {
		t.Fatalf("duplicateNames = %+v, want one duplicate", got)
	}
	if got[0].name != "a.xisf" || got[0].count != 2 {
		t.Fatalf("duplicateNames = %+v, want a.xisf ×2", got)
	}
}
