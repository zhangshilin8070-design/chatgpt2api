package protocol

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"

	"chatgpt2api/internal/service"
	"chatgpt2api/internal/util"
)

func IsTokenInvalidError(message string) bool {
	return service.IsAccountInvalidErrorMessage(message)
}

func MessageText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			switch x := item.(type) {
			case string:
				parts = append(parts, x)
			case map[string]any:
				t := util.Clean(x["type"])
				if t == "text" || t == "input_text" || t == "output_text" {
					parts = append(parts, util.Clean(x["text"]))
				}
			}
		}
		return strings.Join(parts, "")
	default:
		return ""
	}
}

func NormalizeMessages(messages any, system any) []map[string]any {
	var normalized []map[string]any
	if text := MessageText(system); text != "" {
		normalized = append(normalized, map[string]any{"role": "system", "content": text})
	}
	if list, ok := messages.([]map[string]any); ok {
		for _, message := range list {
			normalized = append(normalized, map[string]any{"role": firstNonEmpty(util.Clean(message["role"]), "user"), "content": MessageText(message["content"])})
		}
		return normalized
	}
	if list, ok := messages.([]any); ok {
		for _, raw := range list {
			if message, ok := raw.(map[string]any); ok {
				normalized = append(normalized, map[string]any{"role": firstNonEmpty(util.Clean(message["role"]), "user"), "content": MessageText(message["content"])})
			}
		}
	}
	return normalized
}

func AssistantHistoryText(messages []map[string]any) string {
	var parts []string
	for _, item := range messages {
		if item["role"] == "assistant" {
			parts = append(parts, util.Clean(item["content"]))
		}
	}
	return strings.Join(parts, "")
}

func AssistantHistoryMessages(messages []map[string]any) []string {
	var out []string
	for _, item := range messages {
		if item["role"] == "assistant" && util.Clean(item["content"]) != "" {
			out = append(out, util.Clean(item["content"]))
		}
	}
	return out
}

// NormalizeImageGenerationSize 把请求里的 size 字段做轻量归一化：仅对分档
// 预设（1080p / 2k / 4k）做大小写归一化以便后续识别；像素 / 比例形态保留
// 原始字符串，由各通路 backend 自行做白名单收敛。
//
// 分档预设不会被翻成具体像素：
//   - ChatGPT /backend-api/codex/responses 链路上 tool.size 只接受
//     1024x1024 / 1536x1024 / 1024x1536 / auto，超出会被上游打回 5xx；
//   - OpenAI Images API 同样只接受上述三档；
//   - newapi 等聚合 chat-completions 上游忽略 size 字段，唯一可控的是 prompt
//     里的比例 / 分档提示。
//
// 分档信息仅通过 BuildImagePrompt 的 tier 提示词向上游传达。
func NormalizeImageGenerationSize(size string) string {
	trimmed := strings.TrimSpace(size)
	switch strings.ToLower(trimmed) {
	case "1080p", "2k", "4k":
		return strings.ToLower(trimmed)
	}
	return trimmed
}

