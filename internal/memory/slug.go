package memory

import (
	"strings"
	"unicode"
)

const (
	maxProjectSlugRunes = 80
	maxTitleSlugRunes   = 60
)

// ProjectSlug biến đường dẫn workspace thành slug thư mục tất định:
// mọi ký tự không chữ/số bị thay bằng '-', kết quả viết thường toàn bộ,
// gộp '-' lặp, trim hai đầu, cắt 80 rune. Ví dụ:
//
//	`d:\Admin\Documents\PROJECTS\crm` -> "d-admin-documents-projects-crm"
//	`/home/u/proj`                    -> "home-u-proj"
//
// Trả "" nếu input không chứa ký tự chữ/số nào.
func ProjectSlug(workspacePath string) string {
	return slugify(workspacePath, maxProjectSlugRunes)
}

// TitleSlug tạo phần tên file từ title: viết thường, bỏ dấu thanh tiếng Việt
// và ký tự ngoài [a-z0-9-], cắt 60 rune; rỗng -> "untitled".
// Title hiển thị luôn lấy từ frontmatter, nên slug chỉ cần tính tất định.
func TitleSlug(title string) string {
	slug := slugify(title, maxTitleSlugRunes)
	if slug == "" {
		return "untitled"
	}
	return slug
}

func slugify(input string, maxRunes int) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || maxRunes <= 0 {
		return ""
	}
	var b strings.Builder
	prevDash := true // suppress leading dash
	for _, r := range strings.ToLower(trimmed) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			// ký tự unicode ngoài ASCII (ví dụ tiếng Việt có dấu): bỏ dấu,
			// coi như separator để không sinh tên file khó dự đoán.
			if !prevDash {
				b.WriteRune('-')
				prevDash = true
			}
		default:
			if !prevDash {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	runes := []rune(slug)
	if len(runes) > maxRunes {
		slug = strings.Trim(string(runes[:maxRunes]), "-")
	}
	return slug
}
