package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"chatgpt2api/internal/protocol"
	"chatgpt2api/internal/service"
	"chatgpt2api/internal/util"
)

func (a *App) logCall(identity service.Identity, summary, method, endpoint, model string, started time.Time, outcome string, status int, errText string, urls []string, requestCapture auditRequestCapture, routings ...imageRoutingDetailFields) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if status <= 0 {
		status = http.StatusOK
		if outcome == "failed" {
			status = http.StatusInternalServerError
		}
	}
	ended := time.Now()
	detail := map[string]any{
		"method":         method,
		"path":           endpoint,
		"endpoint":       endpoint,
		"module":         inferAuditModule(endpoint),
		"model":          model,
		"started_at":     started.Format("2006-01-02 15:04:05"),
		"ended_at":       ended.Format("2006-01-02 15:04:05"),
		"duration_ms":    ended.Sub(started).Milliseconds(),
		"status":         status,
		"outcome":        outcome,
		"operation_type": operationTypeForMethod(method),
		"log_level":      logLevelForStatus(status),
	}
	addIdentityLogDetail(detail, identity)
	if name := identityDisplayName(identity); name != "" {
		detail["username"] = name
	}
	if errText != "" {
		detail["error"] = errText
	}
	if len(urls) > 0 {
		detail["urls"] = dedupe(urls)
	}
	addAuditRequestDetail(detail, requestCapture)
	if len(routings) > 0 {
		addImageRoutingDetail(detail, routings[0])
	}
	suffix := "调用完成"
	if outcome == "failed" {
		suffix = "调用失败"
	}
	a.logs.Add(summary+suffix, detail)
}

// imageRoutingDetailFields 承载生图调用的桶 / 路由审计字段，由
// runLoggedImageTask 与 writeProtocol 在每条生图相关日志写入时填充。
//
// 字段语义：
//   - bucket：BillingService 实际作用的桶（bucket_a / bucket_b）。
//   - resolvedModel：Auto 路由解析后的对外模型；非 auto 调用与 originalModel 相等。
//   - upstreamKind：物理上游通路标识（chatgpt / openai_api），仅成功且
//     至少一张图交付后才有值；失败或纯文本响应保持空串。
//
// _Requirements: 9.3, 9.4
type imageRoutingDetailFields struct {
	bucket        string
	resolvedModel string
	upstreamKind  string
}

// imageRoutingDetail 从（result, body）这对来源中提取 bucket / resolved_model
// / upstream_kind。优先 result（Image_Engine 已写入），缺失时退到 body
// （流式或预扣费失败前 result 可能为 nil 或字段未填）。
//
// 这两条来源是同一组数据的不同读出点，并非兼容层：result 在非流式成功
// 路径上由 annotateImageResult 写入；body 在 Auto 路由结束后由
// annotateImageRequestBody 写入。两个写入点都在 Image_Engine，读出点在
// httpapi.logCall 与 runLoggedImageTask。
func imageRoutingDetail(result, body map[string]any) imageRoutingDetailFields {
	pickFromResult := func(key string) string {
		if result == nil {
			return ""
		}
		return strings.TrimSpace(util.Clean(result[key]))
	}
	pickFromBody := func(key string) string {
		if body == nil {
			return ""
		}
		return strings.TrimSpace(util.Clean(body[key]))
	}
	return imageRoutingDetailFields{
		bucket:        firstNonEmpty(pickFromResult("bucket"), pickFromBody("bucket")),
		resolvedModel: firstNonEmpty(pickFromResult("resolved_model"), pickFromBody("resolved_model")),
		upstreamKind:  pickFromResult("upstream_kind"),
	}
}

// addImageRoutingDetail 把 imageRoutingDetailFields 三项写入日志 detail
// map；空字段直接跳过，避免在非生图日志或尚未路由的失败日志中写出空串
// 字段（保持 detail 形状最小）。
func addImageRoutingDetail(detail map[string]any, fields imageRoutingDetailFields) {
	if detail == nil {
		return
	}
	if fields.bucket != "" {
		detail["bucket"] = fields.bucket
	}
	if fields.resolvedModel != "" {
		detail["resolved_model"] = fields.resolvedModel
	}
	if fields.upstreamKind != "" {
		detail["upstream_kind"] = fields.upstreamKind
	}
}

