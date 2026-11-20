package forwarder

import (
	"fmt"
	"strings"
	"testing"

	"cursor/internal/memory"
)

const captureFixtureHandoff = `# SESSION METADATA & STATUS
- Status: IN_PROGRESS
- Git State: feature/cash-deposit @ 1a2b3c4 (Dirty: 3 uncommitted files)
- Primary Target: Implement cash deposit feature with staged submission workflow

# GOALS & ACCEPTANCE CRITERIA STATUS
- Requirement-level checklist (source of truth for requirement completion):
  - [x] Domain models compile with gradle
  - [ ] Notifier integration — IN PROGRESS: stopped at webhook retry policy
- Specific user preferences & latest explicit directives: reply in Vietnamese and never edit generated bindings

# HARD CONSTRAINTS & ARCHITECTURAL DECISIONS
- Enforced rules, safety boundaries, invariants, and latest user corrections.
- Deposits must flow through staged submission: raw rows land in temp table before accountant conversion
- Rejected approaches & dead ends: direct accounting record creation was rejected for data hygiene
  - Redis cache for prefix state failed because keys are unstable across restarts

# VERIFIED FACTS & ENVIRONMENT
- Exact file paths, symbols, configs, endpoints, ports, running processes, and test commands.
- Backend compiles with ./gradlew.bat compileJava -q from the service directory

# CODEBASE MODIFICATIONS & VALIDATION
- Files created/modified/deleted with concise summary of changes.

# EXECUTION PROGRESS & TODOS
- [Next Concrete Step] Implement webhook retry policy in notifier.go

# RESUME DIRECTIVE FOR NEXT AGENT
- Continue implementing Step 4 in notifier.go
`

func TestExtractMapsRejectedApproachesToPitfall(t *testing.T) {
	handoff := "# HARD CONSTRAINTS & ARCHITECTURAL DECISIONS\n" +
		"- Enforced rules, safety boundaries, invariants, and latest user corrections.\n" +
		"- Deposits must flow through staged submission before accountant conversion.\n" +
		"- Rejected approaches & dead ends: Redis cache for prefix state failed because keys are unstable.\n"
	entries := extractMemoriesFromHandoff(handoff, "d-proj")
	var pitfall, decision int
	for _, e := range entries {
		if e.Origin != memory.OriginAuto {
			t.Fatalf("origin must be auto, got %q", e.Origin)
		}
		switch e.Category {
		case memory.CategoryPitfall:
			pitfall++
			if !strings.HasPrefix(e.Title, "Dead end: ") {
				t.Fatalf("dead end title prefix missing: %q", e.Title)
			}
		case memory.CategoryDecision:
			decision++
			if !strings.HasPrefix(e.Title, "Constraint: ") {
				t.Fatalf("constraint title prefix missing: %q", e.Title)
			}
		}
	}
	if pitfall != 1 || decision != 1 {
		t.Fatalf("mapping wrong decision=%d pitfall=%d entries=%+v", decision, pitfall, entries)
	}
}

func TestExtractFromFullFixture(t *testing.T) {
	entries := extractMemoriesFromHandoff(captureFixtureHandoff, "crm")
	if len(entries) == 0 {
		t.Fatal("expected entries from fixture")
	}
	categories := map[memory.Category]int{}
	for _, e := range entries {
		categories[e.Category]++
		// VERIFIED FACTS / EXECUTION PROGRESS / RESUME không được capture.
		if strings.Contains(e.Content, "gradlew") || strings.Contains(e.Content, "Continue implementing Step 4") {
			t.Fatalf("non-durable section leaked into capture: %q", e.Content)
		}
	}
	if categories[memory.CategoryUserBehavior] != 1 {
		t.Fatalf("directive missing: %+v", categories)
	}
	if categories[memory.CategoryDecision] != 1 || categories[memory.CategoryPitfall] != 1 {
		t.Fatalf("constraint/dead-end counts wrong: %+v", categories)
	}
	if categories[memory.CategoryTaskExperience] != 1 {
		t.Fatalf("primary target capture missing: %+v", categories)
	}
	// Sub-bullet của Rejected approaches phải gộp vào dead end cùng nhóm.
	found := false
	for _, e := range entries {
		if e.Category == memory.CategoryPitfall && strings.Contains(e.Content, "Redis cache") {
			found = true
		}
	}
	if !found {
		t.Fatal("sub-bullet dead end not captured")
	}
}

