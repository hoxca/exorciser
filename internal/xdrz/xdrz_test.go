package xdrz_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoxca/exorciser/internal/xdrz"
)

func writeXDRZ(t *testing.T, matrix string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "frame.xdrz")
	text := `<?xml version="1.0" encoding="UTF-8"?>
<xdrz version="1.1" xmlns="http://www.pixinsight.com/xdrz">
<SourceImage>/data/source.xisf</SourceImage>
<AlignmentTargetImage>/data/registered.xisf</AlignmentTargetImage>
<ReferenceGeometry width="4096" height="4096" numberOfChannels="1"/>
<AlignmentMatrix>` + matrix + `</AlignmentMatrix>
</xdrz>`
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadAndMap(t *testing.T) {
	path := writeXDRZ(t, "1,0,10,0,1,20,0,0,1")
	d, err := xdrz.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if d.SourceImage != "/data/source.xisf" {
		t.Errorf("SourceImage = %q", d.SourceImage)
	}
	x, y := d.ToSource(5, 6)
	if x != 15 || y != 26 {
		t.Errorf("ToSource = %v,%v, want 15,26", x, y)
	}
	x, y = d.ToTarget(15, 26)
	if x != 5 || y != 6 {
		t.Errorf("ToTarget = %v,%v, want 5,6", x, y)
	}
}

func TestReadRejectsSingularMatrix(t *testing.T) {
	path := writeXDRZ(t, "1,0,0,2,0,0,0,0,1")
	if _, err := xdrz.Read(path); err == nil {
		t.Fatal("Read accepted a singular matrix")
	}
}

func TestCopyWithSource(t *testing.T) {
	src := writeXDRZ(t, "1,0,0,0,1,0,0,0,1")
	dst := filepath.Join(t.TempDir(), "copy.xdrz")
	newSource := "/data/clean & calibrated.xisf"
	if err := xdrz.CopyWithSource(src, dst, newSource); err != nil {
		t.Fatalf("CopyWithSource: %v", err)
	}
	raw, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "<SourceImage>/data/clean &amp; calibrated.xisf</SourceImage>") {
		t.Errorf("SourceImage was not replaced and escaped:\n%s", text)
	}
	if !strings.Contains(text, "<AlignmentMatrix>1,0,0,0,1,0,0,0,1</AlignmentMatrix>") {
		t.Error("alignment matrix was not preserved")
	}
}

func TestRotation180(t *testing.T) {
	path := writeXDRZ(t, "-1,0,4096,0,-1,4096,0,0,1")
	d, err := xdrz.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	x, y := d.ToSource(0, 0)
	if x != 4096 || y != 4096 {
		t.Errorf("ToSource(0,0) = %v,%v, want 4096,4096", x, y)
	}
	x, y = d.ToTarget(4096, 4096)
	if x != 0 || y != 0 {
		t.Errorf("ToTarget(4096,4096) = %v,%v, want 0,0", x, y)
	}
}
