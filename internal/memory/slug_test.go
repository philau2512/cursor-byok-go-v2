package memory

import (
	"strings"
	"testing"
)

func TestProjectSlugDeterministic(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{`d:\Admin\Documents\PROJECTS\crm`, "d-admin-documents-projects-crm"},
		{`C:\A B`, "c-a-b"},
		{`/home/u/proj`, "home-u-proj"},
		{`  spaced  path  `, "spaced-path"},
		{`...`, ""},
		{``, ""},
	}
	for _, tc := range cases {
		got := ProjectSlug(tc.input)
		if got != tc.want {
			t.Errorf("ProjectSlug(%q) = %q, want %q", tc.input, got, tc.want)
		}
		if again := ProjectSlug(tc.input); again != got {
			t.Errorf("ProjectSlug not deterministic for %q", tc.input)
		}
	}
}

func TestProjectSlugTrimsLongPaths(t *testing.T) {
	long := `c:\` + strings.Repeat("segment\\", 40) + "tail"
	slug := ProjectSlug(long)
	if len([]rune(slug)) > maxProjectSlugRunes {
		t.Fatalf("slug exceeds cap: %d runes", len([]rune(slug)))
	}
	if strings.HasSuffix(slug, "-") {
		t.Fatalf("trimmed slug must not end with dash: %q", slug)
	}
}

func TestTitleSlugHandlesVietnameseAndCollisionsBase(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Gradle compile command", "gradle-compile-command"},
		{"Tiếng Việt ư ợc", "ti-ng-vi-t-c"},
		{"  Multiple   spaces  ", "multiple-spaces"},
		{"!!!", "untitled"},
		{"", "untitled"},
	}
	for _, tc := range cases {
		if got := TitleSlug(tc.input); got != tc.want {
			t.Errorf("TitleSlug(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
