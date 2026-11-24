package promptengine

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	"cursor/gen/agentv1"
)

func getCandidateRoots(requestContext *agentv1.RequestContext) []string {
	seen := make(map[string]struct{})
	var roots []string

	addRoot := func(p string) {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			return
		}
		clean := filepath.Clean(trimmed)
		key := strings.ToLower(filepath.ToSlash(clean))
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			roots = append(roots, clean)
		}
	}

	if requestContext != nil {
		if env := requestContext.GetEnv(); env != nil {
			for _, wp := range env.GetWorkspacePaths() {
				addRoot(wp)
			}
			if pf := env.GetProjectFolder(); !isCursorInternalProjectFolder(pf) {
				addRoot(pf)
			}
		}
		for _, repo := range requestContext.GetGitRepos() {
			if repo != nil {
				addRoot(repo.GetPath())
			}
		}
	}

	if len(roots) == 0 {
		if wd, err := os.Getwd(); err == nil && wd != "" {
			addRoot(wd)
		}
	}

	// Also check parent directories if root is nested inside a workspace
	for _, root := range append([]string(nil), roots...) {
		curr := root
		for i := 0; i < 3; i++ {
			parent := filepath.Dir(curr)
			if parent == curr || parent == "" || parent == "." {
				break
			}
			if hasProjectMarker(parent) {
				addRoot(parent)
			}
			curr = parent
		}
	}

	return roots
}

func isCursorInternalProjectFolder(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	slash := strings.ToLower(filepath.ToSlash(trimmed))
	return strings.Contains(slash, "/.cursor/projects")
}

var (
	projectMarkerCacheMu sync.RWMutex
	projectMarkerCache   = make(map[string]bool)
)

func hasProjectMarker(dir string) bool {
	cleanDir := filepath.Clean(dir)
	key := strings.ToLower(filepath.ToSlash(cleanDir))
	projectMarkerCacheMu.RLock()
	cached, ok := projectMarkerCache[key]
	projectMarkerCacheMu.RUnlock()
	if ok {
		return cached
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if strings.EqualFold(cleanDir, filepath.Clean(home)) {
			projectMarkerCacheMu.Lock()
			projectMarkerCache[key] = false
			projectMarkerCacheMu.Unlock()
			return false
		}
	}
	markers := []string{".cursor", ".agents", ".git"}
	found := false
	for _, m := range markers {
		if info, err := os.Stat(filepath.Join(cleanDir, m)); err == nil && info.IsDir() {
			found = true
			break
		}
	}
	projectMarkerCacheMu.Lock()
	projectMarkerCache[key] = found
	projectMarkerCacheMu.Unlock()
	return found
}

func parseFrontmatter(content string) (map[string]interface{}, string) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "---") {
		return nil, trimmed
	}
	rest := trimmed[3:]
	idx := strings.Index(rest, "---")
	if idx == -1 {
		return nil, trimmed
	}
	yamlBlock := rest[:idx]
	body := strings.TrimSpace(rest[idx+3:])

	var raw map[string]interface{}
	if err := yaml.Unmarshal([]byte(yamlBlock), &raw); err != nil {
		return nil, body
	}
	return raw, body
}

func extractFallbackDescription(body string, fallback string) string {
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimLeft(line, "# ")
		if line != "" {
			return line
		}
	}
	return fallback
}

type discoveredLocalSkill struct {
	absPath     string
	pathKey     string
	nameKey     string
	content     string
	description string
}

type cachedLocalSkillsDir struct {
	exists  bool
	modTime time.Time
	skills  []discoveredLocalSkill
}

var (
	localSkillsCacheMu sync.RWMutex
	localSkillsCache   = make(map[string]cachedLocalSkillsDir)
)

