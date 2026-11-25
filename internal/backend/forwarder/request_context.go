package forwarder

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

func normalizeRequestContextForStorage(requestContext *agentv1.RequestContext) *agentv1.RequestContext {
	if requestContext == nil {
		return nil
	}
	return normalizeRequestContextForStorageMode(requestContext, true)
}

func normalizeRequestContextForStorageMode(requestContext *agentv1.RequestContext, includeStatic bool) *agentv1.RequestContext {
	if requestContext == nil {
		return nil
	}
	if !includeStatic {
		return normalizeRealtimeRequestContextForStorage(requestContext)
	}

	cloned, ok := proto.Clone(requestContext).(*agentv1.RequestContext)
	if !ok || cloned == nil {
		return requestContext
	}

	descriptors := collectSkillDescriptors(cloned)
	cloned.Rules = filterNonSkillRules(cloned.GetRules())
	cloned.AgentSkills = nil
	descriptors = guardSkillDescriptors(descriptors)
	if len(descriptors) > 0 {
		cloned.SkillOptions = &agentv1.SkillOptions{SkillDescriptors: descriptors}
	} else {
		cloned.SkillOptions = nil
	}
	guardRequestContextForStorage(cloned)
	return cloned
}

func normalizeRealtimeRequestContextForStorage(requestContext *agentv1.RequestContext) *agentv1.RequestContext {
	if requestContext == nil {
		return nil
	}

	normalized := &agentv1.RequestContext{}
	if env := requestContext.GetEnv(); env != nil {
		if clonedEnv, ok := proto.Clone(env).(*agentv1.RequestContextEnv); ok && clonedEnv != nil {
			normalized.Env = clonedEnv
		}
	}
	if repos := requestContext.GetGitRepos(); len(repos) > 0 {
		normalized.GitRepos = make([]*agentv1.GitRepoInfo, 0, len(repos))
		for _, repo := range repos {
			if repo != nil {
				if clonedRepo, ok := proto.Clone(repo).(*agentv1.GitRepoInfo); ok && clonedRepo != nil {
					normalized.GitRepos = append(normalized.GitRepos, clonedRepo)
				}
			}
		}
	}
	normalized.Rules = guardCursorRules(filterNonSkillRules(requestContext.GetRules()))
	if fileContents := normalizeRealtimeFileContents(requestContext.GetFileContents()); len(fileContents) > 0 {
		normalized.FileContents = fileContents
	}
	if summary := strings.TrimSpace(requestContext.GetUserIntentSummary()); summary != "" {
		normalized.UserIntentSummary = stringPtr(truncatePromptGuardText("request_context.user_intent_summary", summary, promptGuardRealtimeTextChars))
	}
	if hooks := strings.TrimSpace(requestContext.GetHooksAdditionalContext()); hooks != "" {
		normalized.HooksAdditionalContext = stringPtr(truncatePromptGuardText("request_context.hooks_additional_context", hooks, promptGuardRealtimeTextChars))
	}
	if commit := strings.TrimSpace(requestContext.GetCommitAttributionMessage()); commit != "" {
		normalized.CommitAttributionMessage = stringPtr(truncatePromptGuardText("request_context.commit_attribution_message", commit, promptGuardRealtimeTextChars))
	}
	if pr := strings.TrimSpace(requestContext.GetPrAttributionMessage()); pr != "" {
		normalized.PrAttributionMessage = stringPtr(truncatePromptGuardText("request_context.pr_attribution_message", pr, promptGuardRealtimeTextChars))
	}
	// MCP descriptors chỉ được client gửi lên ở turn 1 (static payload). Giữ chúng
	// trong entry realtime để compaction không làm mất <mcp_file_system>/<mcp_meta_tools>
	// khỏi model-visible history; projector dedup nên bản cũ không đổi vẫn an toàn prefix cache.
	if options := requestContext.GetMcpFileSystemOptions(); options != nil {
		if cloned, ok := proto.Clone(options).(*agentv1.McpFileSystemOptions); ok && cloned != nil {
			normalized.McpFileSystemOptions = cloned
		}
	}
	if options := requestContext.GetMcpMetaToolOptions(); options != nil {
		if cloned, ok := proto.Clone(options).(*agentv1.McpMetaToolOptions); ok && cloned != nil {
			normalized.McpMetaToolOptions = cloned
		}
	}
	// Skills, subagents và repository_info cũng chỉ có trong payload turn 1. Nếu entry realtime
	// không mang chúng thì auto-compaction (chỉ preserve entry của turn hiện tại) sẽ cắt
	// <agent_skills>/<subagents> và làm méo <user_info> khỏi model-visible history.
	// Sao chép đúng công thức của static normalization để content trùng khít, projector dedup
	// gộp mọi block static về một bản duy nhất.
	if descriptors := guardSkillDescriptors(collectSkillDescriptors(requestContext)); len(descriptors) > 0 {
		normalized.SkillOptions = &agentv1.SkillOptions{SkillDescriptors: cloneProtoMessages(descriptors)}
	}
	normalized.CustomSubagents = projectRealtimeCustomSubagents(requestContext.GetCustomSubagents())
	normalized.RepositoryInfo = cloneProtoMessages(requestContext.GetRepositoryInfo())
	if !hasRealtimeRequestContextContent(normalized) && normalized.Env == nil && len(normalized.GitRepos) == 0 {
		return nil
	}
	return normalized
}

