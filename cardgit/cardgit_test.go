package cardgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// hasGit reports whether the git CLI is available.
func hasGit() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func newRepo(t *testing.T) *Repo {
	t.Helper()
	if !hasGit() {
		t.Skip("git CLI unavailable")
	}
	dir := t.TempDir()
	r, _ := Open(dir)
	if _, err := r.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	r.Name = "test"
	r.Email = "test@example.com"
	return r
}

// TestInitCreatesRepoOnce pins that Init is idempotent, so re-opening a card
// workspace does not fail or reset history.
func TestInitCreatesRepoOnce(t *testing.T) {
	dir := t.TempDir()
	if !hasGit() {
		t.Skip("git CLI unavailable")
	}
	r, _ := Open(dir)
	created, err := r.Init()
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !created {
		t.Fatal("Init did not report creating the repository")
	}
	created, err = r.Init()
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if created {
		t.Fatal("second Init reported creating the repository again")
	}
	if !r.IsRepo() {
		t.Fatal("IsRepo is false after Init")
	}
}

// TestSaveCommitsAndReportsNoOp pins both outcomes: a real change commits, and
// an unchanged tree reports "nothing to commit" rather than erroring.
func TestSaveCommitsAndReportsNoOp(t *testing.T) {
	r := newRepo(t)
	if err := os.WriteFile(filepath.Join(r.Dir, "character.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	committed, err := r.Save("import card")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !committed {
		t.Fatal("Save did not commit a real change")
	}

	committed, err = r.Save("no change")
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if committed {
		t.Fatal("Save committed with nothing changed")
	}
}

// TestSaveRequiresMessage pins fail-loud on an empty message: a commit with no
// subject makes the history useless.
func TestSaveRequiresMessage(t *testing.T) {
	r := newRepo(t)
	if _, err := r.Save("   "); err == nil {
		t.Fatal("Save accepted an empty message")
	}
}

// TestDiffShowsTheEditedField pins the whole reason the workspace is
// decomposed: an edit to one field must be a small, readable diff.
func TestDiffShowsTheEditedField(t *testing.T) {
	r := newRepo(t)
	path := filepath.Join(r.Dir, "character.json")
	if err := os.WriteFile(path, []byte("{\n  \"first_mes\": \"Hello\"\n}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := r.Save("baseline"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(path, []byte("{\n  \"first_mes\": \"Welcome\"\n}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	diff, err := r.Diff("character.json")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "-  \"first_mes\": \"Hello\"") {
		t.Fatalf("diff missing the removed line:\n%s", diff)
	}
	if !strings.Contains(diff, "+  \"first_mes\": \"Welcome\"") {
		t.Fatalf("diff missing the added line:\n%s", diff)
	}
	stat, err := r.DiffStat()
	if err != nil {
		t.Fatalf("DiffStat: %v", err)
	}
	if !strings.Contains(stat, "1 insertion") || !strings.Contains(stat, "1 deletion") {
		t.Fatalf("stat = %q, want one insertion and one deletion", stat)
	}
}

// TestLogReturnsNewestFirst pins history ordering, which the workshop's
// rollback list depends on.
func TestLogReturnsNewestFirst(t *testing.T) {
	r := newRepo(t)
	for _, msg := range []string{"first", "second", "third"} {
		if err := os.WriteFile(filepath.Join(r.Dir, "f.txt"), []byte(msg), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := r.Save(msg); err != nil {
			t.Fatalf("Save(%s): %v", msg, err)
		}
	}
	commits, err := r.Log(10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 3 {
		t.Fatalf("got %d commits, want 3", len(commits))
	}
	if commits[0].Subject != "third" {
		t.Fatalf("newest = %q, want third", commits[0].Subject)
	}
	if commits[2].Subject != "first" {
		t.Fatalf("oldest = %q, want first", commits[2].Subject)
	}
}

// TestRestoreRevertsOnePath pins rollback: restoring a path at a revision
// brings that file back without touching the rest of the tree.
func TestRestoreRevertsOnePath(t *testing.T) {
	r := newRepo(t)
	card := filepath.Join(r.Dir, "character.json")
	other := filepath.Join(r.Dir, "meta.json")
	if err := os.WriteFile(card, []byte("v1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(other, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := r.Save("baseline"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := os.WriteFile(card, []byte("v2"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(other, []byte("changed"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := r.Save("edit"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := r.Restore("HEAD~1", "character.json"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, err := os.ReadFile(card)
	if err != nil {
		t.Fatalf("read card: %v", err)
	}
	if string(got) != "v1" {
		t.Fatalf("card = %q, want v1", got)
	}
	// The unrelated file must be untouched.
	gotOther, _ := os.ReadFile(other)
	if string(gotOther) != "changed" {
		t.Fatalf("an unrelated file was reverted: %q", gotOther)
	}
}

// TestEnsureIgnoredAppendsWithoutDuplicating pins that derived artifacts are
// kept out of history and that repeated calls do not grow the file.
func TestEnsureIgnoredAppendsWithoutDuplicating(t *testing.T) {
	r := newRepo(t)
	if err := r.EnsureIgnored("dist/", "assets/"); err != nil {
		t.Fatalf("EnsureIgnored: %v", err)
	}
	if err := r.EnsureIgnored("dist/", "assets/"); err != nil {
		t.Fatalf("second EnsureIgnored: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(r.Dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	body := string(raw)
	if strings.Count(body, "dist/") != 1 {
		t.Fatalf("dist/ appears %d times, want 1:\n%s", strings.Count(body, "dist/"), body)
	}
	if !strings.Contains(body, "assets/") {
		t.Fatalf(".gitignore missing assets/:\n%s", body)
	}
}

// TestIgnoredPathsStayOutOfHistory pins that the ignore rule actually works, so
// a large repacked PNG does not enter the repository.
func TestIgnoredPathsStayOutOfHistory(t *testing.T) {
	r := newRepo(t)
	if err := r.EnsureIgnored("dist/"); err != nil {
		t.Fatalf("EnsureIgnored: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(r.Dir, "dist"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "dist", "card.png"), []byte("binary"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "character.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := r.Save("commit"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	tracked, err := r.Tracked("dist/card.png")
	if err != nil {
		t.Fatalf("Tracked: %v", err)
	}
	if tracked {
		t.Fatal("an ignored path was committed")
	}
}
