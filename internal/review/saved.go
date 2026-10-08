package review

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

// SavedReview contains validated progress and all annotation scopes.
// Its wire format is compatible with Pi's saved-review records.
type SavedReview struct {
	Scope        Stack            `json:"scope"`
	Slug         string           `json:"slug"`
	Path         string           `json:"path"`
	ID           string           `json:"id"`
	Version      string           `json:"version"`
	Reviewed     int              `json:"reviewed"`
	Files        int              `json:"files"`
	Complete     bool             `json:"complete"`
	Comments     []map[string]any `json:"comments"`
	SessionNotes *string          `json:"sessionNotes"`
}

// Status never equates a missing or partially reviewed session with approval.
func (s *SavedReview) Status() string {
	if s == nil {
		return "incomplete"
	}
	if len(s.Comments) > 0 {
		return "feedback"
	}
	if s.SessionNotes != nil {
		if *s.SessionNotes != "" {
			return "feedback"
		}
	}
	if s.Complete {
		return "approved"
	}
	return "incomplete"
}

// SavedReader reads indexed sessions without modifying the tuicr store.
type SavedReader struct{ Stores []string }

// Read returns nil only when no matching session exists. Corruption is an error.
func (r SavedReader) Read(scope Stack, selector string) (*SavedReview, error) {
	if err := validateCommits(scope.Commits); err != nil {
		return nil, err
	}
	for _, store := range r.Stores {
		saved, err := readStore(store, scope, selector)
		if err != nil {
			return nil, err
		}
		if saved != nil {
			return saved, nil
		}
	}
	return nil, nil
}

// DefaultSavedReader uses tuicr's platform store, then the other platform path.
func DefaultSavedReader() (SavedReader, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return SavedReader{}, err
	}
	data := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	linux := filepath.Join(data, "tuicr", "reviews")
	mac := filepath.Join(home, "Library", "Application Support", "tuicr", "reviews")
	if runtime.GOOS == "darwin" {
		return SavedReader{Stores: []string{mac, linux}}, nil
	}
	return SavedReader{Stores: []string{linux, mac}}, nil
}

type object map[string]jsontext.Value

func decodeRequired[T any](obj object, key string) (T, error) {
	var value T
	raw, ok := obj[key]
	if !ok {
		return value, fmt.Errorf("missing %s", key)
	}
	if string(raw) == "null" {
		return value, fmt.Errorf("invalid null %s", key)
	}
	err := json.Unmarshal(raw, &value)
	if err != nil {
		return value, fmt.Errorf("invalid %s: %w", key, err)
	}
	return value, nil
}

func decodeSessionIdentity(session object, scope Stack) (*SavedReview, error) {
	version, err := decodeRequired[string](session, "version")
	if err != nil {
		return nil, err
	}
	if version != "1.3" {
		return nil, fmt.Errorf("unsupported saved-session version %q", version)
	}
	if raw, ok := session["pr_session_key"]; ok {
		if string(raw) != "null" {
			return nil, fmt.Errorf("expected local commit-range session")
		}
	}
	id, err := decodeRequired[string](session, "id")
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("missing session ID")
	}
	base, err := decodeRequired[string](session, "base_commit")
	if err != nil {
		return nil, err
	}
	if !slices.Contains(scope.Commits, base) {
		return nil, fmt.Errorf("session base_commit is outside captured range")
	}
	return &SavedReview{Scope: scope, ID: id, Version: version}, nil
}

func decodeSession(session object, scope Stack) (*SavedReview, error) {
	saved, err := decodeSessionIdentity(session, scope)
	if err != nil {
		return nil, err
	}
	files, err := decodeRequired[object](session, "files")
	if err != nil {
		return nil, err
	}
	collector := commentCollector{comments: []map[string]any{}, ids: map[string]string{}}
	if raw, ok := session["review_comments"]; ok {
		if err := collector.add(raw, "review", "", ""); err != nil {
			return nil, err
		}
	}
	reviewed, err := collector.collectFiles(files)
	if err != nil {
		return nil, err
	}
	notes, err := sessionNotes(session)
	if err != nil {
		return nil, err
	}
	saved.Files = len(files)
	saved.Reviewed = reviewed
	saved.Complete = len(files) > 0 && allReviewed(reviewed, len(files))
	saved.Comments = collector.comments
	saved.SessionNotes = notes
	return saved, nil
}

