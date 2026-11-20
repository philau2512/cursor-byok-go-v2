package forwarder

import (
	"strings"
	"testing"

	"cursor/gen/aiserverv1"
)

func TestSummarizeCommitMessageFilesCountsAdditionsAndDeletions(t *testing.T) {
	diffs := []string{
		"diff --git a/src/a/Foo.java src/a/Foo.java\n" +
			"index 111..222 100644\n" +
			"--- a/src/a/Foo.java\n" +
			"+++ src/a/Foo.java\n" +
			"@@ -1,2 +1,3 @@\n" +
			" context\n" +
			"+added one\n" +
			"+added two\n" +
			"-removed one\n",
		"diff --git a/web/app.ts web/app.ts\n" +
			"--- a/web/app.ts\n" +
			"+++ web/app.ts\n" +
			"@@ -1 +1 @@\n" +
			"-old\n" +
			"+new\n",
		// diff trùng file đầu -> count gộp vào cùng một mục, thứ tự first-seen giữ nguyên
		"diff --git a/src/a/Foo.java src/a/Foo.java\n" +
			"@@ -9 +9 @@\n" +
			"+again\n",
	}

	files := summarizeCommitMessageFiles(diffs)
	if len(files) != 2 {
		t.Fatalf("expected 2 unique files, got %d: %+v", len(files), files)
	}
	if files[0].Path != "src/a/Foo.java" || files[0].Additions != 3 || files[0].Deletions != 1 {
		t.Fatalf("Foo.java = %+v, want +3/-1", files[0])
	}
	if files[1].Path != "web/app.ts" || files[1].Additions != 1 || files[1].Deletions != 1 {
		t.Fatalf("app.ts = %+v, want +1/-1", files[1])
	}
}

func TestSummarizeCommitMessageFilesIgnoresContentWithoutHeader(t *testing.T) {
	files := summarizeCommitMessageFiles([]string{"just some text\n+not a diff\n"})
	if len(files) != 0 {
		t.Fatalf("expected no files without diff header, got %+v", files)
	}
}

func TestFormatCommitMessageManifestHeaderAndOmission(t *testing.T) {
	manifest := formatCommitMessageManifest([]commitMessageFileChange{
		{Path: "one.ts", Additions: 10, Deletions: 2},
		{Path: "two.ts"},
	})
	if !strings.HasPrefix(manifest, "Files changed (2):") {
		t.Fatalf("manifest header wrong: %q", manifest)
	}
	if !strings.Contains(manifest, "- one.ts (+10/-2)") || !strings.Contains(manifest, "- two.ts (+0/-0)") {
		t.Fatalf("manifest rows wrong: %q", manifest)
	}
	if strings.Contains(manifest, "more files") {
		t.Fatal("small manifest should not carry omission note")
	}

	many := make([]commitMessageFileChange, commitMessageManifestMaxFiles+7)
	for i := range many {
		many[i] = commitMessageFileChange{Path: "x"}
	}
	big := formatCommitMessageManifest(many)
	if !strings.Contains(big, "Files changed (207):") || !strings.Contains(big, "- ... and 7 more files") {
		t.Fatalf("oversized manifest missing cap note: %q", big)
	}
}

func TestCommitMessageOutputBudget(t *testing.T) {
	if got := commitMessageOutputBudget(0); got != commitMessageBaseOutputTokens {
		t.Fatalf("empty change budget = %d, want base", got)
	}
	if got := commitMessageOutputBudget(10); got != commitMessageBaseOutputTokens+10*commitMessagePerFileOutputTokens {
		t.Fatalf("adaptive budget = %d", got)
	}
	if got := commitMessageOutputBudget(10_000); got != commitMessageMaxOutputTokens {
		t.Fatalf("budget must clamp at cap, got %d", got)
	}
	if got := commitMessageOutputBudget(-5); got != commitMessageBaseOutputTokens {
		t.Fatalf("negative count must fall back to base, got %d", got)
	}
}

func TestBuildCommitMessagePromptIncludesManifestForTruncatedDiffs(t *testing.T) {
	req := &aiserverv1.WriteGitCommitMessageRequest{
		Diffs: []string{
			"diff --git a/kept.ts kept.ts\n@@ -1 +1 @@\n+kept\n",
			// diff thứ hai bị pipeline truncate bỏ hẳn khỏi `diffs` nhưng vẫn phải
			// xuất hiện trong manifest để model không quên domain này.
			"diff --git a/dropped.ts dropped.ts\n@@ -1 +2 @@\n+dropped line\n",
		},
	}
	truncated := []string{req.Diffs[0]}

	messages, changedCount, err := buildCommitMessagePrompt(req, truncated)
	if err != nil {
		t.Fatalf("buildCommitMessagePrompt: %v", err)
	}
	if changedCount != 2 {
		t.Fatalf("changed file count = %d, want 2", changedCount)
	}
	userContent := messages[1].Content
	for _, want := range []string{"Files changed (2):", "- kept.ts (+1/-0)", "- dropped.ts (+1/-0)"} {
		if !strings.Contains(userContent, want) {
			t.Fatalf("prompt missing %q\n---\n%s", want, userContent)
		}
	}
	if !strings.Contains(userContent, "--- Diff 1 ---") {
		t.Fatal("prompt missing diff section")
	}
	if strings.Contains(userContent, "--- Diff 2 ---") {
		t.Fatal("truncated diff must not be re-included into diff section")
	}
}