// cloneProtoMessages sao deepcopy từng message protobuf trong slice; trả về nil khi rỗng để
// protojson không ghi field null.
func cloneProtoMessages[T proto.Message](items []T) []T {
	if len(items) == 0 {
		return nil
	}
	cloned := make([]T, 0, len(items))
	for _, item := range items {
		if isNilProtoMessage(item) {
			continue
		}
		if copyItem, ok := proto.Clone(item).(T); ok {
			cloned = append(cloned, copyItem)
		}
	}
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

// isNilProtoMessage báo true cả với pointer typed-nil vì proto.Clone panic với message nil.
func isNilProtoMessage(message proto.Message) bool {
	if message == nil {
		return true
	}
	value := reflect.ValueOf(message)
	return value.Kind() == reflect.Ptr && value.IsNil()
}

// projectRealtimeCustomSubagents sao subagents vào entry realtime nhưng cắt Prompt/Tools:
// render (<subagents>) và local discovery chỉ cần Name/Description, trong khi system prompt của
// mỗi subagent có thể vài KB — giữ nguyên sẽ nhân bản toàn bộ vào history ở mọi turn.
// Bỏ field không ảnh hưởng nội dung render nên projector dedup vẫn gộp về một block static.
func projectRealtimeCustomSubagents(items []*agentv1.CustomSubagent) []*agentv1.CustomSubagent {
	if len(items) == 0 {
		return nil
	}
	projected := make([]*agentv1.CustomSubagent, 0, len(items))
	for _, item := range items {
		if isNilProtoMessage(item) {
			continue
		}
		cloned, ok := proto.Clone(item).(*agentv1.CustomSubagent)
		if !ok || cloned == nil {
			continue
		}
		cloned.Prompt = ""
		cloned.Tools = nil
		projected = append(projected, cloned)
	}
	if len(projected) == 0 {
		return nil
	}
	return projected
}

func normalizeRealtimeFileContents(fileContents map[string]string) map[string]string {
	if len(fileContents) == 0 {
		return nil
	}
	normalized := make(map[string]string, len(fileContents))
	for path, content := range fileContents {
		trimmedPath := strings.TrimSpace(path)
		if trimmedPath == "" || strings.TrimSpace(content) == "" {
			continue
		}
		normalized[trimmedPath] = content
	}
	if len(normalized) == 0 {
		return nil
	}
	return guardStringMap(normalized, "request_context.file_contents", promptGuardRequestFileChars, promptGuardRequestFilesTotalChars, promptGuardRequestFilesMaxCount)
}

func hasRealtimeRequestContextContent(requestContext *agentv1.RequestContext) bool {
	if requestContext == nil {
		return false
	}
	return len(requestContext.GetRules()) > 0 ||
		len(requestContext.GetSkillOptions().GetSkillDescriptors()) > 0 ||
		len(requestContext.GetCustomSubagents()) > 0 ||
		len(requestContext.GetRepositoryInfo()) > 0 ||
		len(requestContext.GetFileContents()) > 0 ||
		strings.TrimSpace(requestContext.GetUserIntentSummary()) != "" ||
		strings.TrimSpace(requestContext.GetHooksAdditionalContext()) != "" ||
		strings.TrimSpace(requestContext.GetCommitAttributionMessage()) != "" ||
		strings.TrimSpace(requestContext.GetPrAttributionMessage()) != ""
}

func collectSkillDescriptors(requestContext *agentv1.RequestContext) []*agentv1.SkillDescriptor {
	if requestContext == nil {
		return nil
	}

	orderedKeys := make([]string, 0, 16)
	descriptorsByKey := make(map[string]*agentv1.SkillDescriptor)
	addDescriptor := func(descriptor *agentv1.SkillDescriptor) {
		normalized := normalizeSkillDescriptor(descriptor)
		if normalized == nil {
			return
		}
		key := skillDescriptorKey(normalized)
		if key == "" {
			return
		}
		if existing, ok := descriptorsByKey[key]; ok {
			mergeSkillDescriptor(existing, normalized)
			return
		}
		orderedKeys = append(orderedKeys, key)
		descriptorsByKey[key] = normalized
	}

	for _, descriptor := range requestContext.GetSkillOptions().GetSkillDescriptors() {
		addDescriptor(descriptor)
	}
	for _, skill := range requestContext.GetAgentSkills() {
		addDescriptor(skillDescriptorFromAgentSkill(skill))
	}
	for _, rule := range requestContext.GetRules() {
		if isSkillFilePath(rule.GetFullPath()) {
			addDescriptor(skillDescriptorFromCursorRule(rule))
		}
	}

	descriptors := make([]*agentv1.SkillDescriptor, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		if descriptor := descriptorsByKey[key]; descriptor != nil {
			descriptors = append(descriptors, descriptor)
		}
	}
	return descriptors
}

func filterNonSkillRules(rules []*agentv1.CursorRule) []*agentv1.CursorRule {
	if len(rules) == 0 {
		return nil
	}

	filtered := make([]*agentv1.CursorRule, 0, len(rules))
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		if isSkillFilePath(rule.GetFullPath()) {
			continue
		}
		filtered = append(filtered, rule)
	}
	return filtered
}