func TestExtractSkipsUntrustedAndMalformed(t *testing.T) {
	handoff := "# HARD CONSTRAINTS & ARCHITECTURAL DECISIONS\n" +
		"- Git state remains [Unobserved] in this session so nothing is recorded here.\n" +
		"- Another hypothesis marked [Unverified] should also be skipped entirely.\n"
	if entries := extractMemoriesFromHandoff(handoff, "p"); len(entries) != 0 {
		t.Fatalf("untrusted markers must be skipped: %+v", entries)
	}
	if entries := extractMemoriesFromHandoff("plain prose without sections", "p"); len(entries) != 0 {
		t.Fatalf("malformed handoff must yield zero: %+v", entries)
	}
	if entries := extractMemoriesFromHandoff(captureFixtureHandoff, ""); entries != nil {
		t.Fatal("empty project slug must yield nil")
	}
}

func TestExtractCapAndPriorityOrder(t *testing.T) {
	var b strings.Builder
	b.WriteString("# GOALS & ACCEPTANCE CRITERIA STATUS\n")
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&b, "- Specific user preferences & latest explicit directives: always honor directive number %d when working in this repo\n", i)
	}
	b.WriteString("# HARD CONSTRAINTS & ARCHITECTURAL DECISIONS\n")
	for i := 0; i < 6; i++ {
		fmt.Fprintf(&b, "- Architecture constraint item %d explains a durable decision for this codebase\n", i)
	}
	entries := extractMemoriesFromHandoff(b.String(), "p")
	if len(entries) != compactionCaptureMaxEntries {
		t.Fatalf("cap violated: %d", len(entries))
	}
	// Priority: directive (0) trước constraint (1).
	if entries[0].Category != memory.CategoryUserBehavior {
		t.Fatalf("directives must come first, got %+v", entries[0])
	}
}

func TestCaptureMemoriesFromCompactionIdempotentAndGated(t *testing.T) {
	store := memory.NewStore(t.TempDir())
	enabled := &memoryConfigStub{enabled: true, autoCapture: true, overviewMax: 4096}
	service := &Service{memoryStore: store, memoryConfig: enabled}
	stream := &ActiveStream{WorkspacePaths: []string{`d:\proj`}}

	captured, _ := service.captureMemoriesFromCompaction(stream, captureFixtureHandoff)
	if captured == 0 {
		t.Fatal("first run must capture")
	}
	entries, _, err := store.List("d-proj")
	if err != nil || len(entries) != captured {
		t.Fatalf("store mismatch: %d vs %d (%v)", len(entries), captured, err)
	}
	for _, e := range entries {
		if e.Origin != memory.OriginAuto {
			t.Fatalf("captured entry origin must be auto: %+v", e)
		}
	}
	again, skipped := service.captureMemoriesFromCompaction(stream, captureFixtureHandoff)
	if again != 0 {
		t.Fatalf("rerun must capture nothing, got %d", again)
	}
	if skipped != captured {
		t.Fatalf("rerun skipped count wrong: %d vs %d", skipped, captured)
	}

	disabledService := &Service{memoryStore: store, memoryConfig: &memoryConfigStub{enabled: true, autoCapture: false}}
	if got, _ := disabledService.captureMemoriesFromCompaction(stream, captureFixtureHandoff); got != 0 {
		t.Fatal("autoCapture=false must not write")
	}
	offService := &Service{memoryStore: store, memoryConfig: &memoryConfigStub{enabled: false, autoCapture: true}}
	if got, _ := offService.captureMemoriesFromCompaction(stream, captureFixtureHandoff); got != 0 {
		t.Fatal("memoryEnabled=false must not write")
	}
	noStore := &Service{}
	if got, _ := noStore.captureMemoriesFromCompaction(stream, captureFixtureHandoff); got != 0 {
		t.Fatal("nil store must be inert")
	}
}