func addIdentityLogDetail(detail map[string]any, identity service.Identity) {
	kind := util.Clean(identity.Kind)
	if kind != "" {
		detail["auth_kind"] = kind
	}
	credentialName := util.Clean(identity.CredentialName)
	if identity.Kind == service.AuthKindSession {
		if credentialName != "" {
			detail["session_name"] = credentialName
		}
	} else if name := util.Clean(firstNonEmpty(identity.CredentialName, identity.Name)); name != "" {
		detail["key_name"] = name
	}
	if role := util.Clean(identity.Role); role != "" {
		detail["key_role"] = role
	}
	if id := util.Clean(firstNonEmpty(identity.CredentialID, identity.ID)); id != "" {
		detail["key_id"] = id
	}
	if id := util.Clean(identity.ID); id != "" && id != util.Clean(identity.CredentialID) {
		detail["subject_id"] = id
	}
	if provider := util.Clean(identity.Provider); provider != "" {
		detail["provider"] = provider
	}
}

func payloadAuditCapture(payload map[string]any) auditRequestCapture {
	args := cleanAuditPayloadMap(payload)
	if len(args) == 0 {
		return auditRequestCapture{}
	}
	return auditRequestCapture{args: service.SanitizeLogValue(args)}
}

func cleanAuditPayloadMap(payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		switch key {
		case "owner_id", "owner_name", "base_url":
			continue
		}
		if isInternalPayloadValue(value) {
			continue
		}
		out[key] = cleanAuditPayloadValue(value)
	}
	return out
}

func cleanAuditPayloadValue(value any) any {
	switch x := value.(type) {
	case []protocol.UploadedImage:
		items := make([]map[string]any, 0, len(x))
		for _, image := range x {
			items = append(items, map[string]any{
				"filename":     image.Filename,
				"content_type": image.ContentType,
				"size_bytes":   len(image.Data),
			})
		}
		return items
	case protocol.UploadedImage:
		return map[string]any{
			"filename":     x.Filename,
			"content_type": x.ContentType,
			"size_bytes":   len(x.Data),
		}
	default:
		return value
	}
}

func isInternalPayloadValue(value any) bool {
	if value == nil {
		return false
	}
	switch value.(type) {
	case func(context.Context, int) (func(), error), func([]map[string]any):
		return true
	default:
		return false
	}
}

func identityScope(identity service.Identity) string {
	if owner := util.Clean(identity.OwnerID); owner != "" {
		return owner
	}
	if id := util.Clean(identity.ID); id != "" {
		return id
	}
	return "anonymous"
}

func identityDisplayName(identity service.Identity) string {
	return firstNonEmpty(util.Clean(identity.Name), util.Clean(identity.CredentialName))
}

func imageAccessScope(identity service.Identity) service.ImageAccessScope {
	if identity.Role == service.AuthRoleAdmin {
		return service.ImageAccessScope{All: true}
	}
	return service.ImageAccessScope{OwnerID: identityScope(identity)}
}

func imageListAccessScope(identity service.Identity, value string) (service.ImageAccessScope, int, string) {
	switch strings.TrimSpace(value) {
	case "":
		return imageAccessScope(identity), 0, ""
	case "mine":
		return service.ImageAccessScope{OwnerID: identityScope(identity)}, 0, ""
	case "public":
		if identity.Role == service.AuthRoleAdmin {
			return service.ImageAccessScope{All: true}, 0, ""
		}
		return service.ImageAccessScope{Public: true}, 0, ""
	case "all":
		if identity.Role != service.AuthRoleAdmin {
			return service.ImageAccessScope{}, http.StatusForbidden, "admin permission required"
		}
		return service.ImageAccessScope{All: true}, 0, ""
	default:
		return service.ImageAccessScope{}, http.StatusBadRequest, "scope must be mine, public, or all"
	}
}