func imageSizeDimensions(size string) (int, int, bool) {
	matches := regexp.MustCompile(`^(\d+)x(\d+)$`).FindStringSubmatch(strings.ToLower(strings.TrimSpace(size)))
	if len(matches) != 3 {
		return 0, 0, false
	}
	width := util.ToInt(matches[1], 0)
	height := util.ToInt(matches[2], 0)
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

func BuildImagePrompt(prompt, size, quality string) string {
	prompt = strings.TrimSpace(prompt)
	rawTier := strings.ToLower(strings.TrimSpace(size))
	size = NormalizeImageGenerationSize(size)
	if strings.EqualFold(size, "auto") {
		size = ""
	}
	var hintsList []string
	hints := map[string]string{
		"1:1":  "输出为 1:1 正方形构图，主体居中，适合正方形画幅。",
		"3:2":  "输出为 3:2 横版构图，适合摄影、产品展示和横向叙事画幅。",
		"2:3":  "输出为 2:3 竖版构图，适合海报、人物和纵向叙事画幅。",
		"16:9": "输出为 16:9 横屏构图，适合宽画幅展示。",
		"21:9": "输出为 21:9 超宽横版构图，适合电影感全景和宽银幕画幅。",
		"9:16": "输出为 9:16 竖屏构图，适合竖版画幅展示。",
		"4:3":  "输出为 4:3 比例，兼顾宽度与高度，适合展示画面细节。",
		"3:4":  "输出为 3:4 比例，纵向构图，适合人物肖像或竖向场景。",
	}
	tierHints := map[string]string{
		"1080p": "目标画质档为 1080P，整体清晰度按 1080P 级别构图，实际像素以上游返回为准。",
		"2k":    "目标画质档为 2K，整体清晰度按 2K 级别构图，实际像素以上游返回为准。",
		"4k":    "目标画质档为 4K，整体清晰度按 4K 级别构图，实际像素以上游返回为准。",
	}
	if hint, ok := tierHints[rawTier]; ok {
		hintsList = append(hintsList, hint)
	} else if size != "" {
		if width, height, ok := imageSizeDimensions(size); ok {
			hintsList = append(hintsList, fmt.Sprintf("以 %d x %d 像素对应的宽高比作为构图偏好，实际像素以上游返回为准。", width, height))
		} else if hint, ok := hints[size]; ok {
			hintsList = append(hintsList, hint)
		} else {
			hintsList = append(hintsList, "输出图片，目标尺寸或宽高比为 "+size+"。")
		}
	}
	qualityHints := map[string]string{
		"low":    "画质使用 Low 档，优先更快出图，细节可以适度简化。",
		"medium": "画质使用 Medium 档，在速度、细节和整体完成度之间保持平衡。",
		"high":   "画质使用 High 档，提升细节、纹理、光影和整体完成度。",
	}
	if hint, ok := qualityHints[strings.ToLower(strings.TrimSpace(quality))]; ok {
		hintsList = append(hintsList, hint)
	}
	if len(hintsList) == 0 {
		return prompt
	}
	return prompt + "\n\n" + strings.Join(hintsList, "\n")
}

func buildResponsesImagePrompt(prompt, size, model string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	if strings.TrimSpace(model) == util.ImageModelCodexGPTImage2 {
		return prompt
	}
	return BuildImagePrompt(prompt, size, "")
}

func CountMessageTokens(messages []map[string]any, model string) int {
	total := 3
	for _, message := range messages {
		total += 3
		for key, value := range message {
			if text, ok := value.(string); ok {
				total += CountTextTokens(text, model)
				if key == "name" {
					total++
				}
			}
		}
	}
	return total
}

func CountTextTokens(text, model string) int {
	runes := []rune(text)
	if len(runes) == 0 {
		return 0
	}
	return (len(runes) + 3) / 4
}

func EncodeImages(images []UploadedImage) []string {
	out := make([]string, 0, len(images))
	for _, image := range images {
		if len(image.Data) > 0 {
			out = append(out, base64.StdEncoding.EncodeToString(image.Data))
		}
	}
	return out
}

type UploadedImage struct {
	Data        []byte
	Filename    string
	ContentType string
}

func AssistantText(event map[string]any, currentText, historyText string) string {
	for _, candidate := range []any{event, event["v"]} {
		m := util.StringMap(candidate)
		message := util.StringMap(m["message"])
		if len(message) == 0 {
			continue
		}
		author := util.StringMap(message["author"])
		if strings.ToLower(util.Clean(author["role"])) != "assistant" {
			continue
		}
		text := AssistantMessageText(message)
		if text != "" {
			return StripHistory(text, historyText)
		}
	}
	return ApplyTextPatch(event, currentText, historyText)
}

func EventAssistantText(event map[string]any, historyText string) string {
	for _, candidate := range []any{event, event["v"]} {
		m := util.StringMap(candidate)
		message := util.StringMap(m["message"])
		author := util.StringMap(message["author"])
		if author["role"] == "assistant" {
			return StripHistory(AssistantMessageText(message), historyText)
		}
	}
	return ""
}

func AssistantMessageText(message map[string]any) string {
	content := util.StringMap(message["content"])
	parts, _ := content["parts"].([]any)
	var out []string
	for _, part := range parts {
		if text, ok := part.(string); ok {
			out = append(out, text)
		}
	}
	return strings.Join(out, "")
}

func StripHistory(text, historyText string) string {
	for historyText != "" && strings.HasPrefix(text, historyText) {
		text = text[len(historyText):]
	}
	return text
}

func ApplyTextPatch(event map[string]any, currentText, historyText string) string {
	if event["p"] == "/message/content/parts/0" {
		return ApplyPatchOp(event, currentText, historyText)
	}
	if value, ok := event["v"].(string); ok && currentText != "" && event["p"] == nil && event["o"] == nil {
		return currentText + value
	}
	if event["o"] == "patch" {
		text := currentText
		for _, raw := range anyList(event["v"]) {
			if op, ok := raw.(map[string]any); ok {
				text = ApplyTextPatch(op, text, historyText)
			}
		}
		return text
	}
	text := currentText
	for _, raw := range anyList(event["v"]) {
		if op, ok := raw.(map[string]any); ok {
			text = ApplyTextPatch(op, text, historyText)
		}
	}
	return text
}

func ApplyPatchOp(operation map[string]any, currentText, historyText string) string {
	value := util.Clean(operation["v"])
	switch operation["o"] {
	case "append":
		return currentText + value
	case "replace":
		return StripHistory(value, historyText)
	default:
		return currentText
	}
}

func UpdateConversationState(state *ConversationState, payload string, event map[string]any) {
	conversationID, fileIDs, sedimentIDs := ExtractConversationIDs(payload)
	if conversationID != "" && state.ConversationID == "" {
		state.ConversationID = conversationID
	}
	if event != nil && IsImageToolEvent(event) {
		state.FileIDs = appendUnique(state.FileIDs, fileIDs...)
		state.SedimentIDs = appendUnique(state.SedimentIDs, sedimentIDs...)
	}
	if event == nil {
		return
	}
	if id := util.Clean(event["conversation_id"]); id != "" {
		state.ConversationID = id
	}
	value := util.StringMap(event["v"])
	if id := util.Clean(value["conversation_id"]); id != "" {
		state.ConversationID = id
	}
	if event["type"] == "moderation" {
		moderation := util.StringMap(event["moderation_response"])
		if moderation["blocked"] == true {
			state.Blocked = true
		}
	}
	if event["type"] == "server_ste_metadata" {
		metadata := util.StringMap(event["metadata"])
		if toolInvoked, ok := metadata["tool_invoked"].(bool); ok {
			state.ToolInvoked = &toolInvoked
		}
		if value := util.Clean(metadata["turn_use_case"]); value != "" {
			state.TurnUseCase = value
		}
	}
}

func ExtractConversationIDs(payload string) (string, []string, []string) {
	conversation := ""
	if match := regexp.MustCompile(`"conversation_id"\s*:\s*"([^"]+)"`).FindStringSubmatch(payload); len(match) > 1 {
		conversation = match[1]
	}
	fileIDs := regexp.MustCompile(`(file[-_][A-Za-z0-9]+)`).FindAllString(payload, -1)
	sedimentMatches := regexp.MustCompile(`sediment://([A-Za-z0-9_-]+)`).FindAllStringSubmatch(payload, -1)
	var sediments []string
	for _, match := range sedimentMatches {
		if len(match) > 1 {
			sediments = append(sediments, match[1])
		}
	}
	return conversation, fileIDs, sediments
}

func IsImageToolEvent(event map[string]any) bool {
	value := util.StringMap(event["v"])
	message := util.StringMap(event["message"])
	if len(message) == 0 {
		message = util.StringMap(value["message"])
	}
	metadata := util.StringMap(message["metadata"])
	author := util.StringMap(message["author"])
	return author["role"] == "tool" && metadata["async_task_type"] == "image_gen"
}

func conversationBaseEvent(eventType string, state *ConversationState) ConversationEvent {
	var tool any
	if state.ToolInvoked != nil {
		tool = *state.ToolInvoked
	}
	return ConversationEvent{
		"type":            eventType,
		"text":            state.Text,
		"conversation_id": state.ConversationID,
		"file_ids":        state.FileIDs,
		"sediment_ids":    state.SedimentIDs,
		"blocked":         state.Blocked,
		"tool_invoked":    tool,
		"turn_use_case":   state.TurnUseCase,
	}
}

func anyList(v any) []any {
	switch list := v.(type) {
	case []any:
		return list
	case []map[string]any:
		out := make([]any, 0, len(list))
		for _, item := range list {
			out = append(out, item)
		}
		return out
	}
	return nil
}

func appendUnique(base []string, values ...string) []string {
	seen := map[string]struct{}{}
	for _, item := range base {
		seen[item] = struct{}{}
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		base = append(base, value)
	}
	return base
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// mergeImageSizeWithResolution 把"比例 + 分辨率档"合并为最终下发上游的 size。
//
// 规则（按 IMAGE-API.local.md §6 制定）：
//   - size 已经是显式像素（WxH）或档位（1k/2k/4k）→ 原样保留，忽略 resolution；
//   - resolution 为空 / "auto" → 原样保留 size；
//   - resolution ∈ {1080p / 2k / 4k} 且 size 为比例（"a:b"，例如"9:16"、
//     "16:9"、"1:1"）→ 按比例 + 档位合成具体像素：
//       1080p → 短边 1080；2k → 长边 2048；4k → 长边 3840；
//   - 其它情况 → 返回原 size。
//
// 这样前端"9:16 + 4K"会下发 size="2160x3840"，Axiom 网关命中 4k 计价档同时
// 上游模型识别 9:16 比例；只填"4K"无比例时下发 size="4k"（计价档字面值）。
func mergeImageSizeWithResolution(size, resolution string) string {
	size = strings.TrimSpace(size)
	res := strings.ToLower(strings.TrimSpace(resolution))
	if res == "" || res == "auto" {
		return size
	}
	// 仅比例形态参与合成；像素 / 档位字面值原样保留。
	matches := regexp.MustCompile(`^(\d+(?:\.\d+)?):(\d+(?:\.\d+)?)$`).FindStringSubmatch(size)
	if len(matches) != 3 {
		// 没有有效比例：如果 size 也为空，直接用档位字面值下发；否则保留 size。
		if size == "" {
			switch res {
			case "1080p", "2k", "4k":
				return res
			}
		}
		return size
	}
	ratioW := util.ToInt(matches[1], 0)
	ratioH := util.ToInt(matches[2], 0)
	if ratioW <= 0 || ratioH <= 0 {
		return size
	}
	var refLong int
	var refShort int
	switch res {
	case "1080p":
		refShort = 1080
	case "2k":
		refLong = 2048
	case "4k":
		refLong = 3840
	default:
		return size
	}
	if refLong > 0 {
		// 4k / 2k：按长边对齐。
		if ratioW >= ratioH {
			w := refLong
			h := (refLong * ratioH) / ratioW
			return fmt.Sprintf("%dx%d", w, h)
		}
		w := (refLong * ratioW) / ratioH
		h := refLong
		return fmt.Sprintf("%dx%d", w, h)
	}
	// 1080p：按短边对齐到 1080。
	if ratioW <= ratioH {
		w := refShort
		h := (refShort * ratioH) / ratioW
		return fmt.Sprintf("%dx%d", w, h)
	}
	w := (refShort * ratioW) / ratioH
	h := refShort
	return fmt.Sprintf("%dx%d", w, h)
}
