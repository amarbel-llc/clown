// Package sessions enumerates resumable provider sessions for `clown resume`.
//
// Today only Claude Code is supported. Codex sessions live in a sqlite DB
// without per-thread cwd, so PWD scoping there is not yet implementable;
// see issue #24 follow-ups.
package sessions

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Session is one resumable provider conversation.
type Session struct {
	ID        string    // session UUID; the .jsonl file's stem
	CWD       string    // working directory recorded in the transcript head
	GitBranch string    // git branch recorded in the transcript head
	Title     string    // last custom-title (preferred) or agent-name in the transcript
	ModTime   time.Time // transcript file mtime, used for ordering
	Path      string    // absolute path to the .jsonl transcript
}

// headScanLines bounds how far we read into a transcript looking for the
// cwd/gitBranch fields the first user-message entry typically carries.
const headScanLines = 20

// tailScanBytes bounds how far back from EOF we read looking for a title.
// Custom titles and agent-name entries are written near the end of a
// session; 64 KB comfortably covers normal cases without slurping the
// full transcript.
const tailScanBytes = 64 * 1024

// ListClaudeSessions returns every Claude Code transcript discoverable
// under homeDir/.claude/projects, sorted by mtime descending. Transcripts
// whose head does not contain a cwd field still appear (with an empty
// CWD) so the caller can decide policy.
func ListClaudeSessions(homeDir string) ([]Session, error) {
	root := filepath.Join(homeDir, ".claude", "projects")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var out []Session
	for _, projectEnt := range entries {
		if !projectEnt.IsDir() {
			continue
		}
		projectDir := filepath.Join(root, projectEnt.Name())
		transcripts, err := os.ReadDir(projectDir)
		if err != nil {
			continue
		}
		for _, t := range transcripts {
			if t.IsDir() || filepath.Ext(t.Name()) != ".jsonl" {
				continue
			}
			full := filepath.Join(projectDir, t.Name())
			info, err := t.Info()
			if err != nil {
				continue
			}
			cwd, gitBranch := extractHeadMeta(full)
			s := Session{
				ID:        stem(t.Name()),
				CWD:       cwd,
				GitBranch: gitBranch,
				ModTime:   info.ModTime(),
				Path:      full,
			}
			s.Title = extractTitle(full)
			out = append(out, s)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].ModTime.After(out[j].ModTime)
	})
	return out, nil
}

// FilterByCWD returns the sessions whose CWD exactly matches pwd.
// No prefix matching, no symlink resolution — Claude itself does not
// recognize worktrees, so neither do we.
func FilterByCWD(all []Session, pwd string) []Session {
	var out []Session
	for _, s := range all {
		if s.CWD == pwd {
			out = append(out, s)
		}
	}
	return out
}

// extractHeadMeta scans the first headScanLines of a transcript looking
// for the first cwd / gitBranch fields. Returns empty strings if none
// are found or the file cannot be read.
func extractHeadMeta(path string) (cwd, gitBranch string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for i := 0; i < headScanLines && scanner.Scan(); i++ {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if cwd == "" {
			if v, ok := entry["cwd"].(string); ok {
				cwd = v
			}
		}
		if gitBranch == "" {
			if v, ok := entry["gitBranch"].(string); ok {
				gitBranch = v
			}
		}
		if cwd != "" && gitBranch != "" {
			break
		}
	}
	return cwd, gitBranch
}

// extractTitle reads up to tailScanBytes from the end of a transcript and
// returns the most recently-seen title. custom-title wins over agent-name
// when both appear.
func extractTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return ""
	}

	offset := info.Size() - tailScanBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return ""
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var name string
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		switch entry["type"] {
		case "agent-name":
			if name == "" {
				if v, ok := entry["agentName"].(string); ok {
					name = v
				}
			}
		case "custom-title":
			if v, ok := entry["customTitle"].(string); ok {
				name = v
			}
		}
	}
	return name
}

func stem(filename string) string {
	ext := filepath.Ext(filename)
	return filename[:len(filename)-len(ext)]
}
