package service

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"chatgpt2api/internal/util"
)

// codexImportClockSkewSeconds tolerates small clock drift when checking
// access_token expiry. Mirrors sub2api's value to keep behavior comparable.
const codexImportClockSkewSeconds int64 = 120

// CodexImportPayload is the top-level shape of a sub2api-compatible export
// file. We only consume `accounts`; other top-level fields are ignored.
type CodexImportPayload struct {
	Accounts []CodexImportAccount `json:"accounts"`
}

// CodexImportAccount mirrors the relevant subset of sub2api's exported
// account record. Fields not used here are intentionally omitted.
type CodexImportAccount struct {
	Name        string         `json:"name"`
	Platform    string         `json:"platform"`
	Type        string         `json:"type"`
	Credentials map[string]any `json:"credentials"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// CodexImportItemResult records the outcome for a single account record.
type CodexImportItemResult struct {
	Index   int    `json:"index"`
	Name    string `json:"name,omitempty"`
	Action  string `json:"action"` // created / updated / skipped / failed
	Email   string `json:"email,omitempty"`
	Type    string `json:"type,omitempty"`
	Message string `json:"message,omitempty"`
}

// CodexImportResult is the response payload returned by the import endpoint.
type CodexImportResult struct {
	Total   int                     `json:"total"`
	Created int                     `json:"created"`
	Updated int                     `json:"updated"`
	Skipped int                     `json:"skipped"`
	Failed  int                     `json:"failed"`
	Items   []CodexImportItemResult `json:"items,omitempty"`
}

// jwtClaims captures the OpenAI-specific fields embedded in OAuth tokens
// minted by chatgpt.com. We only decode (do not verify) the payload here;
// the upstream API does the real verification.
type jwtClaims struct {
	Email      string                  `json:"email"`
	Exp        int64                   `json:"exp"`
	Sub        string                  `json:"sub"`
	OpenAIAuth *jwtClaimsOpenAIAuthCtx `json:"https://api.openai.com/auth"`
}

type jwtClaimsOpenAIAuthCtx struct {
	ChatGPTAccountID string `json:"chatgpt_account_id"`
	ChatGPTUserID    string `json:"chatgpt_user_id"`
	ChatGPTPlanType  string `json:"chatgpt_plan_type"`
	UserID           string `json:"user_id"`
	POID             string `json:"poid"`
}

// ParseCodexImportPayload accepts either the full sub2api export JSON object
// or just the `accounts` array. It rejects empty input and surfaces JSON
// errors verbatim to the caller.
func ParseCodexImportPayload(content string) (*CodexImportPayload, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil, errors.New("empty import content")
	}
	// Accept both a wrapped object {"accounts":[...]} and a bare array.
	if strings.HasPrefix(trimmed, "[") {
		var accounts []CodexImportAccount
		if err := json.Unmarshal([]byte(trimmed), &accounts); err != nil {
			return nil, fmt.Errorf("decode accounts array: %w", err)
		}
		return &CodexImportPayload{Accounts: accounts}, nil
	}
	var payload CodexImportPayload
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	return &payload, nil
}

// ImportCodexAccounts consumes a parsed sub2api payload, normalizes each
// OAuth ChatGPT account, and persists the survivors via AddAccounts +
// UpdateAccount. Non-OAuth or non-OpenAI entries are reported as skipped so
// the operator can see why they did not land in the pool.
func (s *AccountService) ImportCodexAccounts(payload *CodexImportPayload) *CodexImportResult {
	result := &CodexImportResult{
		Items: []CodexImportItemResult{},
	}
	if payload == nil {
		return result
	}
	result.Total = len(payload.Accounts)
	if result.Total == 0 {
		return result
	}

	// Snapshot existing tokens once so we can decide created vs updated
	// without holding the account-service mutex across the whole loop.
	existing := map[string]struct{}{}
	for _, token := range s.ListTokens() {
		existing[token] = struct{}{}
	}

	for index, raw := range payload.Accounts {
		entry := CodexImportItemResult{Index: index + 1, Name: raw.Name}

		platform := strings.ToLower(strings.TrimSpace(raw.Platform))
		accountType := strings.ToLower(strings.TrimSpace(raw.Type))
		if platform != "" && platform != "openai" {
			entry.Action = "skipped"
			entry.Message = fmt.Sprintf("不支持的 platform=%s", raw.Platform)
			result.Skipped++
			result.Items = append(result.Items, entry)
			continue
		}
		if accountType != "oauth" {
			entry.Action = "skipped"
			entry.Message = fmt.Sprintf("仅支持 type=oauth 的账号，跳过 type=%s", raw.Type)
			result.Skipped++
			result.Items = append(result.Items, entry)
			continue
		}

		updates, err := normalizeCodexImportEntry(raw)
		if err != nil {
			entry.Action = "failed"
			entry.Message = err.Error()
			result.Failed++
			result.Items = append(result.Items, entry)
			continue
		}

		token := util.Clean(updates["access_token"])
		entry.Email = util.Clean(updates["email"])
		entry.Type = util.Clean(updates["type"])

		if _, alreadyHave := existing[token]; alreadyHave {
			if updated := s.UpdateAccount(token, updates); updated != nil {
				entry.Action = "updated"
				result.Updated++
			} else {
				entry.Action = "failed"
				entry.Message = "账号更新失败"
				result.Failed++
			}
			result.Items = append(result.Items, entry)
			continue
		}

		// Brand-new token: persist via AddAccounts to share the dedup +
		// log path, then immediately enrich with OAuth metadata.
		add := s.AddAccounts([]string{token})
		if util.ToInt(add["added"], 0) <= 0 && util.ToInt(add["skipped"], 0) <= 0 {
			entry.Action = "failed"
			entry.Message = "账号写入失败"
			result.Failed++
			result.Items = append(result.Items, entry)
			continue
		}
		if updated := s.UpdateAccount(token, updates); updated == nil {
			entry.Action = "failed"
			entry.Message = "账号写入后元数据更新失败"
			result.Failed++
			result.Items = append(result.Items, entry)
			continue
		}
		entry.Action = "created"
		result.Created++
		result.Items = append(result.Items, entry)
		existing[token] = struct{}{}
	}
	return result
}

// normalizeCodexImportEntry validates a single OAuth record and converts it
// into the field map UpdateAccount expects. The returned map always
// contains `access_token`; callers should treat it as immutable input.
func normalizeCodexImportEntry(raw CodexImportAccount) (map[string]any, error) {
	creds := raw.Credentials
	if creds == nil {
		return nil, errors.New("credentials is empty")
	}
	accessToken := strings.TrimSpace(util.Clean(creds["access_token"]))
	if accessToken == "" {
		return nil, errors.New("missing credentials.access_token")
	}

	updates := map[string]any{
		"access_token": accessToken,
		"auth_method":  "oauth",
	}

	// Refresh / id token / oauth metadata are stored verbatim; the codex
	// upstream re-uses them when refreshing or upgrading the access_token
	// later. We never overwrite an existing refresh_token with an empty
	// value because that would silently break account auto-renewal.
	if v := strings.TrimSpace(util.Clean(creds["refresh_token"])); v != "" {
		updates["refresh_token"] = v
	}
	if v := strings.TrimSpace(util.Clean(creds["id_token"])); v != "" {
		updates["id_token"] = v
	}
	if v := strings.TrimSpace(util.Clean(creds["client_id"])); v != "" {
		updates["oauth_client_id"] = v
	}
	if v := util.ToInt(creds["expires_at"], 0); v > 0 {
		updates["oauth_expires_at"] = v
	}
	if v := strings.TrimSpace(util.Clean(creds["chatgpt_account_id"])); v != "" {
		updates["chatgpt_account_id"] = v
	}
	if v := strings.TrimSpace(util.Clean(creds["chatgpt_user_id"])); v != "" {
		updates["user_id"] = v
	}
	if v := strings.TrimSpace(util.Clean(creds["organization_id"])); v != "" {
		updates["organization_id"] = v
	}
	if v := strings.TrimSpace(util.Clean(creds["email"])); v != "" {
		updates["email"] = v
	}

	planType := strings.TrimSpace(util.Clean(creds["plan_type"]))

	// JWT decoding is best-effort: if the token does not parse we still
	// import the account, but we use any extracted fields to fill gaps
	// (account id, plan_type, expiry guard).
	if claims, err := decodeJWTClaims(accessToken); err == nil {
		if claims.OpenAIAuth != nil {
			if updates["chatgpt_account_id"] == nil && claims.OpenAIAuth.ChatGPTAccountID != "" {
				updates["chatgpt_account_id"] = claims.OpenAIAuth.ChatGPTAccountID
			}
			if updates["user_id"] == nil && claims.OpenAIAuth.ChatGPTUserID != "" {
				updates["user_id"] = claims.OpenAIAuth.ChatGPTUserID
			}
			if updates["organization_id"] == nil && claims.OpenAIAuth.POID != "" {
				updates["organization_id"] = claims.OpenAIAuth.POID
			}
			if planType == "" {
				planType = claims.OpenAIAuth.ChatGPTPlanType
			}
		}
		if updates["email"] == nil && claims.Email != "" {
			updates["email"] = claims.Email
		}
		if claims.Exp > 0 {
			now := time.Now().Unix()
			if claims.Exp+codexImportClockSkewSeconds < now {
				return nil, fmt.Errorf("access_token 已过期: %s", time.Unix(claims.Exp, 0).UTC().Format(time.RFC3339))
			}
			if _, ok := updates["oauth_expires_at"]; !ok {
				updates["oauth_expires_at"] = claims.Exp
			}
		}
	}

	if planType != "" {
		updates["plan_type"] = planType
		if normalized := normalizeAccountType(planType); normalized != "" {
			updates["type"] = normalized
		}
	}

	// OAuth ChatGPT accounts do not surface explicit image quotas at
	// import time. Mark image_quota_unknown so the scheduler treats the
	// account as available until the next refresh fills in real numbers.
	updates["image_quota_unknown"] = true

	return updates, nil
}

// decodeJWTClaims parses the payload portion of a JWS token without
// verifying its signature. This is sufficient for harvesting OpenAI-issued
// metadata claims; the upstream service performs full verification on use.
func decodeJWTClaims(token string) (*jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid jwt format")
	}
	payload, err := decodeJWTSegment(parts[1])
	if err != nil {
		return nil, err
	}
	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("decode jwt payload: %w", err)
	}
	return &claims, nil
}

// decodeJWTSegment is tolerant of the various base64 paddings JWT
// implementations emit. It tries the standard URL-safe encoding first
// because that is what RFC 7519 requires.
func decodeJWTSegment(segment string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(segment); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(segment); err == nil {
		return decoded, nil
	}
	padded := segment
	if rem := len(padded) % 4; rem > 0 {
		padded += strings.Repeat("=", 4-rem)
	}
	if decoded, err := base64.URLEncoding.DecodeString(padded); err == nil {
		return decoded, nil
	}
	return base64.StdEncoding.DecodeString(padded)
}