func allReviewed(reviewed, files int) bool { return reviewed == files }

type commentCollector struct {
	comments []map[string]any
	ids      map[string]string
}

func (c *commentCollector) add(raw jsontext.Value, location, path, line string) error {
	if string(raw) == "null" {
		return fmt.Errorf("invalid null comments")
	}
	var items []object
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	for _, item := range items {
		comment, id, err := normalizeComment(item, location, path, line)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(comment, json.Deterministic(true))
		if err != nil {
			return err
		}
		if previous, ok := c.ids[id]; ok {
			if previous != string(encoded) {
				return fmt.Errorf("conflicting duplicate comment ID %s", id)
			}
			continue
		}
		c.ids[id] = string(encoded)
		c.comments = append(c.comments, comment)
	}
	return nil
}

func (c *commentCollector) collectFiles(files object) (int, error) {
	reviewed := 0
	for path, raw := range files {
		var file object
		if err := json.Unmarshal(raw, &file); err != nil {
			return 0, err
		}
		storedPath, err := decodeRequired[string](file, "path")
		if err != nil {
			return 0, err
		}
		if storedPath != path {
			return 0, fmt.Errorf("invalid file identity %s", path)
		}
		done, err := decodeRequired[bool](file, "reviewed")
		if err != nil {
			return 0, err
		}
		if done {
			reviewed++
		}
		if err := c.fileComments(file, path); err != nil {
			return 0, err
		}
	}
	return reviewed, nil
}

func (c *commentCollector) fileComments(file object, path string) error {
	raw, ok := file["file_comments"]
	if !ok {
		return fmt.Errorf("missing file_comments")
	}
	if err := c.add(raw, "file", path, ""); err != nil {
		return err
	}
	lines, err := decodeRequired[object](file, "line_comments")
	if err != nil {
		return err
	}
	for line, items := range lines {
		if err := c.add(items, "line", path, line); err != nil {
			return err
		}
	}
	return nil
}

func commentIdentity(item object) (string, error) {
	id, err := decodeRequired[string](item, "id")
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("empty comment ID")
	}
	for _, field := range []string{"content", "comment_type"} {
		if _, err := decodeRequired[string](item, field); err != nil {
			return "", err
		}
	}
	return id, nil
}

func normalizeComment(item object, location, path, line string) (map[string]any, string, error) {
	id, err := commentIdentity(item)
	if err != nil {
		return nil, "", err
	}
	author, err := defaultText(item, "author", "user")
	if err != nil {
		return nil, "", err
	}
	lifecycle, err := defaultText(item, "lifecycle_state", "local_draft")
	if err != nil {
		return nil, "", err
	}
	if !slices.Contains([]string{"local_draft", "pushed_draft", "submitted"}, lifecycle) {
		return nil, "", fmt.Errorf("invalid comment lifecycle %q", lifecycle)
	}
	comment := map[string]any{}
	for key, raw := range item {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, "", err
		}
		comment[key] = value
	}
	comment["author"] = author
	comment["lifecycle_state"] = lifecycle
	comment["location"] = location
	if path != "" {
		comment["path"] = path
	}
	if line != "" {
		comment["stored_line"] = line
	}
	return comment, id, nil
}

func defaultText(obj object, key, fallback string) (string, error) {
	if _, ok := obj[key]; !ok {
		return fallback, nil
	}
	return decodeRequired[string](obj, key)
}

func readObject(path string) (object, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var obj object
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if obj == nil {
		return nil, fmt.Errorf("invalid null object in %s", path)
	}
	return obj, nil
}