func skillDescriptorFromAgentSkill(skill *agentv1.AgentSkill) *agentv1.SkillDescriptor {
	if skill == nil {
		return nil
	}
	fullPath := strings.TrimSpace(skill.GetFullPath())
	if fullPath == "" {
		return nil
	}

	parseError := strings.TrimSpace(skill.GetParseError())
	descriptor := &agentv1.SkillDescriptor{
		Name:           inferSkillName(fullPath),
		Description:    strings.TrimSpace(skill.GetDescription()),
		FolderPath:     filepath.Dir(fullPath),
		Enabled:        parseError == "",
		ReadmeFilePath: fullPath,
		PackageType:    inferSkillPackageType(fullPath),
	}
	if parseError != "" {
		descriptor.ParseError = &parseError
	}
	return descriptor
}

func skillDescriptorFromCursorRule(rule *agentv1.CursorRule) *agentv1.SkillDescriptor {
	if rule == nil {
		return nil
	}
	fullPath := strings.TrimSpace(rule.GetFullPath())
	if fullPath == "" || !isSkillFilePath(fullPath) {
		return nil
	}

	description := ""
	if rule.GetType() != nil && rule.GetType().GetAgentFetched() != nil {
		description = strings.TrimSpace(rule.GetType().GetAgentFetched().GetDescription())
	}
	parseError := strings.TrimSpace(rule.GetParseError())
	descriptor := &agentv1.SkillDescriptor{
		Name:           inferSkillName(fullPath),
		Description:    description,
		FolderPath:     filepath.Dir(fullPath),
		Enabled:        parseError == "",
		ReadmeFilePath: fullPath,
		PackageType:    inferSkillPackageType(fullPath),
	}
	if parseError != "" {
		descriptor.ParseError = &parseError
	}
	return descriptor
}

