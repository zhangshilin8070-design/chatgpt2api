package httpapi

import (
	"strings"

	"chatgpt2api/internal/protocol"
	"chatgpt2api/internal/util"
)

func chatCompletionResultText(result map[string]any) string {
	for _, choice := range util.AsMapSlice(result["choices"]) {
		message := util.StringMap(choice["message"])
		if text := chatCompletionContentText(message["content"]); text != "" {
			return text
		}
	}
	return ""
}

func chatCompletionContentText(content any) string {
	if text, ok := content.(string); ok {
		return strings.TrimSpace(text)
	}
	var parts []string
	for _, item := range anyList(content) {
		block := util.StringMap(item)
		if text := util.Clean(block["text"]); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func collectURLs(v any) []string {
	switch x := v.(type) {
	case map[string]any:
		var urls []string
		for key, value := range x {
			if key == "url" {
				if u := util.Clean(value); u != "" {
					urls = append(urls, u)
				}
			} else if key == "urls" {
				for _, raw := range anyList(value) {
					if u := util.Clean(raw); u != "" {
						urls = append(urls, u)
					}
				}
			} else {
				urls = append(urls, collectURLs(value)...)
			}
		}
		return urls
	case []any:
		var urls []string
		for _, item := range x {
			urls = append(urls, collectURLs(item)...)
		}
		return urls
	case []map[string]any:
		var urls []string
		for _, item := range x {
			urls = append(urls, collectURLs(item)...)
		}
		return urls
	default:
		return nil
	}
}

func protocolBillableUnits(endpoint string, body map[string]any) int {
	switch endpoint {
	case "/v1/images/generations", "/v1/images/edits":
		return normalizedProtocolImageCount(body["n"])
	case "/v1/chat/completions":
		if protocol.IsImageChatRequest(body) {
			return normalizedProtocolImageCount(body["n"])
		}
	case "/v1/responses":
		if protocol.HasResponseImageGenerationTool(body) {
			return normalizedProtocolImageCount(body["n"])
		}
	}
	return 0
}

func normalizedProtocolImageCount(value any) int {
	n := util.ToInt(value, 1)
	if n < 1 {
		return 1
	}
	if n > 4 {
		return 4
	}
	return n
}

func billableProtocolOutputCount(endpoint string, result map[string]any) int {
	if len(result) == 0 {
		return 0
	}
	switch endpoint {
	case "/v1/images/generations", "/v1/images/edits":
		return billableImageDataCount(result["data"])
	case "/v1/chat/completions":
		return countChatCompletionImages(result)
	case "/v1/responses":
		return countResponseOutputImages(result)
	default:
		return billableURLCount(collectURLs(result))
	}
}

func billableProtocolStreamItemCount(endpoint string, item map[string]any) int {
	if len(item) == 0 {
		return 0
	}
	switch endpoint {
	case "/v1/images/generations", "/v1/images/edits":
		if util.Clean(item["object"]) == "image.generation.result" {
			return billableImageDataCount(item["data"])
		}
	case "/v1/chat/completions":
		for _, choice := range util.AsMapSlice(item["choices"]) {
			delta := util.StringMap(choice["delta"])
			if len(delta) == 0 {
				delta = util.StringMap(choice["message"])
			}
			if count := countImagesInChatContent(delta["content"]); count > 0 {
				return count
			}
		}
	case "/v1/responses":
		eventType := util.Clean(item["type"])
		switch eventType {
		case "response.output_item.done", "response.output_item.added":
			if count := countResponseOutputItemImages(util.StringMap(item["item"])); count > 0 {
				return count
			}
		}
	}
	return 0
}

func billableImageDataCount(value any) int {
	count := 0
	for _, item := range util.AsMapSlice(value) {
		if util.Clean(item["url"]) != "" || util.Clean(item["b64_json"]) != "" {
			count++
		}
	}
	return count
}

func countChatCompletionImages(result map[string]any) int {
	count := 0
	for _, choice := range util.AsMapSlice(result["choices"]) {
		message := util.StringMap(choice["message"])
		count += countImagesInChatContent(message["content"])
	}
	return count
}

func countImagesInChatContent(content any) int {
	switch value := content.(type) {
	case string:
		return strings.Count(value, "![")
	case []any:
		count := 0
		for _, raw := range value {
			item := util.StringMap(raw)
			if util.Clean(item["type"]) == "image_url" || util.Clean(item["image_url"]) != "" {
				count++
			}
			if util.Clean(item["type"]) == "text" {
				count += strings.Count(util.Clean(item["text"]), "![")
			}
		}
		return count
	default:
		return 0
	}
}

func countResponseOutputImages(result map[string]any) int {
	count := 0
	for _, item := range util.AsMapSlice(result["output"]) {
		count += countResponseOutputItemImages(item)
	}
	return count
}

func countResponseOutputItemImages(item map[string]any) int {
	if util.Clean(item["type"]) == "image_generation_call" && util.Clean(item["result"]) != "" {
		return 1
	}
	return 0
}

func billableURLCount(urls []string) int {
	return len(dedupe(urls))
}

func dedupe(items []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, item := range items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func anyList(v any) []any {
	if list, ok := v.([]any); ok {
		return list
	}
	if list, ok := v.([]map[string]any); ok {
		out := make([]any, len(list))
		for i, item := range list {
			out[i] = item
		}
		return out
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
