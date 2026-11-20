package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

func sampleEntry(title string) Entry {
	return Entry{
		Title:    title,
		Category: CategoryBuildConfig,
		Keywords: []string{"gradle", "compileJava"},
		Content:  "Use ./gradlew.bat compileJava -q from the service directory.",
		Origin:   OriginModel,
	}
}

func TestSaveIsIdempotentUpsertByKey(t *testing.T) {
	s := newTestStore(t)
	e := sampleEntry("Gradle compile command")
	first, err := s.Save("d-proj", e)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Save("d-proj", e)
	if err != nil {
		t.Fatal(err)
	}
	if second.RelPath != first.RelPath {
		t.Fatalf("expected upsert same path, got %q vs %q", first.RelPath, second.RelPath)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatal("upsert must preserve CreatedAt")
	}
	if second.UpdatedAt.Before(first.UpdatedAt) {
		t.Fatal("UpdatedAt must not go backwards")
	}
	entries, skipped, err := s.List("d-proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || len(skipped) != 0 {
		t.Fatalf("want exactly 1 entry, got %d (skipped %v)", len(entries), skipped)
	}
}

func TestSaveTitleCollisionDifferentTitlesGetsSuffix(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Save("p", sampleEntry("Keep it simple")); err != nil {
		t.Fatal(err)
	}
	second, err := s.Save("p", sampleEntry("Keep  It  Simple!"))
	if err != nil {
		t.Fatal(err)
	}
	// "keep-it-simple" và "keep-it-simple" slug giống nhau nhưng title khác
	// (so sau trim thì "Keep  It  Simple!" != "Keep it simple") -> hậu tố -2.
	if !strings.HasSuffix(second.RelPath, "keep-it-simple-2.md") {
		t.Fatalf("expected collision suffix, got %q", second.RelPath)
	}
}

func TestSaveValidationWritesNothing(t *testing.T) {
	s := newTestStore(t)
	cases := []Entry{
		{Title: "ab", Category: CategoryPitfall, Content: "x"},
		{Title: "valid title", Category: Category("nope"), Content: "x"},
		{Title: "valid title", Category: CategoryPitfall, Content: strings.Repeat("x", MaxContentRunes+1)},
	}
	for i, e := range cases {
		if _, err := s.Save("p", e); err == nil {
			t.Fatalf("case %d must fail validation", i)
		}
	}
	root := filepath.Join(s.Root(), "projects", "p")
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("no directory must be created on validation failure")
	}
}

func TestListSkipsCorruptFiles(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Save("p", sampleEntry("Good entry")); err != nil {
		t.Fatal(err)
	}
	corruptDir := filepath.Join(s.Root(), "projects", "p", string(CategoryPitfall))
	if err := os.MkdirAll(corruptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corruptDir, "broken.md"), []byte("garbage without frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, skipped, err := s.List("p")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Title != "Good entry" {
		t.Fatalf("entries = %+v", entries)
	}
	if len(skipped) != 1 || !strings.HasSuffix(skipped[0], "broken.md") {
		t.Fatalf("skipped = %v", skipped)
	}
}

func TestSearchScoringAndLimit(t *testing.T) {
	s := newTestStore(t)
	mustSave(t, s, "p", Entry{Title: "Gradle compile command", Category: CategoryBuildConfig,
		Keywords: []string{"gradle"}, Content: "compile java with gradlew", Origin: OriginModel})
	mustSave(t, s, "p", Entry{Title: "Unrelated note", Category: CategoryPitfall,
		Keywords: []string{"mysql"}, Content: "mentions gradle once in body", Origin: OriginAuto})
	mustSave(t, s, "p", Entry{Title: "Spring boot restart", Category: CategoryTechStack,
		Keywords: []string{"spring"}, Content: "devtools auto restart", Origin: OriginUser})

	results, err := s.Search("p", "gradle", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 hits, got %d: %+v", len(results), results)
	}
	if results[0].Entry.Title != "Gradle compile command" {
		t.Fatalf("title match must rank first, got %q", results[0].Entry.Title)
	}
	if results[0].Score <= results[1].Score {
		t.Fatalf("scores not ordered: %v vs %v", results[0].Score, results[1].Score)
	}
	if !strings.Contains(results[0].Reason, "title:1") {
		t.Fatalf("reason missing title hit: %q", results[0].Reason)
	}

	// Prefix chịu sai chính tả nhẹ: "compil" phải khớp "compile".
	prefixHits, err := s.Search("p", "compil", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixHits) == 0 || prefixHits[0].Entry.Title != "Gradle compile command" {
		t.Fatalf("prefix match failed: %+v", prefixHits)
	}

	// Category filter + limit.
	filtered, err := s.Search("p", "gradle", "common_pitfalls_experience", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].Entry.Title != "Unrelated note" {
		t.Fatalf("category filter wrong: %+v", filtered)
	}
	if emptyQuery, err := s.Search("p", "", "", 2); err != nil || len(emptyQuery) != 2 {
		t.Fatalf("empty query should return limited list: %v %d", err, len(emptyQuery))
	}
}

