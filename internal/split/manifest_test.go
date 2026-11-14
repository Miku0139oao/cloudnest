package split

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateManifestStrictness(t *testing.T) {
	f := newHashedFixture(t, transferTestSize, "validate.bin")
	if err := ValidateManifest(f.m); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	clone := func() Manifest {
		c := f.m
		c.Parts = append([]PartInfo(nil), f.m.Parts...)
		return c
	}

	cases := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"wrong format", func(m *Manifest) { m.Format = "other/1" }},
		{"zero size", func(m *Manifest) { m.Size = 0 }},
		{"zero part count", func(m *Manifest) { m.PartCount = 0; m.Parts = nil }},
		{"part count too large", func(m *Manifest) { m.PartCount = 1001 }},
		{"part count mismatch", func(m *Manifest) { m.PartCount++ }},
		{"zero chunk size", func(m *Manifest) { m.ChunkSize = 0 }},
		{"bad whole sha", func(m *Manifest) { m.SHA256 = "abc" }},
		{"bad part index", func(m *Manifest) { m.Parts[1].Index = 7 }},
		{"bad part name pattern", func(m *Manifest) { m.Parts[0].Name = "part-1.bin" }},
		{"name does not match index", func(m *Manifest) { m.Parts[0].Name = partName(2) }},
		{"zero part size", func(m *Manifest) { m.Parts[0].Size = 0 }},
		{"sum mismatch", func(m *Manifest) { m.Parts[0].Size++ }},
		{"bad part sha", func(m *Manifest) { m.Parts[0].SHA256 = strings.Repeat("z", 64) }},
		{"manifest too large", func(m *Manifest) {
			m.SourceRel = strings.Repeat("a", maxManifestBytes)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := clone()
			tc.mutate(&m)
			if err := ValidateManifest(m); err == nil {
				t.Fatalf("ValidateManifest accepted %+v", m)
			}
		})
	}
}

func TestInspectStates(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		info, err := InspectDir(filepath.Join(t.TempDir(), "nope"))
		if err != nil {
			t.Fatal(err)
		}
		if info.State != SetAbsent {
			t.Fatalf("state = %q, want absent", info.State)
		}
	})

	t.Run("foreign directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := InspectDir(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.State != SetForeign || !info.Foreign {
			t.Fatalf("info = %+v, want foreign", info)
		}
	})

	t.Run("foreign manifest", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"format":"other/2"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := InspectDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.State != SetForeign || !info.Foreign || info.Manifest == nil {
			t.Fatalf("info = %+v, want foreign with manifest", info)
		}
	})

	t.Run("corrupt json", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"format":`), 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := InspectDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.State != SetCorrupt {
			t.Fatalf("state = %q, want corrupt", info.State)
		}
	})

	t.Run("invalid manifest json", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"format":"cloudbox-split/1","size":1}`), 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := InspectDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.State != SetCorrupt {
			t.Fatalf("state = %q, want corrupt", info.State)
		}
	})

	t.Run("wrong sized part", func(t *testing.T) {
		f := newHashedFixture(t, transferTestSize, "wrong-size.bin")
		f.stageAll(t)
		part := f.m.Parts[0]
		path := filepath.Join(f.partsDir, part.Name)
		if err := os.Truncate(path, part.Size-1); err != nil {
			t.Fatal(err)
		}
		info, err := InspectDir(f.partsDir)
		if err != nil {
			t.Fatal(err)
		}
		if info.State != SetCorrupt {
			t.Fatalf("state = %q, want corrupt", info.State)
		}
	})
}

func TestReadManifestFileErrors(t *testing.T) {
	if _, err := ReadManifestFile(t.TempDir()); err == nil {
		t.Fatal("missing manifest should return an error")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"format":"cloudbox-split/1","extra":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifestFile(dir); err == nil {
		t.Fatal("unknown manifest field should be rejected")
	}
}

func TestWriteMetaWritesReadmeAndNoTemps(t *testing.T) {
	f := newHashedFixture(t, transferTestSize, "meta.bin")
	dir := filepath.Join(f.root, "meta-parts")
	if err := WriteMeta(dir, f.m, ""); err != nil {
		t.Fatal(err)
	}

	readme, err := os.ReadFile(filepath.Join(dir, "README.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(readme) == 0 {
		t.Fatal("README.txt is empty")
	}
	if _, err := ReadManifestFile(dir); err != nil {
		t.Fatalf("manifest did not read back: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}
