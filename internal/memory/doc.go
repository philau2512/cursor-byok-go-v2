// Package memory cung cấp kho project memory file-backed cho local mode.
//
// Mỗi memory là một file markdown với YAML frontmatter, layout:
//
//	<root>/projects/<projectSlug>/<category>/<titleSlug>.md
//
// Khóa tự nhiên của entry là (projectSlug, category, title): Save là upsert
// theo khóa này, nên đổi title sẽ tạo entry mới có chủ đích (dedup ở tầng
// cao hơn dựa trên content). Package này không đọc/ghi dữ liệu của hệ thống
// memory nào khác và không tự từ chối secret — validation nội dung (secret
// guard, rate limit) thuộc trách nhiệm caller phía forwarder.
package memory