// mergeLocalSkills 自动发现工作区及用户主目录下的 skills 目录，合并至 requestContext。
func mergeLocalSkills(requestContext *agentv1.RequestContext) {
	if requestContext == nil {
		return
	}

	knownPaths := make(map[string]struct{})
	knownNames := make(map[string]struct{})

	for _, s := range requestContext.GetAgentSkills() {
		if s == nil {
			continue
		}
		pathKey := strings.ToLower(filepath.ToSlash(filepath.Clean(s.GetFullPath())))
		if pathKey != "" {
			knownPaths[pathKey] = struct{}{}
		}
	}

	roots := getCandidateRoots(requestContext)
	var skillDirs []string
	for _, root := range roots {
		skillDirs = append(skillDirs,
			filepath.Join(root, ".cursor", "skills"),
			filepath.Join(root, ".agents", "skills"),
			filepath.Join(root, ".claude", "skills"),
		)
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		skillDirs = append(skillDirs,
			filepath.Join(home, ".cursor", "skills"),
			filepath.Join(home, ".agents", "skills"),
			filepath.Join(home, ".claude", "skills"),
			filepath.Join(home, ".cursor-local-assistant-v2", "skills"),
		)
	}

	for _, dir := range skillDirs {
		for _, item := range loadLocalSkillsFromDir(dir) {
			if _, ok := knownPaths[item.pathKey]; ok {
				continue
			}
			if item.nameKey != "" {
				if _, ok := knownNames[item.nameKey]; ok {
					continue
				}
			}
			knownPaths[item.pathKey] = struct{}{}
			if item.nameKey != "" {
				knownNames[item.nameKey] = struct{}{}
			}
			requestContext.AgentSkills = append(requestContext.AgentSkills, &agentv1.AgentSkill{
				FullPath:               item.absPath,
				Content:                item.content,
				Description:            item.description,
				DisableModelInvocation: false,
			})
		}
	}
}

func loadLocalSkillsFromDir(dir string) []discoveredLocalSkill {
	cleanDir := filepath.Clean(dir)
	dirKey := strings.ToLower(filepath.ToSlash(cleanDir))

	info, statErr := os.Stat(cleanDir)
	exists := statErr == nil && info.IsDir()

	localSkillsCacheMu.RLock()
	cached, ok := localSkillsCache[dirKey]
	localSkillsCacheMu.RUnlock()
	if ok {
		if !exists && !cached.exists {
			return nil
		}
		if exists && cached.exists && cached.modTime.Equal(info.ModTime()) {
			return cached.skills
		}
	}

	if !exists {
		localSkillsCacheMu.Lock()
		localSkillsCache[dirKey] = cachedLocalSkillsDir{exists: false}
		localSkillsCacheMu.Unlock()
		return nil
	}

	entries, err := os.ReadDir(cleanDir)
	if err != nil {
		return nil
	}
	skills := make([]discoveredLocalSkill, 0, len(entries))
	for _, entry := range entries {
		entryPath := filepath.Join(cleanDir, entry.Name())
		if entry.IsDir() {
			for _, cand := range []string{"SKILL.md", "skill.md", "Skill.md"} {
				skillFile := filepath.Join(entryPath, cand)
				if fileInfo, err := os.Stat(skillFile); err == nil && !fileInfo.IsDir() {
					if item, ok := parseLocalSkillFile(skillFile, entry.Name()); ok {
						skills = append(skills, item)
					}
					break
				}
			}
		} else if strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			stem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			if item, ok := parseLocalSkillFile(entryPath, stem); ok {
				skills = append(skills, item)
			}
		}
	}

	localSkillsCacheMu.Lock()
	localSkillsCache[dirKey] = cachedLocalSkillsDir{
		exists:  true,
		modTime: info.ModTime(),
		skills:  skills,
	}
	localSkillsCacheMu.Unlock()
	return skills
}

func parseLocalSkillFile(filePath string, defaultName string) (discoveredLocalSkill, bool) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		absPath = filePath
	}
	pathKey := strings.ToLower(filepath.ToSlash(filepath.Clean(absPath)))

	contentBytes, err := os.ReadFile(absPath)
	if err != nil || len(bytes.TrimSpace(contentBytes)) == 0 {
		return discoveredLocalSkill{}, false
	}
	content := string(contentBytes)
	fm, body := parseFrontmatter(content)

	name := defaultName
	description := ""
	disableModel := false

	if fm != nil {
		if n, ok := fm["name"].(string); ok && strings.TrimSpace(n) != "" {
			name = strings.TrimSpace(n)
		}
		if d, ok := fm["description"].(string); ok && strings.TrimSpace(d) != "" {
			description = strings.TrimSpace(d)
		}
		if dis, ok := fm["disable_model_invocation"].(bool); ok {
			disableModel = dis
		}
	}

	if disableModel {
		return discoveredLocalSkill{}, false
	}

	if description == "" {
		description = extractFallbackDescription(body, name)
	}

	return discoveredLocalSkill{
		absPath:     absPath,
		pathKey:     pathKey,
		nameKey:     strings.ToLower(name),
		content:     content,
		description: description,
	}, true
}

