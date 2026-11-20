package memory

import (
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	projectsDirName = "projects"
	fileSuffix      = ".md"

	minOverviewBytes     = 512
	maxOverviewBytes     = 16384
	defaultOverviewBytes = 4096

	overviewHeader = "Project memory overview (titles + keywords only; call SearchMemory for full content):"

	maxSearchLimit = 10
	defaultLimit   = 5
)

// ScoredEntry là kết quả Search kèm điểm và lý do xếp hạng (debug-friendly).
type ScoredEntry struct {
	Entry  Entry
	Score  float64
	Reason string
}

type projectCache struct {
	fingerprint int64
	entries     []Entry
	skipped     []string
}

// Store là kho memory file-backed, an toàn goroutine.
type Store struct {
	root  string
	mu    sync.Mutex
	cache map[string]*projectCache
}

// NewStore tạo Store trỏ tới root (thường là appdata.MemoriesRootPath()).
func NewStore(root string) *Store {
	return &Store{
		root:  strings.TrimSpace(root),
		cache: make(map[string]*projectCache),
	}
}

// Root trả về gốc lưu trữ hiện tại (hữu ích cho log/audit).
func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

func (s *Store) projectDir(project string) (string, error) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return "", fmt.Errorf("memory store root is not configured")
	}
	project = strings.TrimSpace(project)
	if project == "" || strings.ContainsAny(project, `/\`) || project == "." || project == ".." {
		return "", fmt.Errorf("invalid project slug %q", project)
	}
	return filepath.Join(s.root, projectsDirName, project), nil
}

// List trả về mọi entry hợp lệ của project theo thứ tự RelPath tăng;
// file không parse được bị bỏ qua và liệt kê trong skipped (RelPath của chúng).
func (s *Store) List(project string) ([]Entry, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, skipped, err := s.loadProjectLocked(project)
	if err != nil {
		return nil, nil, err
	}
	return cloneEntries(entries), append([]string(nil), skipped...), nil
}

// Save upsert theo khóa tự nhiên (project, category, title).
func (s *Store) Save(project string, e Entry) (Entry, error) {
	e.Title = strings.TrimSpace(e.Title)
	e.Content = strings.TrimSpace(e.Content)
	e.Keywords = normalizeStringList(e.Keywords)
	e.UsageScenario = normalizeStringList(e.UsageScenario)
	e.Origin = defaultOrigin(e.Origin)
	if err := e.Validate(); err != nil {
		return Entry{}, err
	}
	dir, err := s.projectDir(project)
	if err != nil {
		return Entry{}, err
	}
	project = strings.TrimSpace(project)

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Truncate(time.Millisecond)
	entries, _, err := s.loadProjectLocked(project)
	if err != nil {
		return Entry{}, err
	}

	// Upsert: cùng category + cùng title (so sau TrimSpace) -> giữ path cũ.
	relPath := ""
	createdAt := now
	for _, existing := range entries {
		if existing.Category == e.Category && strings.TrimSpace(existing.Title) == e.Title {
			relPath = existing.RelPath
			if !existing.CreatedAt.IsZero() {
				createdAt = existing.CreatedAt
			}
			break
		}
	}
	if relPath == "" {
		relPath, err = s.resolveNewRelPathLocked(entries, project, e.Category, e.Title)
		if err != nil {
			return Entry{}, err
		}
	}

	e.RelPath = relPath
	e.CreatedAt = createdAt
	e.UpdatedAt = now
	raw, err := EncodeFrontmatter(e)
	if err != nil {
		return Entry{}, err
	}
	categoryDir := filepath.Join(dir, string(e.Category))
	if err := os.MkdirAll(categoryDir, 0o755); err != nil {
		return Entry{}, fmt.Errorf("create memory category dir: %w", err)
	}
	targetPath := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(relPath, project+"/")))
	if err := atomicWrite(targetPath, raw); err != nil {
		return Entry{}, err
	}
	delete(s.cache, project)
	return e, nil
}

// resolveNewRelPath chọn tên file: TitleSlug, thêm -2/-3... nếu va chạm với
// entry khác title.
func (s *Store) resolveNewRelPathLocked(entries []Entry, project string, category Category, title string) (string, error) {
	base := TitleSlug(title)
	used := make(map[string]struct{}, len(entries))
	for _, existing := range entries {
		used[filepath.ToSlash(existing.RelPath)] = struct{}{}
	}
	candidate := project + "/" + string(category) + "/" + base + fileSuffix
	if _, ok := used[candidate]; !ok {
		return candidate, nil
	}
	for suffix := 2; suffix <= 99; suffix++ {
		candidate = project + "/" + string(category) + "/" + fmt.Sprintf("%s-%d", base, suffix) + fileSuffix
		if _, ok := used[candidate]; !ok {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("too many memory files collide for title %q", title)
}

// AbsPath chuyển RelPath (dạng "project/category/file.md") thành đường dẫn
// tuyệt đối trên filesystem, phục vụ hiển thị trong kết quả tool.
func (s *Store) AbsPath(relPath string) string {
	relPath = strings.TrimSpace(relPath)
	if s == nil || relPath == "" || strings.TrimSpace(s.root) == "" {
		return relPath
	}
	return filepath.Join(s.root, projectsDirName, filepath.FromSlash(relPath))
}

// Get đọc một entry theo RelPath chính xác (phải nằm trong project).
func (s *Store) Get(project, relPath string) (Entry, error) {
	dir, err := s.projectDir(project)
	if err != nil {
		return Entry{}, err
	}
	full, err := resolveWithinProject(dir, project, relPath)
	if err != nil {
		return Entry{}, err
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return Entry{}, fmt.Errorf("read memory file: %w", err)
	}
	entry, err := DecodeFrontmatter(raw)
	if err != nil {
		return Entry{}, err
	}
	entry.RelPath = filepath.ToSlash(relPath)
	return entry, nil
}

// Delete xóa file memory theo RelPath; invalidate cache project.
func (s *Store) Delete(project, relPath string) error {
	dir, err := s.projectDir(project)
	if err != nil {
		return err
	}
	full, err := resolveWithinProject(dir, project, relPath)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil {
		return fmt.Errorf("delete memory file: %w", err)
	}
	s.mu.Lock()
	delete(s.cache, strings.TrimSpace(project))
	s.mu.Unlock()
	return nil
}

// Search xếp hạng theo FR3 (title×3, keywords×2, body×1; prefix match chịu
// sai chính tả nhẹ). Query rỗng trả toàn bộ List (score 0, mới nhất trước).
func (s *Store) Search(project, query, category string, limit int) ([]ScoredEntry, error) {
	entries, _, err := s.List(project)
	if err != nil {
		return nil, err
	}
	if category != "" {
		if !IsCategory(category) {
			return nil, fmt.Errorf("invalid category filter %q", category)
		}
		filtered := make([]Entry, 0, len(entries))
		for _, e := range entries {
			if string(e.Category) == strings.TrimSpace(category) {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	limit = clampLimit(limit)
	queryTokens := tokenize(query)
	scored := make([]ScoredEntry, 0, len(entries))
	if len(queryTokens) == 0 {
		for _, e := range entries {
			scored = append(scored, ScoredEntry{Entry: e})
		}
	} else {
		for _, e := range entries {
			score, reason := scoreEntry(e, queryTokens)
			if score <= 0 {
				continue
			}
			scored = append(scored, ScoredEntry{Entry: e, Score: score, Reason: reason})
		}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].Entry.UpdatedAt.After(scored[j].Entry.UpdatedAt)
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	return scored, nil
}

// Overview sinh cây category -> "- title [kw1, kw2]" trong ngân sách byte.
// Store trống / budget quá nhỏ cho 1 entry -> trả "" (caller bỏ qua block).
func (s *Store) Overview(project string, maxBytes int) (string, error) {
	entries, _, err := s.List(project)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", nil
	}
	budget := clampOverviewBytes(maxBytes)
	byCategory := make(map[Category][]Entry)
	for _, e := range entries {
		byCategory[e.Category] = append(byCategory[e.Category], e)
	}
	var lines []string
	for _, category := range ValidCategories() {
		group := byCategory[category]
		if len(group) == 0 {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool {
			return group[i].UpdatedAt.After(group[j].UpdatedAt)
		})
		lines = append(lines, "## "+string(category))
		for _, e := range group {
			lines = append(lines, " - "+e.Title+formatOverviewKeywords(e.Keywords))
		}
	}
	full := overviewHeader + "\n" + strings.Join(lines, "\n")
	if len(full) <= budget {
		return full, nil
	}
	// Cắt theo dòng, ưu tiên đã sort (category khai báo, UpdatedAt mới nhất),
	// chừa chỗ cho trailer đếm số bỏ sót.
	trailer := fmt.Sprintf("... and %d more entries (use SearchMemory)", 0)
	body := []string{overviewHeader}
	used := len(overviewHeader) + 1
	included := 0
	for i, line := range lines {
		candidateTrailer := fmt.Sprintf("... and %d more entries (use SearchMemory)", len(lines)-i)
		if used+len(line)+1+len(candidateTrailer)+1 > budget {
			break
		}
		body = append(body, line)
		if !strings.HasPrefix(line, "## ") {
			included++
		}
		used += len(line) + 1
		trailer = candidateTrailer
	}
	if included == 0 {
		return "", nil
	}
	return strings.Join(body, "\n") + "\n" + trailer, nil
}

// --- internals -------------------------------------------------------------

// loadProjectLocked dùng cache theo fingerprint thư mục; caller phải giữ s.mu.
func (s *Store) loadProjectLocked(project string) ([]Entry, []string, error) {
	dir, err := s.projectDir(project)
	if err != nil {
		return nil, nil, err
	}
	project = strings.TrimSpace(project)
	fingerprint, err := dirFingerprint(dir)
	if err != nil {
		return nil, nil, err
	}
	if cached, ok := s.cache[project]; ok && cached.fingerprint == fingerprint {
		return cached.entries, cached.skipped, nil
	}
	var entries []Entry
	var skipped []string
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), fileSuffix) {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		relPath := project + "/" + filepath.ToSlash(rel)
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			skipped = append(skipped, relPath)
			return nil
		}
		entry, decodeErr := DecodeFrontmatter(raw)
		if decodeErr != nil {
			skipped = append(skipped, relPath)
			return nil
		}
		entry.RelPath = relPath
		entries = append(entries, entry)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("scan memory dir: %w", err)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].RelPath < entries[j].RelPath })
	s.cache[project] = &projectCache{fingerprint: fingerprint, entries: entries, skipped: skipped}
	return entries, skipped, nil
}

func dirFingerprint(dir string) (int64, error) {
	h := fnv.New64a()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		fmt.Fprintf(h, "%s|%d|%d\n", filepath.ToSlash(path), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int64(h.Sum64()), nil
}

func resolveWithinProject(dir, project, relPath string) (string, error) {
	relPath = strings.TrimSpace(filepath.ToSlash(relPath))
	prefix := strings.TrimSpace(project) + "/"
	if !strings.HasPrefix(relPath, prefix) {
		return "", fmt.Errorf("memory path %q is outside project scope", relPath)
	}
	inner := strings.TrimPrefix(relPath, prefix)
	if inner == "" || strings.Contains(inner, "..") {
		return "", fmt.Errorf("memory path %q is not a safe relative path", relPath)
	}
	// Clean qua đường dẫn tuyệt đối giả định để vô hiệu hóa mọi segment "..",
	// sau đó kiểm tra kết quả vẫn nằm trong project dir.
	safe := strings.TrimPrefix(filepath.Clean("/"+inner), "/")
	full := filepath.Join(filepath.Clean(dir), filepath.FromSlash(safe))
	cleanDir := filepath.Clean(dir)
	if !strings.HasPrefix(full, cleanDir+string(os.PathSeparator)) {
		return "", fmt.Errorf("memory path %q escapes project scope", relPath)
	}
	return full, nil
}

func atomicWrite(target string, raw []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".mem-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp memory file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op nếu rename thành công
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp memory file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp memory file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp memory file: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("rename memory file: %w", err)
	}
	return nil
}

func scoreEntry(e Entry, queryTokens []string) (float64, string) {
	titleTokens := tokenize(e.Title)
	keywordTokens := tokenize(strings.Join(e.Keywords, " "))
	bodyTokens := tokenize(e.Content)
	var titleHits, kwHits, bodyHits float64
	for _, q := range queryTokens {
		if countMatch(titleTokens, q) > 0 {
			titleHits++
		}
		if countMatch(keywordTokens, q) > 0 {
			kwHits++
		}
		if countMatch(bodyTokens, q) > 0 {
			bodyHits++
		}
	}
	score := titleHits*3 + kwHits*2 + bodyHits*1
	reason := fmt.Sprintf("title:%d kw:%d body:%d", int(titleHits), int(kwHits), int(bodyHits))
	return score, reason
}

func countMatch(tokens []string, query string) int {
	hits := 0
	for _, t := range tokens {
		if t == query || tokenPrefixMatch(t, query) {
			hits++
		}
	}
	return hits
}

// tokenPrefixMatch chịu sai chính tả nhẹ: prefix 3 ký tự đầu khớp nhau.
func tokenPrefixMatch(fieldTok, queryTok string) bool {
	if len(fieldTok) < 3 || len(queryTok) < 3 {
		return false
	}
	a, b := fieldTok[:3], queryTok[:3]
	return strings.HasPrefix(fieldTok, b) || strings.HasPrefix(queryTok, a)
}

func tokenize(text string) []string {
	lowered := strings.ToLower(text)
	parts := strings.FieldsFunc(lowered, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_')
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultLimit
	case limit > maxSearchLimit:
		return maxSearchLimit
	default:
		return limit
	}
}

func clampOverviewBytes(maxBytes int) int {
	switch {
	case maxBytes <= 0:
		return defaultOverviewBytes
	case maxBytes < minOverviewBytes:
		return minOverviewBytes
	case maxBytes > maxOverviewBytes:
		return maxOverviewBytes
	default:
		return maxBytes
	}
}

func formatOverviewKeywords(keywords []string) string {
	shown := make([]string, 0, 4)
	for _, kw := range keywords {
		if len(shown) == 4 {
			break
		}
		if kw = strings.TrimSpace(kw); kw != "" {
			shown = append(shown, kw)
		}
	}
	if len(shown) == 0 {
		return ""
	}
	return " [" + strings.Join(shown, ", ") + "]"
}

func cloneEntries(entries []Entry) []Entry {
	out := make([]Entry, len(entries))
	copy(out, entries)
	return out
}
