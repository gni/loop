package ignore

import (
	"path/filepath"
	"testing"
)

func TestIsDirNameCanonicalSet(t *testing.T) {
	for _, name := range []string{".git", "node_modules", "venv", ".venv", "__pycache__", "dist", "vendor", ".cache"} {
		if !IsDirName(name, false) {
			t.Fatalf("%q should be ignored under both policies", name)
		}
	}
	if IsDirName("pkg", false) || IsDirName("pkg", true) {
		t.Fatal("normal directory must not be ignored")
	}
	// Hidden directories are ignored only by the search policy.
	if IsDirName(".loop", false) {
		t.Fatal("file policy must not treat every hidden directory as ignored")
	}
	if !IsDirName(".loop", true) {
		t.Fatal("search policy must treat hidden directories as ignored")
	}
	if IsDirName(".", true) || IsDirName("..", true) {
		t.Fatal("dot segments must not be ignored")
	}
}

func TestHasComponentRespectsRoot(t *testing.T) {
	root := filepath.Join("/tmp", "workspace")
	if HasComponent(root, root, true) {
		t.Fatal("the workspace root itself must never be treated as ignored")
	}
	// A workspace living inside a hidden directory is exempt because the root itself is
	// never judged; hidden segments below the root are still ignored.
	if HasComponent("/tmp/.gotmp/workspace", "/tmp/.gotmp/workspace", true) {
		t.Fatal("a root inside a hidden directory must remain searchable")
	}
	if !HasComponent(filepath.Join(root, ".gotmp", "x"), root, true) {
		t.Fatal("hidden segments below the root must be ignored by the search policy")
	}
	if !HasComponent(filepath.Join(root, "node_modules", "pkg"), root, true) {
		t.Fatal("dependency directory below the root must be ignored")
	}
	// Policy split: the file policy keeps workspace-local hidden folders readable.
	if HasComponent(filepath.Join(root, ".loop", "spill"), root, false) {
		t.Fatal("file policy should keep .loop readable")
	}
	if !HasComponent(filepath.Join(root, ".loop", "spill"), root, true) {
		t.Fatal("search policy should skip .loop")
	}
}