func normalizeSkillDescriptor(descriptor *agentv1.SkillDescriptor) *agentv1.SkillDescriptor {
	if descriptor == nil {
		return nil
	}

	cloned := proto.Clone(descriptor).(*agentv1.SkillDescriptor)
	cloned.Name = strings.TrimSpace(cloned.GetName())
	cloned.Description = strings.TrimSpace(cloned.GetDescription())
	cloned.FolderPath = strings.TrimSpace(cloned.GetFolderPath())
	cloned.ReadmeFilePath = strings.TrimSpace(cloned.GetReadmeFilePath())
	if cloned.ReadmeFilePath == "" && cloned.FolderPath != "" {
		cloned.ReadmeFilePath = filepath.Join(cloned.FolderPath, "SKILL.md")
	}
	if cloned.FolderPath == "" && cloned.ReadmeFilePath != "" {
		cloned.FolderPath = filepath.Dir(cloned.ReadmeFilePath)
	}
	if cloned.Name == "" {
		cloned.Name = inferSkillName(cloned.GetReadmeFilePath())
	}
	if cloned.PackageType == agentv1.PackageType_PACKAGE_TYPE_UNSPECIFIED {
		cloned.PackageType = inferSkillPackageType(firstNonEmpty(cloned.GetReadmeFilePath(), cloned.GetFolderPath()))
	}
	if cloned.GetReadmeFilePath() == "" || cloned.GetDescription() == "" {
		return nil
	}
	return cloned
}

func mergeSkillDescriptor(target *agentv1.SkillDescriptor, candidate *agentv1.SkillDescriptor) {
	if target == nil || candidate == nil {
		return
	}
	if strings.TrimSpace(target.GetDescription()) == "" && strings.TrimSpace(candidate.GetDescription()) != "" {
		target.Description = strings.TrimSpace(candidate.GetDescription())
	}
	if strings.TrimSpace(target.GetReadmeFilePath()) == "" && strings.TrimSpace(candidate.GetReadmeFilePath()) != "" {
		target.ReadmeFilePath = strings.TrimSpace(candidate.GetReadmeFilePath())
	}
	if strings.TrimSpace(target.GetFolderPath()) == "" && strings.TrimSpace(candidate.GetFolderPath()) != "" {
		target.FolderPath = strings.TrimSpace(candidate.GetFolderPath())
	}
	if !target.GetEnabled() && candidate.GetEnabled() {
		target.Enabled = true
	}
	if target.GetPackageType() == agentv1.PackageType_PACKAGE_TYPE_UNSPECIFIED && candidate.GetPackageType() != agentv1.PackageType_PACKAGE_TYPE_UNSPECIFIED {
		target.PackageType = candidate.GetPackageType()
	}
	if strings.TrimSpace(target.GetParseError()) == "" && strings.TrimSpace(candidate.GetParseError()) != "" {
		parseError := strings.TrimSpace(candidate.GetParseError())
		target.ParseError = &parseError
	}
}

func skillDescriptorKey(descriptor *agentv1.SkillDescriptor) string {
	if descriptor == nil {
		return ""
	}
	if readme := strings.TrimSpace(descriptor.GetReadmeFilePath()); readme != "" {
		return readme
	}
	if folder := strings.TrimSpace(descriptor.GetFolderPath()); folder != "" {
		return folder
	}
	return strings.TrimSpace(descriptor.GetName())
}

