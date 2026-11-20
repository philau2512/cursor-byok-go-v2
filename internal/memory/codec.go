package memory

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Codec frontmatter: thứ tự khóa cố định để git-diff ổn định giữa các lần ghi.
type frontmatterPayload struct {
	Title         string   `yaml:"title"`
	Category      string   `yaml:"category"`
	Keywords      []string `yaml:"keywords,omitempty"`
	UsageScenario []string `yaml:"usage_scenario,omitempty"`
	Origin        string   `yaml:"origin"`
	CreatedAt     string   `yaml:"created_at"`
	UpdatedAt     string   `yaml:"updated_at"`
}

var (
	ErrNoFrontmatter         = errors.New("memory file missing leading --- frontmatter delimiter")
	ErrInvalidFrontmatter    = errors.New("memory frontmatter is not valid yaml")
	ErrEmptyTitle            = errors.New("memory frontmatter title is required")
	ErrInvalidCategory       = errors.New("memory frontmatter category is invalid")
	frontmatterTimeLayout    = time.RFC3339Nano
	frontmatterDefaultOrigin = OriginModel
)

// EncodeFrontmatter trả về toàn bộ nội dung file: block ---...--- theo thứ tự
// khóa cố định, tiếp theo là body (content đã trim) và newline kết thúc.
func EncodeFrontmatter(e Entry) ([]byte, error) {
	payload := frontmatterPayload{
		Title:         strings.TrimSpace(e.Title),
		Category:      string(e.Category),
		Keywords:      normalizeStringList(e.Keywords),
		UsageScenario: normalizeStringList(e.UsageScenario),
		Origin:        string(defaultOrigin(e.Origin)),
		CreatedAt:     formatFrontmatterTime(e.CreatedAt),
		UpdatedAt:     formatFrontmatterTime(e.UpdatedAt),
	}
	block, err := yaml.Marshal(&payload)
	if err != nil {
		return nil, fmt.Errorf("encode memory frontmatter: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(block)
	buf.WriteString("---\n\n")
	buf.WriteString(strings.TrimSpace(e.Content))
	buf.WriteString("\n")
	return buf.Bytes(), nil
}

// DecodeFrontmatter đọc file memory. File thiếu khóa phụ được chấp nhận với
// mặc định: keywords/usage_scenario rỗng, origin=model, timestamps zero.
func DecodeFrontmatter(raw []byte) (Entry, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return Entry{}, ErrNoFrontmatter
	}
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		// Chỉ nhận dòng đúng bằng "---" để đóng block, tránh cắt nhầm body
		// chứa markdown horizontal rule hoặc phân cách "---" giữa dòng.
		if lines[i] == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return Entry{}, ErrNoFrontmatter
	}
	var payload frontmatterPayload
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:closeIdx], "\n")), &payload); err != nil {
		return Entry{}, fmt.Errorf("%w: %v", ErrInvalidFrontmatter, err)
	}
	title := strings.TrimSpace(payload.Title)
	if title == "" {
		return Entry{}, ErrEmptyTitle
	}
	category := Category(strings.TrimSpace(payload.Category))
	if !IsCategory(string(category)) {
		return Entry{}, fmt.Errorf("%w: %q", ErrInvalidCategory, payload.Category)
	}
	origin := Origin(strings.TrimSpace(payload.Origin))
	if origin == "" {
		origin = frontmatterDefaultOrigin
	}
	if !isValidOrigin(origin) {
		origin = frontmatterDefaultOrigin
	}
	body := strings.TrimSpace(strings.Join(lines[closeIdx+1:], "\n"))
	entry := Entry{
		Title:         title,
		Category:      category,
		Keywords:      normalizeStringList(payload.Keywords),
		UsageScenario: normalizeStringList(payload.UsageScenario),
		Origin:        origin,
		Content:       body,
		CreatedAt:     parseFrontmatterTime(payload.CreatedAt),
		UpdatedAt:     parseFrontmatterTime(payload.UpdatedAt),
	}
	return entry, nil
}

func defaultOrigin(o Origin) Origin {
	if o == "" {
		return OriginModel
	}
	return o
}

func normalizeStringList(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func formatFrontmatterTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(frontmatterTimeLayout)
}

func parseFrontmatterTime(value string) time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(frontmatterTimeLayout, trimmed)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}