type discoveredLocalSubagent struct {
	absPath     string
	name        string
	nameKey     string
	description string
	model       string
	prompt      string
}

type cachedLocalSubagentsDir struct {
	exists    bool
	modTime   time.Time
	subagents []discoveredLocalSubagent
}

var (
	localSubagentsCacheMu sync.RWMutex
	localSubagentsCache   = make(map[string]cachedLocalSubagentsDir)
)

// mergeLocalSubagents 自动发现工作区及用户主目录下的 agents/subagents 目录，合并至 requestContext。
func mergeLocalSubagents(requestContext *agentv1.RequestContext) {
	if requestContext == nil {
		return
	}

	knownNames := make(map[string]struct{})
	for _, agent := range requestContext.GetCustomSubagents() {
		if agent == nil {
			continue
		}
		nameKey := strings.ToLower(strings.TrimSpace(agent.GetName()))
		if nameKey != "" {
			knownNames[nameKey] = struct{}{}
		}
	}

	roots := getCandidateRoots(requestContext)
	var agentDirs []string
	for _, root := range roots {
		agentDirs = append(agentDirs,
			filepath.Join(root, ".cursor", "agents"),
			filepath.Join(root, ".cursor", "subagents"),
			filepath.Join(root, ".agents", "subagents"),
			filepath.Join(root, ".claude", "agents"),
		)
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		agentDirs = append(agentDirs,
			filepath.Join(home, ".cursor", "agents"),
			filepath.Join(home, ".cursor", "subagents"),
			filepath.Join(home, ".agents", "subagents"),
			filepath.Join(home, ".cursor-local-assistant-v2", "agents"),
			filepath.Join(home, ".cursor-local-assistant-v2", "subagents"),
		)
	}

	for _, dir := range agentDirs {
		for _, item := range loadLocalSubagentsFromDir(dir) {
			if item.nameKey != "" {
				if _, ok := knownNames[item.nameKey]; ok {
					continue
				}
				knownNames[item.nameKey] = struct{}{}
			}
			requestContext.CustomSubagents = append(requestContext.CustomSubagents, &agentv1.CustomSubagent{
				FullPath:    item.absPath,
				Name:        item.name,
				Description: item.description,
				Model:       item.model,
				Prompt:      item.prompt,
			})
		}
	}
}

func loadLocalSubagentsFromDir(dir string) []discoveredLocalSubagent {
	cleanDir := filepath.Clean(dir)
	dirKey := strings.ToLower(filepath.ToSlash(cleanDir))

	info, statErr := os.Stat(cleanDir)
	exists := statErr == nil && info.IsDir()

	localSubagentsCacheMu.RLock()
	cached, ok := localSubagentsCache[dirKey]
	localSubagentsCacheMu.RUnlock()
	if ok {
		if !exists && !cached.exists {
			return nil
		}
		if exists && cached.exists && cached.modTime.Equal(info.ModTime()) {
			return cached.subagents
		}
	}

	if !exists {
		localSubagentsCacheMu.Lock()
		localSubagentsCache[dirKey] = cachedLocalSubagentsDir{exists: false}
		localSubagentsCacheMu.Unlock()
		return nil
	}

	entries, err := os.ReadDir(cleanDir)
	if err != nil {
		return nil
	}
	subagents := make([]discoveredLocalSubagent, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		filePath := filepath.Join(cleanDir, entry.Name())
		absPath, err := filepath.Abs(filePath)
		if err != nil {
			absPath = filePath
		}
		stem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if item, ok := parseLocalSubagentFile(absPath, stem); ok {
			subagents = append(subagents, item)
		}
	}

	localSubagentsCacheMu.Lock()
	localSubagentsCache[dirKey] = cachedLocalSubagentsDir{
		exists:    true,
		modTime:   info.ModTime(),
		subagents: subagents,
	}
	localSubagentsCacheMu.Unlock()
	return subagents
}

