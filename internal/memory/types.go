package memory

import (
	"fmt"
	"strings"
	"time"
)

// Category là tập giá trị đóng; giá trị lưu trên disk đúng bằng tên hằng số.
type Category string

const (
	CategoryTechStack      Category = "project_tech_stack"
	CategoryBuildConfig    Category = "project_build_configuration"
	CategoryEnvConfig      Category = "project_environment_configuration"
	CategoryArchitecture   Category = "project_architecture"
	CategoryCodeSpec       Category = "development_code_specification"
	CategoryPracticeSpec   Category = "development_practice_specification"
	CategoryTestSpec       Category = "development_test_specification"
	CategoryPitfall        Category = "common_pitfalls_experience"
	CategoryDecision       Category = "important_decision_experience"
	CategoryTaskExperience Category = "task_experience"
	CategoryUserBehavior   Category = "user_behavior"
	CategoryUserPreference Category = "user_communication"
)

var categorySet = map[Category]struct{}{
	CategoryTechStack:      {},
	CategoryBuildConfig:    {},
	CategoryEnvConfig:      {},
	CategoryArchitecture:   {},
	CategoryCodeSpec:       {},
	CategoryPracticeSpec:   {},
	CategoryTestSpec:       {},
	CategoryPitfall:        {},
	CategoryDecision:       {},
	CategoryTaskExperience: {},
	CategoryUserBehavior:   {},
	CategoryUserPreference: {},
}

// ValidCategories trả về danh mục hợp lệ theo thứ tự khai báo.
func ValidCategories() []Category {
	return []Category{
		CategoryTechStack,
		CategoryBuildConfig,
		CategoryEnvConfig,
		CategoryArchitecture,
		CategoryCodeSpec,
		CategoryPracticeSpec,
		CategoryTestSpec,
		CategoryPitfall,
		CategoryDecision,
		CategoryTaskExperience,
		CategoryUserBehavior,
		CategoryUserPreference,
	}
}

// IsCategory kiểm tra tên category hợp lệ.
func IsCategory(c string) bool {
	_, ok := categorySet[Category(strings.TrimSpace(c))]
	return ok
}

// Origin phân tách nguồn gốc entry để audit mức độ tin cậy.
type Origin string

const (
	OriginUser  Origin = "user"  // người dùng yêu cầu nhớ
	OriginModel Origin = "model" // model chủ động ghi qua tool
	OriginAuto  Origin = "auto"  // trích tự động từ compaction handoff
)

func isValidOrigin(o Origin) bool {
	switch o {
	case OriginUser, OriginModel, OriginAuto:
		return true
	default:
		return false
	}
}

// Giới hạn validation dùng chung cho Save và caller phía tool.
const (
	MinTitleRunes    = 3
	MaxTitleRunes    = 100
	MinContentRunes  = 1
	MaxContentRunes  = 4000
	MaxKeywords      = 8
	MaxKeywordRunes  = 60
	MaxScenarios     = 3
	MaxScenarioRunes = 160
)

// Entry là một bản ghi memory nguyên tử (một topic duy nhất).
type Entry struct {
	Title         string
	Category      Category
	Keywords      []string
	UsageScenario []string
	Origin        Origin
	Content       string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// RelPath là đường dẫn tương đối so với <root>/projects, dạng
	// "projectSlug/category/file.md" (luôn dùng slash). Store gán khi đọc/ghi.
	RelPath string
}

// Validate trả về lỗi mô tả rõ vi phạm đầu tiên theo thứ tự kiểm tra.
func (e Entry) Validate() error {
	title := strings.TrimSpace(e.Title)
	titleRunes := []rune(title)
	if len(titleRunes) < MinTitleRunes || len(titleRunes) > MaxTitleRunes {
		return fmt.Errorf("title must be %d-%d characters, got %d", MinTitleRunes, MaxTitleRunes, len(titleRunes))
	}
	if !IsCategory(string(e.Category)) {
		return fmt.Errorf("invalid category %q; valid: %s", e.Category, joinCategories())
	}
	content := strings.TrimSpace(e.Content)
	contentRunes := []rune(content)
	if len(contentRunes) < MinContentRunes || len(contentRunes) > MaxContentRunes {
		return fmt.Errorf("content must be %d-%d characters, got %d", MinContentRunes, MaxContentRunes, len(contentRunes))
	}
	if len(e.Keywords) > MaxKeywords {
		return fmt.Errorf("keywords must be at most %d, got %d", MaxKeywords, len(e.Keywords))
	}
	for _, kw := range e.Keywords {
		if len([]rune(strings.TrimSpace(kw))) > MaxKeywordRunes {
			return fmt.Errorf("keyword %q exceeds %d characters", kw, MaxKeywordRunes)
		}
	}
	if len(e.UsageScenario) > MaxScenarios {
		return fmt.Errorf("usage_scenario must be at most %d, got %d", MaxScenarios, len(e.UsageScenario))
	}
	for _, sc := range e.UsageScenario {
		if len([]rune(strings.TrimSpace(sc))) > MaxScenarioRunes {
			return fmt.Errorf("usage_scenario %q exceeds %d characters", sc, MaxScenarioRunes)
		}
	}
	if e.Origin != "" && !isValidOrigin(e.Origin) {
		return fmt.Errorf("invalid origin %q; valid: user, model, auto", e.Origin)
	}
	return nil
}

func joinCategories() string {
	parts := make([]string, 0, len(categorySet))
	for _, c := range ValidCategories() {
		parts = append(parts, string(c))
	}
	return strings.Join(parts, ", ")
}
