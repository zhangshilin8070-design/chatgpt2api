package backend

import (
	"regexp"
	"strconv"
	"strings"

	"chatgpt2api/internal/util"
)

// codex/responses 图像工具接受的 tool.size 白名单。
// 与 OpenAI Images API（gpt-image-1/2）一致，超出此集合的取值会被上游打回
// 5xx（被 classifyOpenAIImagesError 归类为 transient），因此本端在下发前
// 必须把所有非白名单输入收敛为这三档之一，否则就丢弃 size 字段。
const (
	responsesImageToolSizeSquare    = "1024x1024"
	responsesImageToolSizeLandscape = "1536x1024"
	responsesImageToolSizePortrait  = "1024x1536"
)

func supportsResponsesImageOutputCompression(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpg", "jpeg":
		return true
	default:
		return false
	}
}

func normalizeResponsesImageToolModel(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "", util.ImageModelAuto, "gpt-image-1", util.ImageModelGPTImage2:
		return ""
	case util.ImageModelCodexGPTImage2:
		return ResponsesImageCodexToolModel
	case ResponsesImageCodexToolModel:
		return ResponsesImageCodexToolModel
	case util.ImageModelGPT54:
		return util.ImageModelGPT54
	case util.ImageModelGPT55:
		return util.ImageModelGPT55
	case "gpt-5-5-thinking":
		return "gpt-5-5-thinking"
	default:
		return ""
	}
}

// normalizeResponsesImageToolSize 把请求里的 size 字段收敛到 codex/responses
// 接受的三档白名单：1024x1024 / 1536x1024 / 1024x1536。
//
// 收敛规则：
//   - 空 / auto / 分档预设（1080p / 2k / 4k）→ 返回空串，不下发 tool.size，
//     由上游按默认 smimage（1024×1024）出图，分档信息靠 prompt 提示传递。
//   - 像素形态（WIDTHxHEIGHT）→ 按 W/H 比例落到最接近的白名单（正方/横/竖）；
//     无法解析的像素值返回空串。
//   - 比例形态（"a:b"）→ 同上，按比例落到白名单。
//   - 其它取值 → 返回空串。
func normalizeResponsesImageToolSize(size string) string {
	normalized := strings.ToLower(strings.TrimSpace(size))
	normalized = strings.ReplaceAll(normalized, " ", "")
	normalized = strings.ReplaceAll(normalized, "×", "x")
	if normalized == "" || normalized == "auto" {
		return ""
	}
	switch normalized {
	case "1080p", "2k", "4k":
		return ""
	}
	if width, height, ok := parseResponsesImageDimensions(normalized); ok {
		return classifyResponsesImageToolSize(float64(width), float64(height))
	}
	if ratioWidth, ratioHeight, ok := parseResponsesImageRatio(normalized); ok {
		return classifyResponsesImageToolSize(ratioWidth, ratioHeight)
	}
	return ""
}

// classifyResponsesImageToolSize 根据宽高（或宽高比）选出最接近的 codex
// 白名单尺寸。比例阈值 1.1 是宽容窗口：1024:1024(=1) 内的 1.0~1.1 算正方，
// 大于 1.1 算横版，小于 1/1.1 算竖版，与官方实测的 1024×1024 / 1536×1024 /
// 1024×1536 命中边界对齐。
func classifyResponsesImageToolSize(width, height float64) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	ratio := width / height
	switch {
	case ratio >= 1.1:
		return responsesImageToolSizeLandscape
	case ratio <= 1/1.1:
		return responsesImageToolSizePortrait
	default:
		return responsesImageToolSizeSquare
	}
}

