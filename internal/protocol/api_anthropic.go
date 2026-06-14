package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"chatgpt2api/internal/util"
)

func BuildToolPrompt(tools any) string {
	var blocks []string
	for _, raw := range anyList(tools) {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		fn := util.StringMap(tool["function"])
		name := firstNonEmpty(util.Clean(tool["name"]), util.Clean(fn["name"]))
		desc := firstNonEmpty(util.Clean(tool["description"]), util.Clean(fn["description"]))
		schema := firstNonNil(tool["input_schema"], tool["parameters"], fn["input_schema"], fn["parameters"], map[string]any{})
		if name != "" {
			data, _ := json.Marshal(schema)
			blocks = append(blocks, fmt.Sprintf("Tool: %s\nDescription: %s\nParameters: %s", name, desc, string(data)))
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	return "Available tools:\n" + strings.Join(blocks, "\n") + "\n\nTool use rules:\n- If the user asks to list/read/search files, inspect project state, run a command, or answer from local code, you MUST call a suitable tool first. Do not say you cannot access files.\n- To call tools, output ONLY XML and no prose/markdown:\n<tool_calls><tool_call><tool_name>TOOL_NAME</tool_name><parameters><PARAM><![CDATA[value]]></PARAM></parameters></tool_call></tool_calls>\n- Put parameters under <parameters> using the exact schema names."
}

func MergeSystem(system any, extra string) any {
	system = CompactSystem(system)
	if hasClaudeCodeSystem(system) {
		extra = xmlToolRule
	}
	if extra == "" {
		return system
	}
	if text, ok := system.(string); ok && strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text) + "\n\n" + extra
	}
	if list, ok := system.([]any); ok {
		return append(list, map[string]any{"type": "text", "text": extra})
	}
	return extra
}

func CompactSystem(system any) any {
	switch typed := system.(type) {
	case string:
		return compactSystemText(typed)
	case []any:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			if block, ok := item.(map[string]any); ok && util.Clean(block["type"]) == "text" {
				copied := util.CopyMap(block)
				copied["text"] = compactSystemText(util.Clean(block["text"]))
				result = append(result, copied)
				continue
			}
			result = append(result, item)
		}
		return result
	default:
		return system
	}
}

func compactSystemText(text string) string {
	return text
}

func compactMessageText(text string) string {
	return text
}

func hasClaudeCodeSystem(system any) bool {
	switch typed := system.(type) {
	case string:
		return strings.Contains(typed, "You are Claude Code")
	case []any:
		for _, item := range typed {
			block, ok := item.(map[string]any)
			if ok && strings.Contains(util.Clean(block["text"]), "You are Claude Code") {
				return true
			}
		}
	}
	return false
}

func PreprocessMessages(messages any) any {
	list := anyList(messages)
	if list == nil {
		return messages
	}
	var out []any
	for _, raw := range list {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		item := util.CopyMap(message)
		if text, ok := item["content"].(string); ok {
			item["content"] = compactMessageText(text)
		} else if blocks := anyList(item["content"]); blocks != nil {
			processed := make([]any, 0, len(blocks))
			for _, block := range blocks {
				processed = append(processed, preprocessBlock(block))
			}
			item["content"] = processed
		}
		out = append(out, item)
	}
	return out
}

func preprocessBlock(block any) any {
	item, ok := block.(map[string]any)
	if !ok {
		return block
	}
	switch util.Clean(item["type"]) {
	case "text":
		copied := util.CopyMap(item)
		copied["text"] = compactMessageText(util.Clean(item["text"]))
		return copied
	case "tool_use":
		data, _ := json.Marshal(item["input"])
		return map[string]any{"type": "text", "text": fmt.Sprintf("<tool_calls><tool_call><tool_name>%s</tool_name><parameters>%s</parameters></tool_call></tool_calls>", util.Clean(item["name"]), string(data))}
	case "tool_result":
		return map[string]any{"type": "text", "text": fmt.Sprintf("Tool result %s: %s", util.Clean(item["tool_use_id"]), util.Clean(item["content"]))}
	default:
		return block
	}
}

func MessageResponse(model, text string, inputTokens, outputTokens int, tools any) map[string]any {
	content, stopReason := ContentBlocks(text, tools)
	return map[string]any{"id": "msg_" + util.NewUUID(), "type": "message", "role": "assistant", "model": model, "content": content, "stop_reason": stopReason, "stop_sequence": nil, "usage": map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens}}
}

func ContentBlocks(text string, tools any) ([]map[string]any, string) {
	var calls []ToolCall
	if len(anyList(tools)) > 0 {
		calls = ParseToolCalls(text)
	}
	text = StripToolMarkup(text)
	if len(calls) == 0 {
		return []map[string]any{{"type": "text", "text": text}}, "end_turn"
	}
	var content []map[string]any
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	for _, call := range calls {
		content = append(content, map[string]any{"type": "tool_use", "id": "toolu_" + util.NewUUID(), "name": call.Name, "input": call.Input})
	}
	return content, "tool_use"
}

