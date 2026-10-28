package packages

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalPackageLifecycleAndContainment(t *testing.T) {
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(source, "loop.json")
	if err := os.WriteFile(manifest, []byte(`{"api_version":1,"name":"fixture","version":"1","skills":["skills"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Directory: t.TempDir()}
	installed, err := manager.Install(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Name != "fixture" {
		t.Fatal("manifest was not loaded")
	}
	if err := os.WriteFile(manifest, []byte(`{"api_version":1,"name":"fixture","version":"2","skills":["skills"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Update(context.Background(), "fixture"); err != nil {
		t.Fatal(err)
	}
	entries, err := manager.List()
	if err != nil || len(entries) != 1 || entries[0].Version != "2" {
		t.Fatalf("update: %+v %v", entries, err)
	}
	if err := manager.Remove("fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(manifest); err != nil {
		t.Fatal("removal deleted a local package")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(source, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResourcePath(source, "outside"); err == nil {
		t.Fatal("symlink escaped package root")
	}
}

func TestArchiveRejectsTraversalAndLinks(t *testing.T) {
	for _, header := range []*tar.Header{{Name: "package/../../escape", Mode: 0600, Typeflag: tar.TypeReg}, {Name: "package/link", Linkname: "/tmp", Typeflag: tar.TypeSymlink}} {
		directory := t.TempDir()
		path := filepath.Join(directory, "package.tgz")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		compressed := gzip.NewWriter(file)
		archive := tar.NewWriter(compressed)
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
		if err := compressed.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := unpack(path, filepath.Join(directory, "unpacked")); err == nil {
			t.Fatalf("unsafe archive accepted: %+v", header)
		}
	}
}