func parseResponsesImageDimensions(value string) (int, int, bool) {
	match := regexp.MustCompile(`^(\d+)x(\d+)$`).FindStringSubmatch(value)
	if len(match) != 3 {
		return 0, 0, false
	}
	width, err := strconv.Atoi(match[1])
	if err != nil || width <= 0 {
		return 0, 0, false
	}
	height, err := strconv.Atoi(match[2])
	if err != nil || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

func parseResponsesImageRatio(value string) (float64, float64, bool) {
	match := regexp.MustCompile(`^(\d+(?:\.\d+)?):(\d+(?:\.\d+)?)$`).FindStringSubmatch(value)
	if len(match) != 3 {
		return 0, 0, false
	}
	width, err := strconv.ParseFloat(match[1], 64)
	if err != nil || width <= 0 {
		return 0, 0, false
	}
	height, err := strconv.ParseFloat(match[2], 64)
	if err != nil || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

// normalizeOpenAIImagesSize 把 /v1/images/generations 与 /v1/images/edits
// 请求里的 size 字段收敛到上游网关（Axiom）接受的形态。
//
// 依据 IMAGE-API.local.md §6：
//   - `1k` / `2k` / `4k`：档位字面值，原样下发，由网关按最长边映射计价档；
//   - `WIDTHxHEIGHT`：原样下发，网关自动按最长边归档；
//   - `auto`：原样下发（网关按默认 1k 处理）；
//   - 空 / `1080p`（非网关字典字面）/ 比例形态（"16:9"）等：返回空串，
//     不下发 size，让网关按默认 1k 出图。比例信息靠 prompt 提示传递。
func normalizeOpenAIImagesSize(size string) string {
	normalized := strings.ToLower(strings.TrimSpace(size))
	normalized = strings.ReplaceAll(normalized, " ", "")
	normalized = strings.ReplaceAll(normalized, "×", "x")
	if normalized == "" {
		return ""
	}
	switch normalized {
	case "auto", "1k", "2k", "4k":
		return normalized
	case "1080p":
		// 1080p 不在 Axiom 网关字典里，与 2k 接近但语义不同，丢弃不下发。
		return ""
	}
	if _, _, ok := parseResponsesImageDimensions(normalized); ok {
		// 任意 WIDTHxHEIGHT 原样透传，网关按最长边自动归 1k/2k/4k 档。
		return normalized
	}
	// 纯比例（"16:9"）网关不接受，丢弃。
	return ""
}

// normalizeOpenAIImagesQuality 把外部传入的 quality 收敛到 OpenAI Images API
// 文档（IMAGE-API.local.md §4）允许的字面值（low / medium / high / auto）。
// 空 / 非法值返回空串，不下发，让上游按默认值处理。
func normalizeOpenAIImagesQuality(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "low", "medium", "high", "auto":
		return normalized
	default:
		return ""
	}
}

// normalizeOpenAIImagesBackground 仅对空白做剪裁，原样透传上游。
// 依据 IMAGE-API.local.md §4：扩展字段"原样透传上游"，本端不做白名单收敛，
// 让上游网关 / 模型自行判定是否接受。
func normalizeOpenAIImagesBackground(value string) string {
	return strings.TrimSpace(value)
}

// normalizeOpenAIImagesModeration 与 background 同样原样透传（仅去空白），
// 让上游决定是否接受。
func normalizeOpenAIImagesModeration(value string) string {
	return strings.TrimSpace(value)
}

// supportsOpenAIImagesOutputCompression 决定是否下发 output_compression。
// 依据 IMAGE-API.local.md §4：扩展字段原样透传上游。本端只做 0~100 范围卡控，
// 不再按 output_format 屏蔽，让上游决定是否支持当前 format 上的压缩参数。
func supportsOpenAIImagesOutputCompression(format string) bool {
	_ = format
	return true
}

// clampOpenAIImagesCompression 把 0~100 范围外的值压回区间。
func clampOpenAIImagesCompression(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// resolveOpenAIImagesResponseFormat 决定 /v1/images/{generations,edits} 的
// response_format 字段值。
//
// 依据 IMAGE-API.local.md §4 + §5：默认 b64_json（后端 DTO 也按 b64 字段解
// 析）；外部显式传 "url" 时下发 url，由调用方在响应处理时识别 url 字段
// （openaiImagesResponseDTO 已同时支持 b64_json 与 url）。
//
// 其它任何取值都视为非法，回退到 b64_json，防止把"auto"等模糊词原样下发
// 导致上游 400。
func resolveOpenAIImagesResponseFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "url":
		return "url"
	default:
		return openaiImagesResponseFormatB64JSON
	}
}
