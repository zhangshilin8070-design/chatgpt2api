package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"chatgpt2api/internal/util"
)

func findStringInPayload(value any, names ...string) string {
	wanted := map[string]struct{}{}
	for _, name := range names {
		wanted[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	var walk func(any) string
	walk = func(raw any) string {
		switch x := raw.(type) {
		case map[string]any:
			for key, item := range x {
				if _, ok := wanted[strings.ToLower(strings.TrimSpace(key))]; ok {
					if text := util.Clean(item); text != "" {
						return text
					}
				}
			}
			for _, item := range x {
				if text := walk(item); text != "" {
					return text
				}
			}
		case []any:
			for _, item := range x {
				if text := walk(item); text != "" {
					return text
				}
			}
		}
		return ""
	}
	return walk(value)
}

func findBoolInPayload(value any, names ...string) (bool, bool) {
	wanted := map[string]struct{}{}
	for _, name := range names {
		wanted[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	var walk func(any) (bool, bool)
	walk = func(raw any) (bool, bool) {
		switch x := raw.(type) {
		case map[string]any:
			for key, item := range x {
				if _, ok := wanted[strings.ToLower(strings.TrimSpace(key))]; ok {
					return util.ToBool(item), true
				}
			}
			for _, item := range x {
				if value, ok := walk(item); ok {
					return value, true
				}
			}
		case []any:
			for _, item := range x {
				if value, ok := walk(item); ok {
					return value, true
				}
			}
		}
		return false, false
	}
	return walk(value)
}

func truncateString(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func cleanAccountIDs(ids []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

func cleanTokens(tokens []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

func decodeAccessTokenPayload(accessToken string) map[string]any {
	parts := strings.Split(util.Clean(accessToken), ".")
	if len(parts) < 2 {
		return map[string]any{}
	}
	payload := parts[1]
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		data, err = base64.URLEncoding.DecodeString(payload)
	}
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if json.Unmarshal(data, &out) != nil {
		return map[string]any{}
	}
	return out
}

func chatGPTAccountIDFromPayload(payload map[string]any) string {
	if len(payload) == 0 {
		return ""
	}
	if accountID := util.Clean(payload["chatgpt_account_id"]); accountID != "" {
		return accountID
	}
	if accountID := util.Clean(payload["account_id"]); accountID != "" {
		return accountID
	}
	if authPayload := util.StringMap(payload["https://api.openai.com/auth"]); authPayload != nil {
		if accountID := util.Clean(authPayload["chatgpt_account_id"]); accountID != "" {
			return accountID
		}
	}
	return ""
}

func normalizeAccountType(value any) string {
	switch strings.ToLower(util.Clean(value)) {
	case "free":
		return "Free"
	case "plus", "personal":
		return "Plus"
	case "prolite", "pro_lite":
		return "ProLite"
	case "team", "business", "enterprise":
		return "Team"
	case "pro":
		return "Pro"
	default:
		return ""
	}
}

func searchAccountType(value any) string {
	switch x := value.(type) {
	case map[string]any:
		for key, item := range x {
			keyText := strings.ToLower(util.Clean(key))
			if strings.Contains(keyText, "plan") || strings.Contains(keyText, "type") || strings.Contains(keyText, "subscription") || strings.Contains(keyText, "workspace") || strings.Contains(keyText, "tier") {
				if matched := normalizeAccountType(item); matched != "" {
					return matched
				}
				if matched := searchAccountType(item); matched != "" {
					return matched
				}
			}
		}
	case []any:
		for _, item := range x {
			if matched := searchAccountType(item); matched != "" {
				return matched
			}
		}
	}
	return ""
}

func extractQuotaAndRestoreAt(limits []any) (int, any, bool) {
	for _, raw := range limits {
		item, ok := raw.(map[string]any)
		if !ok || item["feature_name"] != "image_gen" {
			continue
		}
		restore := any(nil)
		if value := util.Clean(item["reset_after"]); value != "" {
			restore = value
		}
		return util.ToInt(item["remaining"], 0), restore, false
	}
	return 0, nil, true
}

func anyList(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	if list, ok := value.([]map[string]any); ok {
		out := make([]any, len(list))
		for i, item := range list {
			out[i] = item
		}
		return out
	}
	return []any{}
}

func mergeMaps(items ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, item := range items {
		for key, value := range item {
			out[key] = value
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func isBootstrapErrorMessage(message string) bool {
	return strings.HasPrefix(strings.TrimSpace(message), "bootstrap failed")
}

func refreshHTTPError(context string, status int, body []byte) error {
	detail := summarizeRefreshErrorBody(body)
	if detail == "" {
		return fmt.Errorf("%s failed: HTTP %d", context, status)
	}
	return fmt.Errorf("%s failed: HTTP %d, %s", context, status, detail)
}

func summarizeRefreshErrorBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return ""
	}
	var payload any
	if json.Unmarshal(body, &payload) == nil {
		if detail := summarizeRefreshErrorValue(payload); detail != "" {
			return detail
		}
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "cf_chl") ||
		strings.Contains(lower, "challenge-platform") ||
		strings.Contains(lower, "enable javascript and cookies to continue") ||
		strings.Contains(lower, "cloudflare") {
		return "upstream returned Cloudflare challenge page; refresh browser fingerprint/session or change proxy"
	}
	if strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype html") || strings.Contains(lower, "<body") {
		return "upstream returned HTML error page"
	}
	const maxBodyDetail = 2048
	if len(text) > maxBodyDetail {
		return "body=" + text[:maxBodyDetail] + "...(truncated)"
	}
	return "body=" + text
}

func summarizeRefreshErrorValue(value any) string {
	switch item := value.(type) {
	case map[string]any:
		for _, key := range []string{"detail", "message", "error_description"} {
			if detail := summarizeRefreshErrorValue(item[key]); detail != "" {
				return detail
			}
		}
		if detail := summarizeRefreshErrorValue(item["error"]); detail != "" {
			return detail
		}
		if data, err := json.Marshal(item); err == nil && len(data) > 0 {
			return "body=" + string(data)
		}
	case []any:
		if len(item) == 0 {
			return ""
		}
		if detail := summarizeRefreshErrorValue(item[0]); detail != "" {
			return detail
		}
	case string:
		text := strings.TrimSpace(item)
		if text != "" {
			return "body=" + text
		}
	}
	return ""
}
