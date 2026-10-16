// Package cardgit versions a card workspace with Git.
//
// A card is a PNG wrapping JSON, so versioning the PNG itself produces
// undiffable history. The card package decomposes a card into text, which
// makes Git useful: a one-field edit is a one-line diff.
//
// The history is written through go-git, the in-process Git implementation,
// NOT through a `git` subprocess. That is what makes the package work on
// Android: an app process has no `git` binary on PATH, and Android 10+ blocks
// execve from the app's own directories entirely, so shelling out is
// structurally impossible there. An in-process implementation also removes
// the interpreter-config drift the CLI form had (git's configured user.name
// no longer changes what a commit says).
//
// A workspace is still a normal Git repository: go-git writes standard
// objects, refs, and a standard work tree, so the user can drive the same
// history by hand with any Git build.
package cardgit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// ErrNoRepo reports a directory that is not a Git repository.
var ErrNoRepo = errors.New("cardgit: not a git repository")

// Repo is a Git repository rooted at a directory.
type Repo struct {
	Dir string
	// Author identity used for commits. Empty means a fixed, deterministic
	// workspace identity rather than the machine's configured one: a card's
	// history is a document's history, and the committer must not drift
	// between machines.
	Name  string
	Email string
}

// defaultAuthor is the identity a workspace commits with when the caller
// supplies none. It is fixed rather than resolved from the environment, so a
// card packed on one machine carries the same committer as anywhere else.
const (
	defaultName  = "Q_RP_dsh"
	defaultEmail = "qrpdsh@localhost"
)

func (r *Repo) author() (string, string) {
	if r.Name != "" && r.Email != "" {
		return r.Name, r.Email
	}
	return defaultName, defaultEmail
}

// plainRepo opens the in-process repository handle.
func (r *Repo) plainRepo() (*git.Repository, error) {
	return git.PlainOpen(r.Dir)
}

// Open returns a Repo for dir, reporting whether dir is already a repository.
func Open(dir string) (*Repo, bool) {
	r := &Repo{Dir: dir}
	return r, r.IsRepo()
}

// IsRepo reports whether dir is inside a Git work tree.
func (r *Repo) IsRepo() bool {
	_, err := r.plainRepo()
	return err == nil
}

// Init creates a repository if one does not exist and returns whether it
// created it.
//
// The default branch is pinned to `main` so a workspace does not depend on
// the machine's `init.defaultBranch`.
func (r *Repo) Init() (bool, error) {
	if r.IsRepo() {
		return false, nil
	}
	if _, err := git.PlainInit(r.Dir, false); err != nil {
		return false, fmt.Errorf("cardgit: init: %w", err)
	}
	repo, err := r.plainRepo()
	if err != nil {
		return false, err
	}
	// PlainInit leaves HEAD symbolic but unborn on a nonstandard name only;
	// pin the branch by pointing HEAD at refs/heads/main.
	if err := setHead(repo, "refs/heads/main"); err != nil {
		return false, err
	}
	return true, nil
}

// setHead repoints the repository's HEAD at a branch name.
func setHead(repo *git.Repository, ref string) error {
	h, err := repo.Head()
	if err == nil && h.Name().String() == ref {
		return nil
	}
	// Storing the symbolic ref directly keeps the work tree's branch choice
	// explicit without requiring a first commit.
	return repo.Storer.SetReference(
		plumbingNewSymbolicRef("HEAD", ref),
	)
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
	repo, err := r.plainRepo()
	if err != nil {
		return false, err
	}
	w, err := repo.Worktree()
	if err != nil {
		return false, err
	}
	if err := w.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return false, fmt.Errorf("cardgit: add: %w", err)
	}
	status, err := w.Status()
	if err != nil {
		return false, err
	}
	if status.IsClean() {
		return false, nil
	}
	name, email := r.author()
	_, err = w.Commit(message, &git.CommitOptions{
		Author: &object.Signature{Name: name, Email: email, When: time.Now()},
	})
	if err != nil {
		return false, fmt.Errorf("cardgit: commit: %w", err)
	}
	return true, nil
}

