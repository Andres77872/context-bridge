package migrate

import (
	"path/filepath"
	"testing"

	"context-bridge/internal/store"
)

func TestImportManifestDirSuccessPath(t *testing.T) {
	st := openTestStore(t)

	count, err := ImportManifestDir(st, filepath.Join("testdata", "sessions"))
	if err != nil {
		t.Fatalf("ImportManifestDir: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 imported captures, got %d", count)
	}

	captures, err := st.ListCaptures("ses-import-child", "")
	if err != nil {
		t.Fatalf("ListCaptures: %v", err)
	}
	if len(captures) != 2 {
		t.Fatalf("expected 2 stored captures, got %d", len(captures))
	}
	if captures[0].SourcePath == "" {
		t.Fatal("expected imported source path to be recorded")
	}
	root, err := st.ResolveRoot("ses-import-child")
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	if root != "ses-import" {
		t.Fatalf("expected imported child root ses-import, got %s", root)
	}
}

func TestImportManifestDirIsIdempotentOnReimport(t *testing.T) {
	st := openTestStore(t)
	sessionsDir := filepath.Join("testdata", "sessions")

	first, err := ImportManifestDir(st, sessionsDir)
	if err != nil {
		t.Fatalf("first ImportManifestDir: %v", err)
	}
	second, err := ImportManifestDir(st, sessionsDir)
	if err != nil {
		t.Fatalf("second ImportManifestDir: %v", err)
	}

	if first != 2 {
		t.Fatalf("expected first import count 2, got %d", first)
	}
	if second != 0 {
		t.Fatalf("expected second import count 0, got %d", second)
	}

	captures, err := st.ListCaptures("ses-import", "")
	if err != nil {
		t.Fatalf("ListCaptures: %v", err)
	}
	if len(captures) != 2 {
		t.Fatalf("expected exactly 2 captures after reimport, got %d", len(captures))
	}
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "migrate.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