func parseLocalSubagentFile(absPath string, defaultName string) (discoveredLocalSubagent, bool) {
	contentBytes, err := os.ReadFile(absPath)
	if err != nil || len(bytes.TrimSpace(contentBytes)) == 0 {
		return discoveredLocalSubagent{}, false
	}
	content := string(contentBytes)
	fm, body := parseFrontmatter(content)

	name := defaultName
	description := ""
	model := ""

	if fm != nil {
		if n, ok := fm["name"].(string); ok && strings.TrimSpace(n) != "" {
			name = strings.TrimSpace(n)
		}
		if d, ok := fm["description"].(string); ok && strings.TrimSpace(d) != "" {
			description = strings.TrimSpace(d)
		}
		if m, ok := fm["model"].(string); ok {
			model = strings.TrimSpace(m)
		}
	}

	if description == "" {
		description = extractFallbackDescription(body, name)
	}

	return discoveredLocalSubagent{
		absPath:     absPath,
		name:        name,
		nameKey:     strings.ToLower(name),
		description: description,
		model:       model,
		prompt:      body,
	}, true
}

const (
	maxAgentRuleScanDepth = 5
	maxAgentRuleFileSize  = 64 * 1024
	maxAgentRuleFileCount = 30
)

func isIgnoredDirForAgentRuleScan(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".github", ".vscode", ".idea", ".next", ".nuxt", ".output", ".turbo", ".cache",
		"__pycache__", ".venv", "venv", "env", "node_modules", "vendor", "target", "dist",
		"build", "out", "bin", "obj", "coverage", "tmp", "temp", "appdata":
		return true
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	return false
}

func isAgentRuleFileName(name string) bool {
	return strings.EqualFold(name, "AGENTS.md") || strings.EqualFold(name, "AGENT.md")
}

func getWorkspaceRootsForAgentRules(requestContext *agentv1.RequestContext) []string {
	seen := make(map[string]struct{})
	var roots []string

	homeDir, _ := os.UserHomeDir()
	cleanHome := ""
	if homeDir != "" {
		cleanHome = strings.ToLower(filepath.ToSlash(filepath.Clean(homeDir)))
	}

	addRoot := func(p string) {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			return
		}
		clean := filepath.Clean(trimmed)
		key := strings.ToLower(filepath.ToSlash(clean))

		// Tuyệt đối không dùng thư mục home của user làm workspace root cho AGENTS.md
		if cleanHome != "" && key == cleanHome {
			return
		}
		// Bỏ qua thư mục cache nội bộ .cursor/projects của Cursor
		if isCursorInternalProjectFolder(clean) {
			return
		}

		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			roots = append(roots, clean)
		}
	}

	if requestContext != nil {
		if env := requestContext.GetEnv(); env != nil {
			for _, wp := range env.GetWorkspacePaths() {
				addRoot(wp)
			}
			if pf := env.GetProjectFolder(); !isCursorInternalProjectFolder(pf) {
				addRoot(pf)
			}
		}
		if len(roots) == 0 {
			for _, repo := range requestContext.GetGitRepos() {
				if repo != nil {
					addRoot(repo.GetPath())
				}
			}
		}
	}

	if len(roots) == 0 {
		if wd, err := os.Getwd(); err == nil && wd != "" {
			addRoot(wd)
		}
	}

	return roots
}

type discoveredAgentRule struct {
	fullPath string
	relPath  string
	depth    int
	content  string
}

var (
	agentRulesCacheMu sync.RWMutex
	agentRulesCache   = make(map[string]cachedAgentRules)
)

type cachedAgentRules struct {
	cachedAt time.Time
	rules    []discoveredAgentRule
}

// InvalidateAgentRulesCache 清理工作区 agent rules、skills、subagents 与 MCP definition 缓存，确保只在用户发送新消息时重新扫盘。
func InvalidateAgentRulesCache() {
	agentRulesCacheMu.Lock()
	agentRulesCache = make(map[string]cachedAgentRules)
	agentRulesCacheMu.Unlock()

	localSkillsCacheMu.Lock()
	localSkillsCache = make(map[string]cachedLocalSkillsDir)
	localSkillsCacheMu.Unlock()

	localSubagentsCacheMu.Lock()
	localSubagentsCache = make(map[string]cachedLocalSubagentsDir)
	localSubagentsCacheMu.Unlock()

	projectMarkerCacheMu.Lock()
	projectMarkerCache = make(map[string]bool)
	projectMarkerCacheMu.Unlock()

	mcpReadableCacheMu.Lock()
	mcpReadableCache = make(map[string]bool)
	mcpReadableCacheMu.Unlock()
}

