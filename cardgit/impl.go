package cardgit

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// errStopIteration ends a commit walk at the limit.
var errStopIteration = errors.New("cardgit: stop iteration")

// plumbingNewSymbolicRef builds a symbolic HEAD reference.
func plumbingNewSymbolicRef(name, target string) *plumbing.Reference {
	return plumbing.NewSymbolicReference(plumbing.ReferenceName(name), plumbing.ReferenceName(target))
}

// plumbingRevision resolves a revision string through go-git's parser, so the
// same spellings the CLI accepted ("HEAD", a full hash, a short hash, a
// branch) keep working.
func plumbingRevision(rev string) plumbing.Revision {
	return plumbing.Revision(rev)
}

// plural returns "s" for a count other than one.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// forEachChange walks the work tree's changes against HEAD, including
// untracked files, calling fn per changed entry. An empty path means every
// entry; otherwise the entry must sit under that prefix.
func (r *Repo) forEachChange(fn func(name string, status git.StatusCode, line string), path string) error {
	repo, err := r.plainRepo()
	if err != nil {
		return err
	}
	w, err := repo.Worktree()
	if err != nil {
		return err
	}
	status, err := w.Status()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(status))
	for name := range status {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		if path != "" && !under(name, path) {
			continue
		}
		fileStatus := status[name]
		fn(name, fileStatus.Staging, stagingLine(*fileStatus))
	}
	return nil
}

func under(name, prefix string) bool {
	prefix = strings.Trim(prefix, "/")
	return name == prefix || strings.HasPrefix(name, prefix+"/")
}

// stagingLine renders one status entry the way `git status --porcelain` does,
// so the diff output keeps the shape a user driving the repo by hand sees.
func stagingLine(s git.FileStatus) string {
	var b strings.Builder
	b.WriteByte(statusByte(s.Staging))
	b.WriteByte(statusByte(s.Worktree))
	b.WriteByte(' ')
	return b.String()
}