func buildSkillDiscoveryMessage(requestContext *agentv1.RequestContext) string {
	normalized := normalizeRequestContextForStorageMode(requestContext, true)
	if normalized == nil || normalized.GetSkillOptions() == nil || len(normalized.GetSkillOptions().GetSkillDescriptors()) == 0 {
		return ""
	}

	lines := []string{
		"<agent_skills>",
		"When users ask you to perform tasks, check if any of the available skills below can help complete the task more effectively. Skills provide specialized capabilities and domain knowledge. To use a skill, read the skill file at the provided absolute path using the Read tool, then follow the instructions within. When a skill is relevant, read and follow it IMMEDIATELY as your first action. NEVER just announce or mention a skill without actually reading and following it. Only use skills listed below.",
		"",
		`<available_skills description="Skills the agent can use. Use the Read tool with the provided absolute path to fetch full contents.">`,
	}
	for _, descriptor := range normalized.GetSkillOptions().GetSkillDescriptors() {
		if descriptor == nil {
			continue
		}
		fullPath := strings.TrimSpace(descriptor.GetReadmeFilePath())
		description := strings.TrimSpace(descriptor.GetDescription())
		if fullPath == "" || description == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf(`<agent_skill fullPath="%s">%s</agent_skill>`, escapeSkillPromptXML(fullPath), escapeSkillPromptXML(description)))
	}
	lines = append(lines, "</available_skills>", "</agent_skills>")
	if len(lines) == 6 {
		return ""
	}
	return strings.Join(lines, "\n\n")
}

// carryForwardStaticContext bù các field static (MCP descriptors, skill descriptors, custom
// subagents, repository_info) vào request context sống khi client không gửi lên — chúng chỉ có
// trong payload turn 1. Client chỉ gửi lại đầy đủ ở turn đầu, nên nếu không bù thì entry realtime
// của các turn sau mất dần static context và auto-compaction sẽ cắt nó khỏi model-visible history.
//
// Quét ngược history đã persist để lấy snapshot mới nhất cho từng field; payload rỗng nhưng tồn
// tại được coi là tín hiệu "đã clear" nên không bù. Vòng lặp dừng ngay khi mọi field đã có, còn
// trường hợp conversation chưa từng có skills/subagents thì chi phí bằng một lượt quét các entry
// request_context (cùng cỡ với shouldPersistStaticRequestContext đang chạy mỗi turn).
func carryForwardStaticContext(requestContext *agentv1.RequestContext, entries []HistoryEntry) *agentv1.RequestContext {
	if requestContext == nil || len(entries) == 0 {
		return requestContext
	}
	var cloned *agentv1.RequestContext
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if strings.TrimSpace(entry.Kind) != "request_context" || len(entry.Payload) == 0 {
			continue
		}
		prior := &agentv1.RequestContext{}
		if err := protojson.Unmarshal(entry.Payload, prior); err != nil {
			continue
		}
		if !requestContextCarriesStaticPayload(prior) {
			continue
		}
		if cloned == nil {
			if copyRequestContext, ok := proto.Clone(requestContext).(*agentv1.RequestContext); ok && copyRequestContext != nil {
				cloned = copyRequestContext
			} else {
				cloned = requestContext
			}
		}
		fillMissingStaticFields(cloned, prior)
		if staticContextFilled(cloned) {
			break
		}
	}
	if cloned == nil {
		return requestContext
	}
	return cloned
}

// fillMissingStaticFields chỉ sao chép field mà request context sống còn thiếu, không ghi đè
// dữ liệu client vừa gửi (kể cả payload rỗng = client báo đã clear), và không mutate bản sống.
func fillMissingStaticFields(target, source *agentv1.RequestContext) {
	if target == nil || source == nil {
		return
	}
	if target.GetMcpFileSystemOptions() == nil {
		if options := source.GetMcpFileSystemOptions(); options != nil {
			if cloned, ok := proto.Clone(options).(*agentv1.McpFileSystemOptions); ok && cloned != nil {
				target.McpFileSystemOptions = cloned
			}
		}
	}
	if target.GetMcpMetaToolOptions() == nil {
		if options := source.GetMcpMetaToolOptions(); options != nil {
			if cloned, ok := proto.Clone(options).(*agentv1.McpMetaToolOptions); ok && cloned != nil {
				target.McpMetaToolOptions = cloned
			}
		}
	}
	if len(target.GetSkillOptions().GetSkillDescriptors()) == 0 {
		if descriptors := source.GetSkillOptions().GetSkillDescriptors(); len(descriptors) > 0 {
			target.SkillOptions = &agentv1.SkillOptions{SkillDescriptors: cloneProtoMessages(descriptors)}
		}
	}
	if len(target.GetCustomSubagents()) == 0 {
		target.CustomSubagents = projectRealtimeCustomSubagents(source.GetCustomSubagents())
	}
	if len(target.GetRepositoryInfo()) == 0 {
		target.RepositoryInfo = cloneProtoMessages(source.GetRepositoryInfo())
	}
}