// mergeLocalAgentRules 递归发现工作区各层级目录下的 AGENTS.md / AGENT.md 文件，并按层级合并至 requestContext.Rules。
func mergeLocalAgentRules(requestContext *agentv1.RequestContext) {
	if requestContext == nil {
		return
	}

	knownPaths := make(map[string]struct{})
	for _, rule := range requestContext.GetRules() {
		if rule == nil {
			continue
		}
		pathKey := strings.ToLower(filepath.ToSlash(filepath.Clean(rule.GetFullPath())))
		if pathKey != "" {
			knownPaths[pathKey] = struct{}{}
		}
	}

	roots := getWorkspaceRootsForAgentRules(requestContext)
	var discovered []discoveredAgentRule

	for _, root := range roots {
		cleanRoot := filepath.Clean(root)
		rootKey := strings.ToLower(filepath.ToSlash(cleanRoot))

		agentRulesCacheMu.RLock()
		cached, found := agentRulesCache[rootKey]
		agentRulesCacheMu.RUnlock()

		if found {
			for _, item := range cached.rules {
				pathKey := strings.ToLower(filepath.ToSlash(filepath.Clean(item.fullPath)))
				if _, ok := knownPaths[pathKey]; !ok {
					knownPaths[pathKey] = struct{}{}
					discovered = append(discovered, item)
				}
			}
			continue
		}

		info, err := os.Stat(cleanRoot)
		if err != nil || !info.IsDir() {
			continue
		}

		var rootDiscovered []discoveredAgentRule
		_ = filepath.WalkDir(cleanRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}

			if d.IsDir() {
				if cleanRoot == path {
					return nil
				}
				name := d.Name()
				if isIgnoredDirForAgentRuleScan(name) {
					return filepath.SkipDir
				}
				rel, relErr := filepath.Rel(cleanRoot, path)
				if relErr == nil {
					depth := len(strings.Split(filepath.ToSlash(rel), "/"))
					if depth > maxAgentRuleScanDepth {
						return filepath.SkipDir
					}
				}
				return nil
			}

			if len(rootDiscovered) >= maxAgentRuleFileCount {
				return nil
			}

			if !isAgentRuleFileName(d.Name()) {
				return nil
			}

			absPath, err := filepath.Abs(path)
			if err != nil {
				absPath = path
			}
			pathKey := strings.ToLower(filepath.ToSlash(filepath.Clean(absPath)))
			if _, ok := knownPaths[pathKey]; ok {
				return nil
			}

			fileInfo, err := d.Info()
			if err != nil || fileInfo.Size() > maxAgentRuleFileSize {
				return nil
			}

			contentBytes, err := os.ReadFile(absPath)
			if err != nil || len(bytes.TrimSpace(contentBytes)) == 0 {
				return nil
			}

			content := string(contentBytes)
			_, body := parseFrontmatter(content)
			ruleText := strings.TrimSpace(body)
			if ruleText == "" {
				ruleText = strings.TrimSpace(content)
			}
			if ruleText == "" {
				return nil
			}

			relPath, err := filepath.Rel(cleanRoot, absPath)
			if err != nil {
				relPath = d.Name()
			}
			relSlash := filepath.ToSlash(relPath)
			depth := 0
			if relSlash != "." && relSlash != d.Name() {
				parts := strings.Split(relSlash, "/")
				depth = len(parts) - 1
			}

			rootDiscovered = append(rootDiscovered, discoveredAgentRule{
				fullPath: absPath,
				relPath:  relSlash,
				depth:    depth,
				content:  ruleText,
			})
			return nil
		})

		agentRulesCacheMu.Lock()
		agentRulesCache[rootKey] = cachedAgentRules{
			cachedAt: time.Now(),
			rules:    rootDiscovered,
		}
		agentRulesCacheMu.Unlock()

		for _, item := range rootDiscovered {
			pathKey := strings.ToLower(filepath.ToSlash(filepath.Clean(item.fullPath)))
			if _, ok := knownPaths[pathKey]; !ok {
				knownPaths[pathKey] = struct{}{}
				discovered = append(discovered, item)
			}
		}
	}

	if len(discovered) == 0 {
		return
	}

	sort.SliceStable(discovered, func(i, j int) bool {
		if discovered[i].depth != discovered[j].depth {
			return discovered[i].depth < discovered[j].depth
		}
		return discovered[i].relPath < discovered[j].relPath
	})

	for _, item := range discovered {
		var header string
		dirPart := filepath.ToSlash(filepath.Dir(item.relPath))
		if dirPart == "." || dirPart == "" {
			header = fmt.Sprintf("# Project Rule: %s (Root Workspace)", item.relPath)
		} else {
			header = fmt.Sprintf("# Project Rule: %s (Scope: %s/)", item.relPath, dirPart)
		}

		formatted := fmt.Sprintf("%s\n\n%s", header, item.content)
		requestContext.Rules = append(requestContext.Rules, &agentv1.CursorRule{
			FullPath: item.fullPath,
			Content:  formatted,
		})
	}
}