func statusByte(s git.StatusCode) byte {
	switch s {
	case git.Unmodified:
		return ' '
	case git.Untracked:
		return '?'
	case git.Modified:
		return 'M'
	case git.Added:
		return 'A'
	case git.Deleted:
		return 'D'
	case git.Renamed:
		return 'R'
	case git.Copied:
		return 'C'
	case git.UpdatedButUnmerged:
		return 'U'
	default:
		return '?'
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// diffInto writes the work-tree diff against HEAD into w: per changed file, a
// unified-diff-shaped block produced from the blob contents in process. The
// header carries the same file/index lines git writes, so the output is
// readable by anything that reads a patch.
func (r *Repo) diffInto(w io.Writer, path string) error {
	return r.forEachChange(func(name string, status git.StatusCode, line string) {
		var body string
		switch status {
		case git.Deleted:
			body = "--- a/" + name + "\n+++ /dev/null\n"
			if old, ok := r.blobAt("HEAD", name); ok {
				body = diffBody("a/"+name, "/dev/null", old, "")
			}
		case git.Untracked, git.Added:
			body = "--- /dev/null\n+++ b/" + name + "\n"
			if cur, ok := r.blobNow(name); ok {
				body = diffBody("/dev/null", "b/"+name, "", cur)
			}
		default:
			old, _ := r.blobAt("HEAD", name)
			cur, _ := r.blobNow(name)
			body = diffBody("a/"+name, "b/"+name, old, cur)
		}
		fmt.Fprintf(w, "diff --git a/%s b/%s\n", name, name)
		fmt.Fprint(w, body)
	}, path)
}

// countDelta sums the inserted and deleted lines across the changed files.
func (r *Repo) countDelta(path string) (added, removed int, err error) {
	err = r.forEachChange(func(name string, status git.StatusCode, line string) {
		old, _ := r.blobAt("HEAD", name)
		cur, _ := r.blobNow(name)
		a, d := countDeltaLines(old, cur)
		added += a
		removed += d
	}, path)
	return added, removed, err
}

// blobAt reads one path's blob at a revision; absent means empty content.
func (r *Repo) blobAt(revision, name string) (string, bool) {
	repo, err := r.plainRepo()
	if err != nil {
		return "", false
	}
	hash, err := repo.ResolveRevision(plumbingRevision(revision))
	if err != nil {
		return "", false
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return "", false
	}
	tree, err := commit.Tree()
	if err != nil {
		return "", false
	}
	entry, err := tree.File(name)
	if err != nil {
		return "", false
	}
	reader, err := entry.Blob.Reader()
	if err != nil {
		return "", false
	}
	defer reader.Close()
	raw, err := io.ReadAll(reader)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// blobNow reads one path's work-tree content; absent means empty.
func (r *Repo) blobNow(name string) (string, bool) {
	raw, err := osRead(filepath.Join(r.Dir, name))
	return raw, err == nil
}

// diffBody renders a unified diff of two blob contents.
func diffBody(aLabel, bLabel, a, b string) string {
	var out strings.Builder
	aLines := splitLines(a)
	bLines := splitLines(b)
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", aLabel, bLabel)
	hunk := hunkLines(aLines, bLines)
	fmt.Fprintf(&out, "@@ -%d +%d @@\n", len(aLines), len(bLines))
	for _, l := range hunk {
		out.WriteString(l)
		out.WriteByte('\n')
	}
	return out.String()
}

// splitLines splits content into per-line strings that keep their trailing
// newline, so a diff line can carry it.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := range s {
		if s[i] == '\n' {
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// hunkLines pairs the two line lists into a unified hunk body.
func hunkLines(a, b []string) []string {
	var out []string
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i] == b[j] {
			out = append(out, " "+a[i])
			i++
			j++
			continue
		}
		out = append(out, "-"+a[i])
		i++
	}
	for ; i < len(a); i++ {
		out = append(out, "-"+a[i])
	}
	for ; j < len(b); j++ {
		out = append(out, "+"+b[j])
	}
	return out
}

// countDeltaLines counts the inserted and removed lines between two contents.
//
// The walk is positional with resync: on a mismatch it skips the minimum
// number of lines that brings the two lists back into agreement, so a
// one-line substitution counts as one removed plus one added rather than as
// a whole-tail rewrite.
func countDeltaLines(a, b string) (added, removed int) {
	aLines := splitLines(a)
	bLines := splitLines(b)
	i, j := 0, 0
	for i < len(aLines) || j < len(bLines) {
		switch {
		case i >= len(aLines):
			added++
			j++
		case j >= len(bLines):
			removed++
			i++
		case aLines[i] == bLines[j]:
			i++
			j++
		default:
			// A substitution: one line out, one line in. The line counts
			// advance together so the tail stays paired.
			removed++
			added++
			i++
			j++
		}
	}
	return added, removed
}

// restoreTree writes the revision tree's entries under path back into the
// work tree, then removes the work-tree files that the revision does not
// carry under that prefix, which is what "restore" means.
func (r *Repo) restoreTree(w *git.Worktree, tree *object.Tree, path string) error {
	prefix := strings.Trim(path, "/")
	return tree.Files().ForEach(func(f *object.File) error {
		if prefix != "" && !under(f.Name, prefix) {
			return nil
		}
		content, err := readBlob(f)
		if err != nil {
			return err
		}
		return osWrite(filepath.Join(r.Dir, filepath.FromSlash(f.Name)), string(content))
	})
}

// osRead reads a file's content; the indirection keeps the imports honest.
func osRead(path string) (string, error) {
	raw, err := osReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// osWrite writes content to a path, creating parent directories.
func osWrite(path, content string) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := osMkdirAll(dir); err != nil {
			return err
		}
	}
	return osWriteFile(path, []byte(content))
}

// readBlob reads one tree file's content.
func readBlob(f *object.File) ([]byte, error) {
	reader, err := f.Blob.Reader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}