func TestOverviewBudgetAndTrailer(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 40; i++ {
		e := Entry{
			Title:    fmt.Sprintf("Memory item number %02d with some length to consume budget", i),
			Category: CategoryTaskExperience,
			Keywords: []string{"kw", "another", "third", "fourth"},
			Content:  "content body",
			Origin:   OriginModel,
		}
		mustSave(t, s, "p", e)
	}
	full, err := s.Overview("p", 16384)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full, "Memory item number 39") || strings.Contains(full, "more entries") {
		t.Fatalf("full overview wrong:\n%s", full)
	}
	if !strings.HasPrefix(full, overviewHeader) {
		t.Fatal("missing header")
	}
	if !strings.Contains(full, "## task_experience") {
		t.Fatal("missing category line")
	}

	trimmed, err := s.Overview("p", 512)
	if err != nil {
		t.Fatal(err)
	}
	if len(trimmed) > 512 {
		t.Fatalf("budget violated: %d bytes", len(trimmed))
	}
	if !strings.Contains(trimmed, "more entries (use SearchMemory)") {
		t.Fatalf("missing trailer:\n%s", trimmed)
	}

	empty := newTestStore(t)
	if got, err := empty.Overview("nothing", 4096); err != nil || got != "" {
		t.Fatalf("empty store overview should be blank: %q %v", got, err)
	}
}

func TestGetDeleteAndPathTraversalGuard(t *testing.T) {
	s := newTestStore(t)
	saved, err := s.Save("p", sampleEntry("Delete me please"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("p", saved.RelPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Delete me please" {
		t.Fatalf("get mismatch: %+v", got)
	}
	if _, err := s.Get("p", "p/../../secret.md"); err == nil {
		t.Fatal("traversal must be rejected")
	}
	if _, err := s.Get("p", "other/file.md"); err == nil {
		t.Fatal("cross-project path must be rejected")
	}
	if err := s.Delete("p", saved.RelPath); err != nil {
		t.Fatal(err)
	}
	entries, _, err := s.List("p")
	if err != nil || len(entries) != 0 {
		t.Fatalf("after delete want empty, got %d (%v)", len(entries), err)
	}
	if err := s.Delete("p", saved.RelPath); err == nil {
		t.Fatal("double delete must error")
	}
}

func TestCacheInvalidatesOnExternalEdit(t *testing.T) {
	s := newTestStore(t)
	saved, err := s.Save("p", sampleEntry("Externally edited"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.List("p"); err != nil {
		t.Fatal(err)
	}
	// Sửa file từ ngoài (người dùng audit bằng editor): fingerprint phải đổi.
	// RelPath là "p/category/file.md" so với projects/, nên suy đường dẫn thật:
	path := filepath.Join(s.Root(), "projects", filepath.FromSlash(saved.RelPath))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), "gradle", "gradle-external", 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	// Đảm bảo mtime đổi với filesystem granularity thô.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search("p", "gradle-external", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("external edit must be visible after fingerprint change")
	}
}

func TestConcurrentSavesSameTitle(t *testing.T) {
	s := newTestStore(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Save("p", sampleEntry("Contended title")); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent save: %v", err)
	}
	entries, _, err := s.List("p")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want single upserted entry, got %d", len(entries))
	}
}

func TestProjectSlugGuard(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Save(`..\evil`, sampleEntry("Bad project")); err == nil {
		t.Fatal("invalid slug must be rejected")
	}
	if _, err := s.Save("", sampleEntry("Empty project")); err == nil {
		t.Fatal("empty slug must be rejected")
	}
}

func mustSave(t *testing.T, s *Store, project string, e Entry) {
	t.Helper()
	if _, err := s.Save(project, e); err != nil {
		t.Fatalf("save %q: %v", e.Title, err)
	}
}