// HasChanges reports whether the work tree differs from HEAD, including
// untracked files.
func (r *Repo) HasChanges() (bool, error) {
	repo, err := r.plainRepo()
	if err != nil {
		return false, err
	}
	w, err := repo.Worktree()
	if err != nil {
		return false, err
	}
	status, err := w.Status()
	if err != nil {
		return false, err
	}
	return !status.IsClean(), nil
}

// Diff returns a unified diff of the work tree against HEAD.
//
// `path` narrows the diff to one file or directory; empty means everything.
// It is produced in-process over the work tree's own status, which is the
// same information `git status --porcelain` reports, rendered as a patch so
// a one-field edit reads as a one-line diff.
func (r *Repo) Diff(path string) (string, error) {
	var buf strings.Builder
	if err := r.diffInto(&buf, path); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// DiffStat returns a shortstat summary of the work tree against HEAD.
func (r *Repo) DiffStat() (string, error) {
	var changed, added, removed int
	err := r.forEachChange(func(name string, status git.StatusCode, statusLine string) {
		changed++
	}, "")
	if err != nil {
		return "", err
	}
	added, removed, err = r.countDelta("")
	if err != nil {
		return "", err
	}
	if changed == 0 && added == 0 && removed == 0 {
		return "", nil
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, " %d file%s changed", changed, plural(changed))
	if added > 0 {
		fmt.Fprintf(&buf, ", %d insertion%s(+)", added, plural(added))
	}
	if removed > 0 {
		fmt.Fprintf(&buf, ", %d deletion%s(-)", removed, plural(removed))
	}
	return buf.String(), nil
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
	repo, err := r.plainRepo()
	if err != nil {
		return nil, err
	}
	iterator, err := repo.Log(&git.LogOptions{})
	if err != nil {
		return nil, err
	}
	var commits []Commit
	if err := iterator.ForEach(func(c *object.Commit) error {
		if len(commits) >= limit {
			return errStopIteration
		}
		commits = append(commits, Commit{
			Hash:    c.Hash.String(),
			Subject: strings.SplitN(c.Message, "\n", 2)[0],
			Date:    c.Author.When.Format("2006-01-02"),
		})
		return nil
	}); err != nil && !errors.Is(err, errStopIteration) {
		return nil, err
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
	repo, err := r.plainRepo()
	if err != nil {
		return err
	}
	hash, err := repo.ResolveRevision(plumbingRevision(revision))
	if err != nil {
		return fmt.Errorf("cardgit: resolve %q: %w", revision, err)
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return err
	}
	targetTree, err := commit.Tree()
	if err != nil {
		return err
	}
	w, err := repo.Worktree()
	if err != nil {
		return err
	}
	// A path-scoped restore walks the revision's tree at that prefix and
	// writes each entry back; an unscoped restore resets the whole work tree
	// to the revision.
	return r.restoreTree(w, targetTree, path)
}

// Tracked reports whether a path is tracked by Git.
func (r *Repo) Tracked(path string) (bool, error) {
	repo, err := r.plainRepo()
	if err != nil {
		return false, err
	}
	w, err := repo.Worktree()
	if err != nil {
		return false, err
	}
	status, err := w.Status()
	if err != nil {
		return false, err
	}
	// A tracked file appears in the status map unless it is clean AND staged
	// clean; the map only carries files whose work-tree state differs from
	// HEAD. The index is the authoritative tracked set, so read it directly.
	index, err := repo.Storer.Index()
	if err != nil {
		return false, err
	}
	rel := path
	if rel == "" {
		return false, errors.New("cardgit: tracked path is empty")
	}
	rel = filepath.ToSlash(rel)
	for _, entry := range index.Entries {
		if entry.Name == rel || strings.HasPrefix(entry.Name, rel+"/") {
			return true, nil
		}
	}
	_ = status
	return false, nil
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
	var buf strings.Builder
	buf.WriteString(existing)
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		buf.WriteString("\n")
	}
	for _, e := range add {
		buf.WriteString(e)
		buf.WriteString("\n")
	}
	return os.WriteFile(path, []byte(buf.String()), 0o644)
}