func (e *Engine) StreamAnthropicEvents(ctx context.Context, request MessageRequest) (<-chan map[string]any, <-chan error) {
	chunks, errCh := e.StreamTextChatCompletion(ctx, e.TextBackend(e.Accounts.GetTextAccessToken()), request.Messages, request.Model)
	out := make(chan map[string]any)
	outErr := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(outErr)
		messageID := "msg_" + util.NewUUID()
		current := ""
		streamed := ""
		toolMode := len(anyList(request.Tools)) > 0
		toolStarted := false
		textOpen := false
		out <- map[string]any{"type": "message_start", "message": map[string]any{"id": messageID, "type": "message", "role": "assistant", "model": request.Model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": CountMessageTokens(request.Messages, request.Model), "output_tokens": 0}}}
		if !toolMode {
			textOpen = true
			out <- map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}
		}
		for chunk := range chunks {
			choice := firstChoice(chunk)
			delta := util.StringMap(choice["delta"])
			textDelta := util.Clean(delta["content"])
			if textDelta != "" {
				current += textDelta
				if !toolStarted {
					visible := current
					if toolMode {
						visible = StreamableText(current)
					}
					if strings.HasPrefix(visible, streamed) {
						next := visible[len(streamed):]
						if next != "" {
							if !textOpen {
								textOpen = true
								out <- map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}
							}
							streamed = visible
							out <- map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": next}}
						}
					}
					toolStarted = toolMode && visible != current
				}
			}
			if choice["finish_reason"] != nil {
				content, stopReason := ContentBlocks(current, request.Tools)
				if textOpen {
					out <- map[string]any{"type": "content_block_stop", "index": 0}
				}
				if stopReason == "tool_use" {
					startIndex := 0
					if textOpen {
						startIndex = 1
					}
					outBufferedBlocks(out, content, startIndex)
				}
				out <- map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": CountTextTokens(current, request.Model)}}
				break
			}
		}
		if err := <-errCh; err != nil {
			outErr <- err
			return
		}
		out <- map[string]any{"type": "message_stop", "created": time.Now().Unix()}
		outErr <- nil
	}()
	return out, outErr
}

func outBufferedBlocks(out chan<- map[string]any, content []map[string]any, startIndex int) {
	for offset, block := range content {
		index := startIndex + offset
		if block["type"] == "tool_use" {
			data, _ := json.Marshal(block["input"])
			out <- map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "tool_use", "id": block["id"], "name": block["name"], "input": map[string]any{}}}
			out <- map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(data)}}
		} else {
			out <- map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "text", "text": ""}}
			out <- map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "text_delta", "text": block["text"]}}
		}
		out <- map[string]any{"type": "content_block_stop", "index": index}
	}
}

func CollectChatContent(chunks <-chan map[string]any) string {
	var parts []string
	for chunk := range chunks {
		choice := firstChoice(chunk)
		delta := util.StringMap(choice["delta"])
		if content := util.Clean(delta["content"]); content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "")
}

func firstChoice(chunk map[string]any) map[string]any {
	choices := anyList(chunk["choices"])
	if len(choices) == 0 {
		return map[string]any{}
	}
	if choice, ok := choices[0].(map[string]any); ok {
		return choice
	}
	return map[string]any{}
}

type ToolCall struct {
	Name  string
	Input map[string]any
}

func StripToolMarkup(text string) string {
	return strings.TrimSpace(regexp.MustCompile(`(?is)<tool_calls\b[^>]*>.*?</tool_calls>|<tool_call\b[^>]*>.*?</tool_call>|<function_call\b[^>]*>.*?</function_call>|<invoke\b[^>]*>.*?</invoke>`).ReplaceAllString(text, ""))
}

func StreamableText(text string) string {
	loc := regexp.MustCompile(`(?is)<tool_calls\b|<tool_call\b|<function_call\b|<invoke\b`).FindStringIndex(text)
	if loc == nil {
		return text
	}
	return strings.TrimRight(text[:loc[0]], " \t\r\n")
}

func ParseToolCalls(text string) []ToolCall {
	text = regexp.MustCompile("(?is)```.*?```").ReplaceAllString(text, "")
	matches := regexp.MustCompile(`(?is)<tool_call\b[^>]*>(.*?)</tool_call>|<function_call\b[^>]*>(.*?)</function_call>|<invoke\b[^>]*>(.*?)</invoke>`).FindAllStringSubmatch(text, -1)
	var out []ToolCall
	for _, match := range matches {
		block := ""
		for _, part := range match[1:] {
			if part != "" {
				block = part
				break
			}
		}
		name := firstNonEmpty(XMLValue(block, "tool_name"), XMLValue(block, "name"), XMLValue(block, "function"))
		params := firstNonEmpty(XMLValue(block, "parameters"), XMLValue(block, "input"), XMLValue(block, "arguments"), "{}")
		if name != "" {
			out = append(out, ToolCall{Name: name, Input: ParseToolParams(params)})
		}
	}
	return out
}

func XMLValue(text, tag string) string {
	re := regexp.MustCompile(`(?is)<` + regexp.QuoteMeta(tag) + `\b[^>]*>(.*?)</` + regexp.QuoteMeta(tag) + `>`)
	match := re.FindStringSubmatch(text)
	if len(match) < 2 {
		return ""
	}
	value := strings.TrimSpace(match[1])
	if cdata := regexp.MustCompile(`(?is)^<!\[CDATA\[(.*?)]]>$`).FindStringSubmatch(value); len(cdata) > 1 {
		value = cdata[1]
	}
	return strings.TrimSpace(html.UnescapeString(value))
}

func ParseToolParams(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	var parsed map[string]any
	if json.Unmarshal([]byte(raw), &parsed) == nil {
		return parsed
	}
	out := map[string]any{}
	for _, match := range regexp.MustCompile(`(?is)<([\w.-]+)\b[^>]*>(.*?)</([\w.-]+)>`).FindAllStringSubmatch(raw, -1) {
		if len(match) > 3 && match[1] == match[3] {
			out[match[1]] = ParseToolValue(match[2])
		}
	}
	return out
}

func ParseToolValue(raw string) any {
	value := XMLValue("<x>"+raw+"</x>", "x")
	var parsed any
	if json.Unmarshal([]byte(value), &parsed) == nil {
		return parsed
	}
	return value
}