type mcpToolFilePayload struct {
	Description string          `json:"description"`
	Arguments   json.RawMessage `json:"arguments"`
}

var (
	mcpDescCacheMu sync.RWMutex
	mcpDescCache   = make(map[string]cachedMcpDesc)
)

type cachedMcpDesc struct {
	modTime     time.Time
	description string
	inputSchema string
}

const (
	// mcpInlineSchemaBudgetBytes là ngân sách tối đa cho input schema compact inline vào prompt mỗi tool.
	// Vượt ngân sách thì giữ lazy load qua definition_path.
	mcpInlineSchemaBudgetBytes = 1200
	// mcpInlineSchemaSlimBudgetBytes là ngân sách cho bản rút gọn chỉ còn type + required.
	mcpInlineSchemaSlimBudgetBytes = 600
)

// readMCPToolDefinition đọc file definition của MCP tool, trả về description đầy đủ
// và input schema compact (tên param + type + enum + required, không kèm mô tả từng param).
func readMCPToolDefinition(defPath string) (string, string) {
	cleanPath := filepath.Clean(defPath)
	info, err := os.Stat(cleanPath)
	if err != nil || info.IsDir() {
		return "", ""
	}

	mcpDescCacheMu.RLock()
	cached, ok := mcpDescCache[cleanPath]
	mcpDescCacheMu.RUnlock()

	if ok && cached.modTime.Equal(info.ModTime()) {
		return cached.description, cached.inputSchema
	}

	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return "", ""
	}

	var payload mcpToolFilePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", ""
	}

	desc := strings.TrimSpace(payload.Description)
	schema := compactMCPToolSchema(payload.Arguments)
	mcpDescCacheMu.Lock()
	mcpDescCache[cleanPath] = cachedMcpDesc{
		modTime:     info.ModTime(),
		description: desc,
		inputSchema: schema,
	}
	mcpDescCacheMu.Unlock()

	return desc, schema
}

// compactMCPToolSchema rút gọn JSON schema (field "arguments" của definition file) thành bản
// chỉ giữ type/enum/required/properties/items để model biết tham số bắt buộc mà không tốn token.
// Trả về "" khi vượt ngân sách — caller giữ cơ chế lazy load qua definition_path.
func compactMCPToolSchema(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return ""
	}
	if full, err := json.Marshal(compactSchemaNode(doc, true)); err == nil {
		encoded := compactPromptJSON(full)
		if len(encoded) <= mcpInlineSchemaBudgetBytes {
			return encoded
		}
	}
	if slim, err := json.Marshal(compactSchemaNode(doc, false)); err == nil {
		encoded := compactPromptJSON(slim)
		if len(encoded) <= mcpInlineSchemaSlimBudgetBytes {
			return encoded
		}
	}
	return ""
}