// staticContextFilled báo mọi field static cần bù đã có dữ liệu. MCP được coi là đủ khi client
// dùng một trong hai variant (file system hoặc meta tools).
func staticContextFilled(requestContext *agentv1.RequestContext) bool {
	if requestContext == nil {
		return false
	}
	if requestContext.GetMcpFileSystemOptions() == nil && requestContext.GetMcpMetaToolOptions() == nil {
		return false
	}
	return len(requestContext.GetSkillOptions().GetSkillDescriptors()) > 0 &&
		len(requestContext.GetCustomSubagents()) > 0 &&
		len(requestContext.GetRepositoryInfo()) > 0
}

// shouldPersistStaticRequestContext báo hiệu đây là turn ĐẦU TIÊN model-visible history
// được nạp static context (rules/skills/subagents/MCP descriptors).
//
// Điều kiện cũ là turnSeq == 1, nhưng turn 1 không nhất thiết là turn thật:
//   - prewarm_request cũng đi qua handleRunIntent và chiếm một turnSeq nhưng không mang
//     request_context, nên user message đầu tiên thành turn 2 và bị strip static;
//   - conversation import/fork không có entry request_context nào (chỉ có model_message
//     nhập từ parent, mà parent state không chứa static section).
//
// Vì vậy chỉ cần kiểm tra history đã có entry request_context mang static payload chưa.
func shouldPersistStaticRequestContext(entries []HistoryEntry) bool {
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) != "request_context" || len(entry.Payload) == 0 {
			continue
		}
		prior := &agentv1.RequestContext{}
		if err := protojson.Unmarshal(entry.Payload, prior); err != nil {
			continue
		}
		if requestContextCarriesStaticPayload(prior) {
			return false
		}
	}
	return true
}

// requestContextCarriesStaticPayload nhận diện entry được ghi bằng static normalization:
// realtime normalization không bao giờ sinh ra các field này. Entry chỉ có env/rules/file
// contents được coi là chưa có static; ghi lại lần nữa cũng không đổi nội dung render nên
// projector dedup vẫn giữ nguyên prefix.
func requestContextCarriesStaticPayload(requestContext *agentv1.RequestContext) bool {
	if requestContext == nil {
		return false
	}
	return requestContext.GetSkillOptions() != nil ||
		len(requestContext.GetAgentSkills()) > 0 ||
		len(requestContext.GetCustomSubagents()) > 0 ||
		len(requestContext.GetNonFileRules()) > 0 ||
		len(requestContext.GetTools()) > 0 ||
		len(requestContext.GetMcpInstructions()) > 0 ||
		requestContext.GetCloudRule() != "" ||
		len(requestContext.GetRepositoryInfo()) > 0 ||
		len(requestContext.GetProjectLayouts()) > 0 ||
		requestContext.GetConversationNotesListing() != "" ||
		requestContext.GetSharedNotesListing() != "" ||
		requestContext.GetDebugModeConfig() != nil ||
		requestContext.GetMcpFileSystemOptions() != nil ||
		requestContext.GetMcpMetaToolOptions() != nil
}

