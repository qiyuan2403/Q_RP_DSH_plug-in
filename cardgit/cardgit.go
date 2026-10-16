// Package cardgit versions a card workspace with Git.
//
// A card is a PNG wrapping JSON, so versioning the PNG itself produces
// undiffable history. The card package decomposes a card into text, which
// makes Git useful: a one-field edit is a one-line diff.
//
// This package wraps the git CLI rather than reimplementing object storage.
// That is deliberate: a workspace is a normal Git repository the user can also
// drive by hand, so the history stays portable and no proprietary store is
// introduced.
package cardgit

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNoRepo reports a directory that is not a Git repository.
var ErrNoRepo = errors.New("cardgit: not a git repository")

// Repo is a Git repository rooted at a directory.
type Repo struct {
	Dir string
	// Author identity used for commits. Empty means git's configured identity.
	Name  string
	Email string
}

// Open returns a Repo for dir, reporting whether dir is already a repository.
func Open(dir string) (*Repo, bool) {
	r := &Repo{Dir: dir}
	return r, r.IsRepo()
}

// IsRepo reports whether dir is inside a Git work tree.
func (r *Repo) IsRepo() bool {
	out, err := r.git("rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// Init creates a repository if one does not exist and returns whether it
// created it.
//
// The default branch is pinned to `main` so a workspace does not depend on the
// machine's `init.defaultBranch`.
func (r *Repo) Init() (bool, error) {
	if r.IsRepo() {
		return false, nil
	}
	if _, err := r.git("init", "-b", "main"); err != nil {
		return false, err
	}
	return true, nil
}

// Save stages everything and commits. It returns false when there was nothing
// to commit, which is a normal outcome (a pack with no edit) and not an error.
func (r *Repo) Save(message string) (bool, error) {
	if !r.IsRepo() {
		return false, ErrNoRepo
	}
	if strings.TrimSpace(message) == "" {
		return false, errors.New("cardgit: commit message is empty")
	}
	if _, err := r.git("add", "-A"); err != nil {
		return false, err
	}
	dirty, err := r.HasChanges()
	if err != nil {
		return false, err
	}
	if !dirty {
		return false, nil
	}
	args := []string{}
	if r.Name != "" && r.Email != "" {
		args = append(args, "-c", "user.name="+r.Name, "-c", "user.email="+r.Email)
	}
	args = append(args, "commit", "-m", message)
	if _, err := r.git(args...); err != nil {
		return false, err
	}
	return true, nil
}

// HasChanges reports whether the work tree differs from HEAD, including
// untracked files.
func (r *Repo) HasChanges() (bool, error) {
	out, err := r.git("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// Diff returns the unified diff of the work tree against HEAD.
//
// `path` narrows the diff to one file or directory; empty means everything.
func (r *Repo) Diff(path string) (string, error) {
	args := []string{"diff", "--no-color"}
	if path != "" {
		args = append(args, "--", path)
	}
	return r.git(args...)
}

// DiffStat returns the shortstat summary of the work tree against HEAD.
func (r *Repo) DiffStat() (string, error) {
	out, err := r.git("diff", "--shortstat")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Commit is one history entry.
type Commit struct {
	Hash    string
	Subject string
	Date    string
}

// Log returns the most recent commits, newest first.
func (r *Repo) Log(limit int) ([]Commit, error) {
	if limit <= 0 {
		limit = 20
	}
	out, err := r.git("log", fmt.Sprintf("-n%d", limit), "--pretty=format:%H%x09%ad%x09%s", "--date=short")
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		commits = append(commits, Commit{Hash: parts[0], Date: parts[1], Subject: parts[2]})
	}
	return commits, nil
}

// Restore reverts one path in the work tree to its state at a revision.
//
// This is the rollback the workshop exposes: pick a commit, restore the card
// document, and pack. The original import under `.original/` is never the
// target of a restore by default, because losing it would break the guarantee
// that packing starts from the imported bytes.
func (r *Repo) Restore(revision, path string) error {
	if revision == "" {
		revision = "HEAD"
	}
	args := []string{"checkout", revision}
	if path != "" {
		args = append(args, "--", path)
	}
	_, err := r.git(args...)
	return err
}

// Tracked reports whether a path is tracked by Git.
func (r *Repo) Tracked(path string) (bool, error) {
	out, err := r.git("ls-files", "--error-unmatch", path)
	if err != nil {
		// git exits non-zero when the path is untracked; that is an answer, not
		// a failure.
		if strings.Contains(out, "did not match") || strings.Contains(out, "error") {
			return false, nil
		}
		return false, nil
	}
	return strings.TrimSpace(out) != "", nil
}

// EnsureIgnored appends entries to .gitignore so large or derived artifacts do
// not enter history.
//
// The default set keeps `dist/` (repacked output, reproducible from the
// original) and `assets/` (potentially large binaries) out of the diff, which
// is what makes the remaining history readable. An entry already present is not
// duplicated.
func (r *Repo) EnsureIgnored(entries ...string) error {
	if len(entries) == 0 {
		return nil
	}
	path := filepath.Join(r.Dir, ".gitignore")
	existing := ""
	if raw, err := os.ReadFile(path); err == nil {
		existing = string(raw)
	}
	var add []string
	for _, e := range entries {
		if e == "" || strings.Contains(existing, e) {
			continue
		}
		add = append(add, e)
	}
	if len(add) == 0 {
		return nil
	}
	var buf bytes.Buffer
	buf.WriteString(existing)
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		buf.WriteString("\n")
	}
	for _, e := range add {
		buf.WriteString(e)
		buf.WriteString("\n")
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// git runs one git command in the repository directory.
func (r *Repo) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	// A deterministic environment: a caller's global config must not change
	// what a workspace operation produces.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_PAGER=cat",
		"LC_ALL=C",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("cardgit: git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}