// compactSchemaNode giữ lại các khóa cấu trúc cần thiết, bỏ description/default và các metadata dài dòng.
// keepEnums=false sẽ cắt thêm enum để giảm kích thước khi bản đầy đủ vượt ngân sách.
func compactSchemaNode(node map[string]any, keepEnums bool) map[string]any {
	out := make(map[string]any, 4)
	if valueType, ok := node["type"].(string); ok && strings.TrimSpace(valueType) != "" {
		out["type"] = valueType
	}
	if keepEnums {
		if enumValue, ok := node["enum"]; ok {
			out["enum"] = enumValue
		}
	}
	if required, ok := node["required"]; ok {
		out["required"] = required
	}
	if properties, ok := node["properties"].(map[string]any); ok {
		compacted := make(map[string]any, len(properties))
		for name, value := range properties {
			child, ok := value.(map[string]any)
			if !ok {
				continue
			}
			compacted[name] = compactSchemaNode(child, keepEnums)
		}
		out["properties"] = compacted
	}
	if items, ok := node["items"].(map[string]any); ok {
		out["items"] = compactSchemaNode(items, keepEnums)
	}
	return out
}

var (
	mcpReadableCacheMu sync.RWMutex
	mcpReadableCache   = make(map[string]bool)
)

func hasReadableMCPDefinitionFile(defPath string) bool {
	trimmed := strings.TrimSpace(defPath)
	if trimmed == "" {
		return false
	}
	cleanPath := filepath.Clean(trimmed)
	cacheKey := strings.ToLower(filepath.ToSlash(cleanPath))
	mcpReadableCacheMu.RLock()
	cached, ok := mcpReadableCache[cacheKey]
	mcpReadableCacheMu.RUnlock()
	if ok {
		return cached
	}
	info, err := os.Stat(cleanPath)
	readable := err == nil && !info.IsDir()
	mcpReadableCacheMu.Lock()
	mcpReadableCache[cacheKey] = readable
	mcpReadableCacheMu.Unlock()
	return readable
}

// hydrateLocalMCPDescriptors 从 definition_path 文件或 fallback 规则中自动补齐 MCP 工具的摘要 description；
// 当 definition_path 在磁盘上可读时延迟加载 input_schema（由模型按需 Read definition_path），
// 仅在无可读 definition_path 时才内联 compact input schema。
func hydrateLocalMCPDescriptors(requestContext *agentv1.RequestContext) {
	if requestContext == nil {
		return
	}
	var descriptors []*agentv1.McpDescriptor
	if fsOpts := requestContext.GetMcpFileSystemOptions(); fsOpts != nil {
		descriptors = append(descriptors, fsOpts.GetMcpDescriptors()...)
	}
	if metaOpts := requestContext.GetMcpMetaToolOptions(); metaOpts != nil {
		descriptors = append(descriptors, metaOpts.GetMcpDescriptors()...)
	}

	for _, desc := range descriptors {
		if desc == nil {
			continue
		}
		serverID := firstNonEmpty(strings.TrimSpace(desc.GetServerIdentifier()), strings.TrimSpace(desc.GetServerName()))
		folderPath := strings.TrimSpace(desc.GetFolderPath())
		for _, tool := range desc.GetTools() {
			if tool == nil {
				continue
			}
			defPath := strings.TrimSpace(tool.GetDefinitionPath())
			if defPath == "" && folderPath != "" {
				defPath = filepath.Join(folderPath, "tools", tool.GetToolName()+".json")
			}
			readableDef := hasReadableMCPDefinitionFile(defPath)
			if readableDef && strings.TrimSpace(tool.GetDefinitionPath()) == "" {
				tool.DefinitionPath = proto.String(defPath)
			}
			needsDescription := strings.TrimSpace(tool.GetDescription()) == ""
			needsSchema := !readableDef && strings.TrimSpace(tool.GetInputSchemaJson()) == "" && tool.GetInputSchema() == nil
			var descText, schemaText string
			if readableDef && (needsDescription || needsSchema) {
				descText, schemaText = readMCPToolDefinition(defPath)
			}
			if needsDescription {
				if descText != "" {
					tool.Description = proto.String(descText)
				} else if fb := getFallbackMcpToolDescription(serverID, tool.GetToolName()); fb != "" {
					tool.Description = proto.String(fb)
				}
			}
			if needsSchema && schemaText != "" {
				tool.InputSchemaJson = proto.String(schemaText)
			}
		}
	}
}