func readIndex(store string) (map[string][]object, error) {
	index, err := readObject(filepath.Join(store, "index.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	version, err := decodeRequired[string](index, "version")
	if err != nil {
		return nil, err
	}
	if version != "2.0" {
		return nil, fmt.Errorf("unsupported review index version %q", version)
	}
	return decodeRequired[map[string][]object](index, "entries")
}

func readStore(store string, scope Stack, selector string) (*SavedReview, error) {
	entries, err := readIndex(store)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		return nil, nil
	}
	repo, err := filepath.EvalSymlinks(scope.Repo)
	if err != nil {
		return nil, err
	}
	candidates := map[string]*SavedReview{}
	for slug, rows := range entries {
		for _, entry := range rows {
			saved, err := readEntry(store, repo, slug, entry, scope, selector)
			if err != nil {
				return nil, err
			}
			if saved != nil {
				candidates[saved.Path] = saved
			}
		}
	}
	if len(candidates) > 1 {
		return nil, fmt.Errorf("ambiguous saved reviews; select an exact session with --session")
	}
	for _, saved := range candidates {
		return saved, nil
	}
	return nil, nil
}

func readEntry(store, repo, slug string, entry object, scope Stack, selector string) (*SavedReview, error) {
	kind, err := decodeRequired[object](entry, "kind")
	if err != nil {
		return nil, err
	}
	entryType, err := decodeRequired[string](kind, "type")
	if err != nil {
		return nil, err
	}
	if entryType != "local" {
		return nil, nil
	}
	canonical, err := decodeRequired[string](entry, "canonical_repo_path")
	if err != nil {
		return nil, err
	}
	if filepath.Clean(canonical) != repo {
		return nil, nil
	}
	relative, err := decodeRequired[string](entry, "path")
	if err != nil {
		return nil, err
	}
	if !sessionPathPattern.MatchString(relative) {
		return nil, fmt.Errorf("unsafe session path %q", relative)
	}
	path, err := safeSessionPath(store, relative)
	if err != nil {
		return nil, err
	}
	return readCandidate(path, scope, repo, selector, slug, relative)
}

func readCandidate(path string, scope Stack, repo, selector, slug, relative string) (*SavedReview, error) {
	session, err := readObject(path)
	if err != nil {
		return nil, err
	}
	saved, err := matchSession(session, scope, repo, selector, slug, relative)
	if saved != nil {
		saved.Slug = slug
		saved.Path = path
	}
	return saved, err
}

func safeSessionPath(store, relative string) (string, error) {
	path, err := filepath.EvalSymlinks(filepath.Join(store, relative))
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(filepath.Join(store, "sessions"))
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return "", fmt.Errorf("session path escapes review store")
	}
	return path, nil
}

func matchesSelector(session object, selector, slug, relative string) (bool, error) {
	if selector == "" {
		return true, nil
	}
	id, err := decodeRequired[string](session, "id")
	if err != nil {
		return false, err
	}
	return slices.Contains([]string{id, slug, relative}, selector), nil
}

func matchSession(session object, scope Stack, repo, selector, slug, relative string) (*SavedReview, error) {
	source, err := decodeRequired[string](session, "diff_source")
	if err != nil {
		return nil, err
	}
	if source != "commit_range" {
		return nil, nil
	}
	commits, err := decodeRequired[[]string](session, "commit_range")
	if err != nil {
		return nil, err
	}
	if err := validateCommits(commits); err != nil {
		return nil, err
	}
	if !sameCommitSet(commits, scope.Commits) {
		return nil, nil
	}
	selected, err := matchesSelector(session, selector, slug, relative)
	if err != nil {
		return nil, err
	}
	if !selected {
		return nil, nil
	}
	if err := validateSessionRepo(session, repo); err != nil {
		return nil, err
	}
	scope.Repo = repo
	return decodeSession(session, scope)
}

func validateSessionRepo(session object, repo string) error {
	stored, err := decodeRequired[string](session, "repo_path")
	if err != nil {
		return err
	}
	stored, err = filepath.EvalSymlinks(stored)
	if err != nil {
		return err
	}
	if stored != repo {
		return fmt.Errorf("index/session repository mismatch")
	}
	return nil
}

func sameCommitSet(a, b []string) bool {
	a = slices.Clone(a)
	b = slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func sessionNotes(session object) (*string, error) {
	raw, ok := session["session_notes"]
	if !ok {
		return nil, nil
	}
	if string(raw) == "null" {
		return nil, nil
	}
	value, err := decodeRequired[string](session, "session_notes")
	return &value, err
}

var sessionPathPattern = regexp.MustCompile(`^sessions/[a-f0-9]{16}\.json$`)
