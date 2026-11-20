package memory

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCodecRoundTripPreservesAllFields(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 30, 0, 123456000, time.UTC)
	entry := Entry{
		Title:         "Gradle compile command",
		Category:      CategoryBuildConfig,
		Keywords:      []string{"gradle", "compileJava"},
		UsageScenario: []string{"When verifying a backend change, compile with gradlew."},
		Origin:        OriginUser,
		Content:       "Run ./gradlew.bat compileJava -q from the service dir.\n\n---\nThis horizontal rule must not break decoding.",
		CreatedAt:     now,
		UpdatedAt:     now.Add(time.Minute),
	}
	raw, err := EncodeFrontmatter(entry)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeFrontmatter(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Title != entry.Title || decoded.Category != entry.Category {
		t.Fatalf("identity lost: %+v", decoded)
	}
	if decoded.Origin != OriginUser {
		t.Fatalf("origin = %q", decoded.Origin)
	}
	if len(decoded.Keywords) != 2 || decoded.Keywords[1] != "compileJava" {
		t.Fatalf("keywords = %#v", decoded.Keywords)
	}
	if len(decoded.UsageScenario) != 1 {
		t.Fatalf("usage_scenario = %#v", decoded.UsageScenario)
	}
	if !strings.Contains(decoded.Content, "horizontal rule") {
		t.Fatalf("body truncated at inner ---: %q", decoded.Content)
	}
	if !decoded.CreatedAt.Equal(entry.CreatedAt) || !decoded.UpdatedAt.Equal(entry.UpdatedAt) {
		t.Fatalf("timestamps lost: created %v updated %v", decoded.CreatedAt, decoded.UpdatedAt)
	}
}

func TestCodecKeyOrderIsStable(t *testing.T) {
	entry := Entry{Title: "Stable order", Category: CategoryPitfall, Content: "x", Origin: OriginModel}
	first, err := EncodeFrontmatter(entry)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeFrontmatter(entry)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("encoding not deterministic")
	}
	for _, want := range []string{"title:", "category:", "origin:", "created_at:", "updated_at:"} {
		if !strings.Contains(string(first), want) {
			t.Fatalf("missing key %q in:\n%s", want, first)
		}
	}
	if idxTitle, idxCategory := strings.Index(string(first), "title:"), strings.Index(string(first), "category:"); idxTitle > idxCategory {
		t.Fatal("title must precede category")
	}
}

func TestCodecRejectsBadFiles(t *testing.T) {
	if _, err := DecodeFrontmatter([]byte("no frontmatter here")); !errors.Is(err, ErrNoFrontmatter) {
		t.Fatalf("want ErrNoFrontmatter, got %v", err)
	}
	if _, err := DecodeFrontmatter([]byte("---\ntitle: \"\"\n---\n\nbody")); !errors.Is(err, ErrEmptyTitle) {
		t.Fatalf("want ErrEmptyTitle, got %v", err)
	}
	if _, err := DecodeFrontmatter([]byte("---\ntitle: Ok Title\ncategory: nope_category\n---\n\nbody")); !errors.Is(err, ErrInvalidCategory) {
		t.Fatalf("want ErrInvalidCategory, got %v", err)
	}
	if _, err := DecodeFrontmatter([]byte("---\ntitle: [unclosed\n---\n\nbody")); !errors.Is(err, ErrInvalidFrontmatter) {
		t.Fatalf("want ErrInvalidFrontmatter, got %v", err)
	}
}

func TestCodecDefaultsOnMissingOptionalKeys(t *testing.T) {
	decoded, err := DecodeFrontmatter([]byte("---\ntitle: Minimal Entry\ncategory: task_experience\n---\n\nJust body.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Origin != OriginModel {
		t.Fatalf("default origin should be model, got %q", decoded.Origin)
	}
	if len(decoded.Keywords) != 0 || !decoded.CreatedAt.IsZero() {
		t.Fatalf("optional keys should default empty: %+v", decoded)
	}
}

func TestEntryValidation(t *testing.T) {
	base := Entry{Title: "Valid title", Category: CategoryPitfall, Content: "content", Origin: OriginModel}
	if err := base.Validate(); err != nil {
		t.Fatalf("base should pass: %v", err)
	}
	if err := (Entry{Title: "ab", Category: CategoryPitfall, Content: "x"}).Validate(); err == nil {
		t.Fatal("short title must fail")
	}
	if err := (Entry{Title: "ok title", Category: Category("bogus"), Content: "x"}).Validate(); err == nil {
		t.Fatal("bad category must fail")
	}
	if err := (Entry{Title: "ok title", Category: CategoryPitfall, Content: strings.Repeat("x", MaxContentRunes+1)}).Validate(); err == nil {
		t.Fatal("oversized content must fail")
	}
	long := strings.Repeat("k", MaxKeywordRunes+1)
	if err := (Entry{Title: "ok title", Category: CategoryPitfall, Content: "x", Keywords: []string{long}}).Validate(); err == nil {
		t.Fatal("oversized keyword must fail")
	}
	scenarios := make([]string, MaxScenarios+1)
	for i := range scenarios {
		scenarios[i] = "scenario"
	}
	if err := (Entry{Title: "ok title", Category: CategoryPitfall, Content: "x", UsageScenario: scenarios}).Validate(); err == nil {
		t.Fatal("too many scenarios must fail")
	}
}