func collectMCPToolServers(requestContext *agentv1.RequestContext) map[string]string {
	if requestContext == nil {
		return nil
	}

	servers := make(map[string]string)
	serverAliases := make(map[string]string)
	addDescriptors := func(descriptors []*agentv1.McpDescriptor) {
		for _, descriptor := range descriptors {
			if descriptor == nil {
				continue
			}
			serverIdentifier := strings.TrimSpace(firstNonEmpty(descriptor.GetServerIdentifier(), descriptor.GetServerName()))
			serverName := strings.TrimSpace(descriptor.GetServerName())
			if serverIdentifier == "" {
				continue
			}
			if serverName != "" {
				serverAliases[serverName] = serverIdentifier
			}
			serverAliases[serverIdentifier] = serverIdentifier
			for _, tool := range descriptor.GetTools() {
				if tool == nil {
					continue
				}
				toolName := strings.TrimSpace(tool.GetToolName())
				if toolName == "" {
					continue
				}
				servers[toolName] = serverIdentifier
				if serverName != "" {
					servers[forwarderCanonicalMCPToolLookupName(serverName, toolName)] = serverIdentifier
				}
				servers[forwarderCanonicalMCPToolLookupName(serverIdentifier, toolName)] = serverIdentifier
			}
		}
	}

	addDescriptors(requestContext.GetMcpFileSystemOptions().GetMcpDescriptors())
	addDescriptors(requestContext.GetMcpMetaToolOptions().GetMcpDescriptors())
	for alias, identifier := range serverAliases {
		servers[alias] = identifier
	}
	if len(servers) == 0 {
		return nil
	}
	return servers
}

// collectMCPToolDefinitionPaths ánh xạ tên tool (cả alias canonical server/tool) tới đường dẫn
// file định nghĩa schema trên đĩa, phục vụ heal lỗi tham số MCP bằng schema đầy đủ khi runtime.
func collectMCPToolDefinitionPaths(requestContext *agentv1.RequestContext) map[string]string {
	if requestContext == nil {
		return nil
	}

	paths := make(map[string]string)
	addDescriptors := func(descriptors []*agentv1.McpDescriptor) {
		for _, descriptor := range descriptors {
			if descriptor == nil {
				continue
			}
			serverIdentifier := strings.TrimSpace(firstNonEmpty(descriptor.GetServerIdentifier(), descriptor.GetServerName()))
			serverName := strings.TrimSpace(descriptor.GetServerName())
			folderPath := strings.TrimSpace(descriptor.GetFolderPath())
			for _, tool := range descriptor.GetTools() {
				if tool == nil {
					continue
				}
				toolName := strings.TrimSpace(tool.GetToolName())
				if toolName == "" {
					continue
				}
				definitionPath := strings.TrimSpace(tool.GetDefinitionPath())
				if definitionPath == "" && folderPath != "" {
					definitionPath = filepath.Join(folderPath, "tools", toolName+".json")
				}
				if definitionPath == "" {
					continue
				}
				paths[toolName] = definitionPath
				if serverName != "" {
					paths[forwarderCanonicalMCPToolLookupName(serverName, toolName)] = definitionPath
				}
				if serverIdentifier != "" {
					paths[forwarderCanonicalMCPToolLookupName(serverIdentifier, toolName)] = definitionPath
				}
			}
		}
	}

	addDescriptors(requestContext.GetMcpFileSystemOptions().GetMcpDescriptors())
	addDescriptors(requestContext.GetMcpMetaToolOptions().GetMcpDescriptors())
	if len(paths) == 0 {
		return nil
	}
	return paths
}

func escapeSkillPromptXML(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		`"`, "&quot;",
		"<", "&lt;",
		">", "&gt;",
	)
	return replacer.Replace(strings.TrimSpace(value))
}

func inferSkillName(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if strings.EqualFold(filepath.Base(trimmed), "SKILL.md") {
		return strings.TrimSpace(filepath.Base(filepath.Dir(trimmed)))
	}
	return strings.TrimSpace(strings.TrimSuffix(filepath.Base(trimmed), filepath.Ext(trimmed)))
}

func inferSkillPackageType(path string) agentv1.PackageType {
	normalized := strings.ToLower(strings.TrimSpace(path))
	switch {
	case strings.Contains(normalized, "/.agents/skills/"):
		return agentv1.PackageType_PACKAGE_TYPE_CURSOR_PROJECT
	case strings.Contains(normalized, "/.cursor/skills/"),
		strings.Contains(normalized, "/.cursor/skills-cursor/"),
		strings.Contains(normalized, "/.codex/skills/"):
		return agentv1.PackageType_PACKAGE_TYPE_CURSOR_PERSONAL
	default:
		return agentv1.PackageType_PACKAGE_TYPE_UNSPECIFIED
	}
}

func isSkillFilePath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	return strings.EqualFold(filepath.Base(trimmed), "SKILL.md")
}
