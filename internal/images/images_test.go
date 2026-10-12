package images

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/onatm/boxface/internal/store"
)

func newRegistry(t *testing.T) (*Registry, string, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "boxface.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	importDir, imagesDir := filepath.Join(dir, "import"), filepath.Join(dir, "images")
	for _, d := range []string{importDir, imagesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return NewRegistry(db, importDir, imagesDir), importDir, imagesDir
}

func stage(t *testing.T, importDir, name, kernel, rootfs string) {
	t.Helper()
	dir := filepath.Join(importDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "vmlinux"), []byte(kernel), 0o600)
	os.WriteFile(filepath.Join(dir, "rootfs.ext4"), []byte(rootfs), 0o600)
}

func TestImportMovesArtifactsAndRecordsDigest(t *testing.T) {
	r, importDir, imagesDir := newRegistry(t)
	stage(t, importDir, "gate-c", "kernel", "rootfs")

	img, created, err := r.Import(context.Background(), "gate-c")
	if err != nil || !created {
		t.Fatalf("import = %+v, %v, %v", img, created, err)
	}
	if img.Status != "ready" || len(img.Digest) != len("sha256:")+64 {
		t.Fatalf("image = %+v", img)
	}
	info, err := os.Stat(filepath.Join(imagesDir, img.ID, "rootfs.ext4"))
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("rootfs = %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(importDir, "gate-c")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source left in import dir: %v", err)
	}
	list, _ := r.List(context.Background())
	if len(list) != 1 || !reflect.DeepEqual(list[0], img) {
		t.Fatalf("list = %+v", list)
	}
}

func TestImportSameContentReturnsExistingImage(t *testing.T) {
	r, importDir, _ := newRegistry(t)
	stage(t, importDir, "first", "kernel", "rootfs")
	first, _, _ := r.Import(context.Background(), "first")
	stage(t, importDir, "again", "kernel", "rootfs")
	again, created, err := r.Import(context.Background(), "again")
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("again = %+v, created %v, %v", again, created, err)
	}
}

func TestImportRejectsBadSources(t *testing.T) {
	r, importDir, _ := newRegistry(t)
	stage(t, importDir, "partial", "kernel", "rootfs")
	os.Remove(filepath.Join(importDir, "partial", "vmlinux"))
	os.Symlink("/etc/passwd", filepath.Join(importDir, "partial", "vmlinux"))
	for _, source := range []string{"../etc", "a/b", "", "missing", "partial"} {
		if _, _, err := r.Import(context.Background(), source); !errors.Is(err, ErrInvalidSource) {
			t.Fatalf("import %q: err = %v", source, err)
		}
	}
}

func TestSetAliasMovesBetweenImages(t *testing.T) {
	r, importDir, _ := newRegistry(t)
	ctx := context.Background()
	stage(t, importDir, "one", "kernel", "one")
	stage(t, importDir, "two", "kernel", "two")
	one, _, _ := r.Import(ctx, "one")
	two, _, _ := r.Import(ctx, "two")

	if err := r.SetAlias(ctx, "main", one.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.SetAlias(ctx, "main", two.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := r.List(ctx)
	if !reflect.DeepEqual(list[0].Aliases, []string{}) || !reflect.DeepEqual(list[1].Aliases, []string{"main"}) {
		t.Fatalf("list = %+v", list)
	}
	if err := r.SetAlias(ctx, "main", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown image: err = %v", err)
	}
	for _, name := range []string{"Main", "../x", "", "1st"} {
		if err := r.SetAlias(ctx, name, one.ID); !errors.Is(err, ErrInvalidAlias) {
			t.Fatalf("alias %q: err = %v", name, err)
		}
	}
}

func TestStagedListsCompleteImports(t *testing.T) {
	r, importDir, _ := newRegistry(t)
	stage(t, importDir, "base", "kernel", "rootfs")
	os.MkdirAll(filepath.Join(importDir, "partial"), 0o755)
	os.WriteFile(filepath.Join(importDir, "partial", "vmlinux"), []byte("k"), 0o600)
	os.WriteFile(filepath.Join(importDir, "loose-file"), []byte("x"), 0o600)
	got, err := r.Staged()
	if err != nil || !reflect.DeepEqual(got, []Staged{{Source: "base"}}) {
		t.Fatalf("staged = %+v, %v", got, err)
	}
	if _, _, err := r.Import(context.Background(), "base"); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Staged(); len(got) != 0 {
		t.Fatalf("staged after import = %+v", got)
	}
}
