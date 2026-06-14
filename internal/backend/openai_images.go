package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	urlpkg "net/url"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// dataURLRegexp 匹配 RFC 2397 data URL（聚合服务把 base64 图像直接塞回 chat
// 响应里时常用）。捕获组保留整段 URL 供调用方解码。
var dataURLRegexp = regexp.MustCompile(`data:image/[A-Za-z0-9.+\-]+;base64,[A-Za-z0-9+/=]+`)

// markdownImageRegexp 匹配 markdown 图片语法 ![alt](url)。仅捕获 URL 部分；
// alt 文本与 title 都被丢弃。
var markdownImageRegexp = regexp.MustCompile(`!\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)

// OpenAIImagesClient 是 OpenAI 协议生图客户端。每次请求由 Image_Engine 在
// ReserveForUpstreamModel 之后构造，绑定单个 OpenAIAccountReservation 的
// BaseURL 与 APIKey。客户端不持有任何账号级状态，调用结束后即可丢弃。
type OpenAIImagesClient struct {
	HTTPClient *http.Client
	BaseURL    string
	APIKey     string
}

// OpenAIImagesRequest 是统一的生成 / 编辑请求形态。
//
// UpstreamModel 取自 util.UpstreamModelForExternal(external)，由调用方负责
// 在路由层完成对外模型→上游模型的解析。本结构不感知外部模型名。
type OpenAIImagesRequest struct {
	UpstreamModel  string
	Prompt         string
	N              int
	Size           string
	OutputFormat   string
	// ImageResolution 来自 ConversationRequest.ImageResolution（生图页"分辨率"
	// 下拉的 1080p / 2k / 4k / auto 预设）。仅 chat/completions 协议使用，
	// 写入 image_size 字段（大写形态如 "4K"），与 newapi 等聚合服务对齐。
	// 标准 OpenAI Images API 不读该字段。
	ImageResolution string
	InputImages    []ResponsesInputImage // 用于 edits
	InputImageMask *ResponsesInputImage  // 用于 edits（可选）
}

// OpenAIImagesResult 是 OpenAI Images API 响应的归一化形态。
type OpenAIImagesResult struct {
	Data []OpenAIImageDatum
}

// OpenAIImageDatum 对应响应 data[].* 字段，按当前 OpenAI Images API 文档定义。
type OpenAIImageDatum struct {
	B64JSON       string
	URL           string
	RevisedPrompt string
}

// ImageOutput 是 OpenAI 协议生图后端输出的与 Image_Engine 字段对齐的结构。
// Engine 在路由层把该结构透传给下游存图、缩略图、计费扣费钩子。Raw 用于承载
// 路由层关心的元信息，例如 upstream_kind / revised_prompt。
type ImageOutput struct {
	Kind              string
	Model             string
	Index             int
	Total             int
	Created           int64
	Text              string
	UpstreamEventType string
	Data              []map[string]any
	ChargeHandled     bool
	Raw               map[string]any
}

// OpenAIImagesErrorKind 标识上游错误的归类，由 Image_Engine 用 errors.As
// 判别后决定账号置位（异常 / 限流）与跨通路重试策略。
type OpenAIImagesErrorKind int

const (
	// OpenAIImagesErrorAuth 表示账号鉴权失败，调用方应把对应账号-模型
	// model_states[m].status 置为「异常」。
	OpenAIImagesErrorAuth OpenAIImagesErrorKind = iota + 1
	// OpenAIImagesErrorRateLimit 表示账号被限流，调用方应把对应账号-模型
	// model_states[m].status 置为「限流」。
	OpenAIImagesErrorRateLimit
	// OpenAIImagesErrorTransient 表示瞬时性错误（5xx / 网络），允许跨通路
	// 共享 retry budget 内重试。
	OpenAIImagesErrorTransient
	// OpenAIImagesErrorTimeout 表示客户端等待上游响应超时（例如 newapi 等
	// 聚合服务转发到 Gemini / Vertex 时长尾分布超过 180s 客户端 timeout）。
	// 与限流同类：占用「下一个账号」机会但不消耗 transient 重试预算，
	// 避免一次慢上游就把 3 次跨通路重试预算耗光。
	OpenAIImagesErrorTimeout
	// OpenAIImagesErrorPermanent 表示其它 4xx 永久性错误，调用方决定是否
	// 透传给客户端。
	OpenAIImagesErrorPermanent
)

// OpenAIImagesError 是 OpenAI 协议出图客户端的统一错误类型。
type OpenAIImagesError struct {
	Kind    OpenAIImagesErrorKind
	Status  int
	Message string
	Raw     []byte
}

func (e *OpenAIImagesError) Error() string {
	if e == nil {
		return ""
	}
	if e.Status > 0 && e.Message != "" {
		return fmt.Sprintf("openai images upstream %d: %s", e.Status, e.Message)
	}
	if e.Status > 0 {
		return fmt.Sprintf("openai images upstream returned status %d", e.Status)
	}
	if e.Message != "" {
		return "openai images request failed: " + e.Message
	}
	return "openai images request failed"
}

// Generate 通过 POST {BaseURL}/v1/images/generations 调用上游生图。
//
// 行为遵循当前 OpenAI Images API 文档的字段集，不对历史字段做兼容处理。
// 失败时返回 *OpenAIImagesError，由 Image_Engine 根据 Kind 决定后续动作。
func (c *OpenAIImagesClient) Generate(ctx context.Context, req OpenAIImagesRequest) (*OpenAIImagesResult, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if err := req.validateGenerate(); err != nil {
		return nil, err
	}

	payload := openaiImagesGenerationDTO{
		Model:          req.UpstreamModel,
		Prompt:         req.Prompt,
		ResponseFormat: openaiImagesResponseFormatB64JSON,
	}
	if req.N >= 1 {
		payload.N = req.N
	}
	if size := strings.TrimSpace(req.Size); size != "" {
		payload.Size = size
	}
	if format := strings.TrimSpace(req.OutputFormat); format != "" {
		payload.OutputFormat = format
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode openai images generation payload: %w", err)
	}

	target, err := buildOpenAIImagesURL(c.BaseURL, openaiImagesGenerationsPath)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build openai images generation request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	return c.execute(httpReq)
}

// Edit 通过 POST {BaseURL}/v1/images/edits 调用上游编辑生图。
//
// 请求体使用 multipart/form-data；每张参考图作为独立的 image 表单分片，
// 可选 mask 作为 mask 字段。其余文本字段与 Generate 一致。
func (c *OpenAIImagesClient) Edit(ctx context.Context, req OpenAIImagesRequest) (*OpenAIImagesResult, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if err := req.validateEdit(); err != nil {
		return nil, err
	}

	bodyBuf := &bytes.Buffer{}
	writer := multipart.NewWriter(bodyBuf)

	if err := writer.WriteField("model", req.UpstreamModel); err != nil {
		return nil, fmt.Errorf("write multipart model: %w", err)
	}
	if err := writer.WriteField("prompt", req.Prompt); err != nil {
		return nil, fmt.Errorf("write multipart prompt: %w", err)
	}
	if req.N >= 1 {
		if err := writer.WriteField("n", strconv.Itoa(req.N)); err != nil {
			return nil, fmt.Errorf("write multipart n: %w", err)
		}
	}
	if size := strings.TrimSpace(req.Size); size != "" {
		if err := writer.WriteField("size", size); err != nil {
			return nil, fmt.Errorf("write multipart size: %w", err)
		}
	}
	if err := writer.WriteField("response_format", openaiImagesResponseFormatB64JSON); err != nil {
		return nil, fmt.Errorf("write multipart response_format: %w", err)
	}
	if format := strings.TrimSpace(req.OutputFormat); format != "" {
		if err := writer.WriteField("output_format", format); err != nil {
			return nil, fmt.Errorf("write multipart output_format: %w", err)
		}
	}

	for index, image := range req.InputImages {
		if err := writeOpenAIImagesPart(writer, "image", index, image); err != nil {
			return nil, err
		}
	}
	if req.InputImageMask != nil && len(req.InputImageMask.Data) > 0 {
		if err := writeOpenAIImagesPart(writer, "mask", 0, *req.InputImageMask); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close multipart writer: %w", err)
	}

	target, err := buildOpenAIImagesURL(c.BaseURL, openaiImagesEditsPath)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bodyBuf)
	if err != nil {
		return nil, fmt.Errorf("build openai images edit request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	httpReq.Header.Set("Accept", "application/json")

	return c.execute(httpReq)
}

// ===== Chat-completions 协议（用于 gemini-3.1-flash-image 等）=====

// GenerateViaChat 通过 POST {BaseURL}/v1/chat/completions 走聚合服务的多模态
// 接口生图。某些模型（例如 newapi.qianqianye.com 上的 gemini-3.1-flash-image）
// 不在 /v1/images/generations 白名单内，但能通过 chat/completions 返回图像
// （多模态 image_url）。请求体走 messages[0].content = [{type:"text", text:prompt}]，
// 响应里图像可能以下面任一形态出现：
//
//   - choices[0].message.content 含 data:image/...;base64,... data URL
//   - choices[0].message.content 含 markdown ![](https://...) 链接
//   - choices[0].message.images[] 数组（OpenRouter 风格），元素是
//     {"type":"image_url","image_url":{"url":"data:image/..."}} 或 {"url":"https://..."}
//
// extractOpenAIImagesFromChat 会同时识别这三种形态，按出现顺序合并到 Data。
// 失败语义与 Generate 一致，由 Image_Engine 通过 OpenAIImagesError.Kind 判定。
func (c *OpenAIImagesClient) GenerateViaChat(ctx context.Context, req OpenAIImagesRequest) (*OpenAIImagesResult, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if err := req.validateGenerate(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(buildOpenAIChatPayload(req, nil))
	if err != nil {
		return nil, fmt.Errorf("encode openai chat completions payload: %w", err)
	}
	target, err := buildOpenAIImagesURL(c.BaseURL, openaiImagesChatCompletionsPath)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build openai chat completions request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	return c.executeChat(httpReq)
}

// EditViaChat 通过 POST {BaseURL}/v1/chat/completions 走聚合服务的多模态
// 接口编辑生图。参考图作为 messages[0].content 中 image_url(data URL) 项注入。
func (c *OpenAIImagesClient) EditViaChat(ctx context.Context, req OpenAIImagesRequest) (*OpenAIImagesResult, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if err := req.validateEdit(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(buildOpenAIChatPayload(req, req.InputImages))
	if err != nil {
		return nil, fmt.Errorf("encode openai chat completions payload: %w", err)
	}
	target, err := buildOpenAIImagesURL(c.BaseURL, openaiImagesChatCompletionsPath)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build openai chat completions request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	return c.executeChat(httpReq)
}

// buildOpenAIChatPayload 构造 chat/completions 请求体。
//
// 双形态：
//   - 纯文生图（references 为空）：messages[0].content = "prompt 字符串"，
//     与 newapi.qianqianye.com gemini 路径示例一致。
//   - 编辑场景（references 非空）：messages[0].content = [{type:text,text:prompt},
//     {type:image_url,image_url:{url:dataURL}}, ...] multimodal 数组，
//     允许多张参考图。
//
// 关于尺寸：经实测，newapi 上 gemini-3.1-flash-image 完全忽略
// image_size / size / aspect_ratio / extra_body / generation_config 等所有
// 顶层字段（默认输出 1408×768），唯一能控制比例的渠道是把比例提示写进
// prompt 文本（例如 "Portrait 9:16: ..."、"Square 1:1: ..."）。因此本函数
// 不再下发 image_size，而是把 ConversationRequest.Size / ImageResolution
// 翻译为 prompt 前缀（仅 chat 协议；标准 OpenAI Images API 仍按字段下发）。
//
// 同步等待完整响应（不引入 stream / SSE）：调用方仍按"提交一次拿一次结果"
// 的语义在 runOpenAIImagesCandidate 里跑跨账号轮询。
func buildOpenAIChatPayload(req OpenAIImagesRequest, references []ResponsesInputImage) map[string]any {
	prompt := decoratePromptWithSizeHint(req)
	payload := map[string]any{
		"model": req.UpstreamModel,
	}
	if len(references) == 0 {
		payload["messages"] = []map[string]any{
			{"role": "user", "content": prompt},
		}
	} else {
		parts := make([]map[string]any, 0, len(references)+1)
		parts = append(parts, map[string]any{"type": "text", "text": prompt})
		for _, ref := range references {
			if len(ref.Data) == 0 {
				continue
			}
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": encodeDataURL(ref.ContentType, ref.Data)},
			})
		}
		payload["messages"] = []map[string]any{
			{"role": "user", "content": parts},
		}
	}
	if req.N >= 1 {
		payload["n"] = req.N
	}
	return payload
}

// decoratePromptWithSizeHint 把 OpenAIImagesRequest 上的尺寸字段翻译为
// prompt 前缀，用于 newapi gemini 等 chat-completions 上游（这些上游忽略
// JSON 顶层 size 字段，但响应 prompt 中的比例描述）。
//
// 翻译规则：
//   - Size 是比例形态（"1:1" / "16:9" / "9:16" / 其它带 ":" 的）→ 直接当 aspect ratio
//   - Size 是像素形态（"1024x1024" / "1024X1024"）→ 解析为比例
//   - ImageResolution（1080p / 2k / 4k）→ 不能控制实际像素，但作为 quality 提示加入
//
// 实测 1:1 → 1024×1024，9:16 → 768×1376，16:9 → 1376×768，与 prompt 前缀
// 行为一致；image_size / size / extra_body 等 16 种字段全部被忽略。
//
// 当 prompt 已经以相同的比例提示开头时不重复添加，避免多次重发同一会话
// 累积出 "Square 1:1: Square 1:1: ..." 这种污染。
func decoratePromptWithSizeHint(req OpenAIImagesRequest) string {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return prompt
	}
	hint := buildSizeHintPrefix(req.Size)
	if hint == "" {
		return prompt
	}
	if strings.HasPrefix(prompt, hint) {
		return prompt
	}
	return hint + " " + prompt
}

// buildSizeHintPrefix 根据 Size 字段输出 prompt 前缀。空串表示不加前缀。
func buildSizeHintPrefix(size string) string {
	value := strings.TrimSpace(size)
	if value == "" {
		return ""
	}
	width, height, ok := parseAspectRatio(value)
	if !ok {
		return ""
	}
	switch {
	case width == height:
		return "Square 1:1:"
	case width > height:
		return fmt.Sprintf("Landscape %d:%d:", width, height)
	default:
		return fmt.Sprintf("Portrait %d:%d:", width, height)
	}
}

// parseAspectRatio 把 "16:9" / "1024x1024" / "1024X1024" / "1:1" 等取值解析为
// (width, height) 整数对；其他形态返回 ok=false。
func parseAspectRatio(value string) (int, int, bool) {
	s := strings.ToLower(strings.TrimSpace(value))
	if s == "" {
		return 0, 0, false
	}
	for _, sep := range []string{":", "x", "*"} {
		if !strings.Contains(s, sep) {
			continue
		}
		parts := strings.SplitN(s, sep, 2)
		if len(parts) != 2 {
			continue
		}
		w, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
		h, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
		if errW != nil || errH != nil || w <= 0 || h <= 0 {
			continue
		}
		return w, h, true
	}
	return 0, 0, false
}

func encodeDataURL(contentType string, data []byte) string {
	mime := strings.TrimSpace(contentType)
	if mime == "" {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// executeChat 与 execute 行为一致，但响应解析走 chat/completions DTO。
// 错误归类、timeout 识别与 Generate / Edit 完全共享。
func (c *OpenAIImagesClient) executeChat(req *http.Request) (*OpenAIImagesResult, error) {
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		kind := OpenAIImagesErrorTransient
		if isHTTPTimeoutError(err) {
			kind = OpenAIImagesErrorTimeout
		}
		return nil, &OpenAIImagesError{Kind: kind, Status: 0, Message: err.Error()}
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, &OpenAIImagesError{
			Kind:    OpenAIImagesErrorTransient,
			Status:  resp.StatusCode,
			Message: fmt.Sprintf("read response body: %v", readErr),
			Raw:     body,
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, classifyOpenAIImagesError(resp.StatusCode, body)
	}
	images, text, parseErr := extractOpenAIImagesFromChat(body)
	if parseErr != nil {
		return nil, &OpenAIImagesError{
			Kind:    OpenAIImagesErrorPermanent,
			Status:  resp.StatusCode,
			Message: parseErr.Error(),
			Raw:     body,
		}
	}
	if len(images) == 0 {
		// 收到 200 但没有任何图像数据：归为永久错误，让 Engine 把账号
		// model_states 标记为异常（"model can't return images"）。
		message := "no image data in chat completions response"
		if text != "" {
			message = message + ": " + truncateText(text, 200)
		}
		return nil, &OpenAIImagesError{
			Kind:    OpenAIImagesErrorPermanent,
			Status:  resp.StatusCode,
			Message: message,
			Raw:     body,
		}
	}
	return &OpenAIImagesResult{Data: images}, nil
}

// extractOpenAIImagesFromChat 把 chat/completions 响应里的图像数据归并出来。
// 同时支持：data URL（base64）、markdown ![](https://...)、images[] 数组。
// 第二个返回值是 message.content 的文本部分，仅在没有任何图像时供错误信息使用。
func extractOpenAIImagesFromChat(body []byte) ([]OpenAIImageDatum, string, error) {
	var dto struct {
		Choices []struct {
			Message struct {
				// content 既可能是字符串也可能是 [{type,text,image_url:{url}}]
				// 数组（多模态消息）；为避免双形态解析失败，先用 RawMessage 接收。
				Content json.RawMessage `json:"content"`
				Images  []struct {
					Type     string `json:"type"`
					URL      string `json:"url"`
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"images"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &dto); err != nil {
		return nil, "", fmt.Errorf("decode chat completions response: %w", err)
	}
	if len(dto.Choices) == 0 {
		return nil, "", nil
	}
	choice := dto.Choices[0]
	textContent, parts, err := decodeChatMessageContent(choice.Message.Content)
	if err != nil {
		return nil, "", err
	}
	out := make([]OpenAIImageDatum, 0)
	// 1) message.content 文本里的 data URL / markdown URL
	out = append(out, extractImagesFromText(textContent)...)
	// 2) message.content 多模态分片里 image_url 字段
	for _, part := range parts {
		if url := strings.TrimSpace(part); url != "" {
			out = appendChatImage(out, url)
		}
	}
	// 3) message.images[] 数组（OpenRouter 风格）
	for _, image := range choice.Message.Images {
		if url := strings.TrimSpace(image.URL); url != "" {
			out = appendChatImage(out, url)
		}
		if url := strings.TrimSpace(image.ImageURL.URL); url != "" {
			out = appendChatImage(out, url)
		}
	}
	return out, textContent, nil
}

// decodeChatMessageContent 处理 chat/completions message.content 既可能是字符串
// 也可能是数组的两种形态。返回 (合并后的文本内容, 多模态分片中 image_url 的 url)。
func decodeChatMessageContent(raw json.RawMessage) (string, []string, error) {
	if len(raw) == 0 {
		return "", nil, nil
	}
	// string
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString, nil, nil
	}
	// array
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", nil, fmt.Errorf("decode chat completions content: %w", err)
	}
	var text strings.Builder
	urls := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			if part.Text != "" {
				if text.Len() > 0 {
					text.WriteString("\n")
				}
				text.WriteString(part.Text)
			}
		case "image_url":
			if url := strings.TrimSpace(part.ImageURL.URL); url != "" {
				urls = append(urls, url)
			}
		}
	}
	return text.String(), urls, nil
}

// extractImagesFromText 从纯文本里识别图像数据：
//   - data:image/...;base64,... 的 data URL 整段
//   - markdown ![alt](https://...) 中的 URL
func extractImagesFromText(text string) []OpenAIImageDatum {
	if text == "" {
		return nil
	}
	out := make([]OpenAIImageDatum, 0)
	// data URL 必须在 markdown 之前抽（markdown 也可能包 data URL）
	for _, match := range dataURLRegexp.FindAllString(text, -1) {
		out = appendChatImage(out, match)
	}
	for _, match := range markdownImageRegexp.FindAllStringSubmatch(text, -1) {
		if len(match) > 1 {
			out = appendChatImage(out, strings.TrimSpace(match[1]))
		}
	}
	return out
}

// appendChatImage 把单个 image url 归并到 OpenAIImageDatum 列表，区分 data URL 与 https URL。
func appendChatImage(list []OpenAIImageDatum, url string) []OpenAIImageDatum {
	url = strings.TrimSpace(url)
	if url == "" {
		return list
	}
	if strings.HasPrefix(url, "data:") {
		// data:image/png;base64,xxx → b64_json = xxx
		if idx := strings.Index(url, ",") ; idx >= 0 {
			b64 := url[idx+1:]
			if b64 != "" {
				return append(list, OpenAIImageDatum{B64JSON: b64})
			}
		}
		return list
	}
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		return append(list, OpenAIImageDatum{URL: url})
	}
	return list
}

func truncateText(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max] + "..."
}
//
//   - model：对外模型名（不是 upstream model）
//   - startIndex：当前批次在整个请求 outputs 中的起始下标
//   - total：整个请求总输出数
//   - upstreamKind：固定 util.UpstreamKindOpenAIAPI，由调用方传入以便审计
func (r *OpenAIImagesResult) ToImageOutputs(model string, startIndex, total int, upstreamKind string) []ImageOutput {
	if r == nil || len(r.Data) == 0 {
		return nil
	}
	outputs := make([]ImageOutput, 0, len(r.Data))
	created := time.Now().Unix()
	for i, datum := range r.Data {
		item := map[string]any{}
		if datum.B64JSON != "" {
			item["b64_json"] = datum.B64JSON
		}
		if datum.URL != "" {
			item["url"] = datum.URL
		}
		if datum.RevisedPrompt != "" {
			item["revised_prompt"] = datum.RevisedPrompt
		}
		if _, hasFormat := item["output_format"]; !hasFormat {
			item["output_format"] = "png"
		}
		raw := map[string]any{}
		if upstreamKind != "" {
			raw["upstream_kind"] = upstreamKind
		}
		if datum.RevisedPrompt != "" {
			raw["revised_prompt"] = datum.RevisedPrompt
		}
		outputs = append(outputs, ImageOutput{
			Kind:    "result",
			Model:   model,
			Index:   startIndex + i,
			Total:   total,
			Created: created,
			Data:    []map[string]any{item},
			Raw:     raw,
		})
	}
	return outputs
}

// classifyOpenAIImagesError 把上游响应转换为后端错误。
//   - HTTP 401 或响应体含 invalid_api_key  → OpenAIImagesErrorAuth      （账号需置异常）
//   - HTTP 429                              → OpenAIImagesErrorRateLimit（账号需置限流）
//   - HTTP 5xx 与网络错                     → OpenAIImagesErrorTransient（允许重试，跨通路共享 retry budget）
//   - 其它 4xx                              → OpenAIImagesErrorPermanent（调用方决定是否重试）
func classifyOpenAIImagesError(status int, body []byte) error {
	message, code := parseOpenAIImagesErrorBody(body)
	switch {
	case status == http.StatusUnauthorized,
		strings.EqualFold(code, "invalid_api_key"),
		bodyContainsInvalidAPIKey(body):
		return &OpenAIImagesError{
			Kind:    OpenAIImagesErrorAuth,
			Status:  status,
			Message: firstNonEmptyMessage(message, "invalid api key"),
			Raw:     body,
		}
	case status == http.StatusTooManyRequests:
		return &OpenAIImagesError{
			Kind:    OpenAIImagesErrorRateLimit,
			Status:  status,
			Message: firstNonEmptyMessage(message, "rate limited"),
			Raw:     body,
		}
	case status >= 500 && status < 600:
		return &OpenAIImagesError{
			Kind:    OpenAIImagesErrorTransient,
			Status:  status,
			Message: firstNonEmptyMessage(message, "upstream server error"),
			Raw:     body,
		}
	default:
		return &OpenAIImagesError{
			Kind:    OpenAIImagesErrorPermanent,
			Status:  status,
			Message: firstNonEmptyMessage(message, "openai images request failed"),
			Raw:     body,
		}
	}
}

// --- internal helpers ---

const (
	openaiImagesGenerationsPath        = "/v1/images/generations"
	openaiImagesEditsPath              = "/v1/images/edits"
	openaiImagesChatCompletionsPath    = "/v1/chat/completions"
	openaiImagesResponseFormatB64JSON  = "b64_json"
)

// openaiImagesGenerationDTO 是发送给 /v1/images/generations 的请求体 DTO。
// 仅按当前 OpenAI Images API 字段集编码，不写多版本兼容路径。
type openaiImagesGenerationDTO struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`
	ResponseFormat string `json:"response_format"`
	OutputFormat   string `json:"output_format,omitempty"`
}

// openaiImagesResponseDTO 是 /v1/images/generations 与 /v1/images/edits 的
// 响应体 DTO。
type openaiImagesResponseDTO struct {
	Created int64 `json:"created"`
	Data    []struct {
		B64JSON       string `json:"b64_json,omitempty"`
		URL           string `json:"url,omitempty"`
		RevisedPrompt string `json:"revised_prompt,omitempty"`
	} `json:"data"`
}

func (d *openaiImagesResponseDTO) toResult() *OpenAIImagesResult {
	out := &OpenAIImagesResult{Data: make([]OpenAIImageDatum, 0, len(d.Data))}
	for _, datum := range d.Data {
		out.Data = append(out.Data, OpenAIImageDatum{
			B64JSON:       datum.B64JSON,
			URL:           datum.URL,
			RevisedPrompt: datum.RevisedPrompt,
		})
	}
	return out
}

func (c *OpenAIImagesClient) validate() error {
	if c == nil {
		return errors.New("openai images client is nil")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("base_url is required")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		return errors.New("api_key is required")
	}
	return nil
}

func (req OpenAIImagesRequest) validateGenerate() error {
	if strings.TrimSpace(req.UpstreamModel) == "" {
		return errors.New("upstream model is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return errors.New("prompt is required")
	}
	return nil
}

func (req OpenAIImagesRequest) validateEdit() error {
	if err := req.validateGenerate(); err != nil {
		return err
	}
	if len(req.InputImages) == 0 {
		return errors.New("input images are required for edit")
	}
	for index, image := range req.InputImages {
		if len(image.Data) == 0 {
			return fmt.Errorf("input image %d is empty", index)
		}
	}
	if req.InputImageMask != nil && len(req.InputImageMask.Data) == 0 {
		return errors.New("input image mask is empty")
	}
	return nil
}

func (c *OpenAIImagesClient) execute(req *http.Request) (*OpenAIImagesResult, error) {
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		// 把 net/http 客户端 timeout（含 "context deadline exceeded" /
		// "Client.Timeout exceeded"）归为 OpenAIImagesErrorTimeout，让
		// Engine 把它当成"限流同类"——切下一账号，但不占用跨通路 3 次
		// transient 重试预算（参见 OpenAIImagesErrorKind 注释）。
		// 其余网络错误（DNS / 连接被拒绝等）维持 transient 语义。
		kind := OpenAIImagesErrorTransient
		if isHTTPTimeoutError(err) {
			kind = OpenAIImagesErrorTimeout
		}
		return nil, &OpenAIImagesError{
			Kind:    kind,
			Status:  0,
			Message: err.Error(),
		}
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, &OpenAIImagesError{
			Kind:    OpenAIImagesErrorTransient,
			Status:  resp.StatusCode,
			Message: fmt.Sprintf("read response body: %v", readErr),
			Raw:     body,
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, classifyOpenAIImagesError(resp.StatusCode, body)
	}
	var dto openaiImagesResponseDTO
	if err := json.Unmarshal(body, &dto); err != nil {
		return nil, &OpenAIImagesError{
			Kind:    OpenAIImagesErrorPermanent,
			Status:  resp.StatusCode,
			Message: fmt.Sprintf("decode openai images response: %v", err),
			Raw:     body,
		}
	}
	return dto.toResult(), nil
}

// buildOpenAIImagesURL 把 baseURL 与子路径拼接为绝对 URL，处理是否带尾部斜杠。
// 仅接受 http / https scheme，其余视为非法配置。
func buildOpenAIImagesURL(baseURL, suffix string) (string, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return "", errors.New("base_url is required")
	}
	parsed, err := urlpkg.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("invalid base_url scheme %q", parsed.Scheme)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + suffix
	return parsed.String(), nil
}

// writeOpenAIImagesPart 写入一个 multipart 文件分片。fieldName 通常是 image 或
// mask；index 仅参与文件名构造，不影响表单字段名（OpenAI Images Edits 期望
// 重复使用同一字段名 image 表示多张参考图）。
func writeOpenAIImagesPart(writer *multipart.Writer, fieldName string, index int, image ResponsesInputImage) error {
	contentType := strings.TrimSpace(image.ContentType)
	if contentType == "" {
		contentType = "image/png"
	}
	filename := fmt.Sprintf("%s_%d.%s", fieldName, index, openAIImagesExtensionFromMIME(contentType))
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, fieldName, filename))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return fmt.Errorf("create multipart %s part: %w", fieldName, err)
	}
	if _, err := part.Write(image.Data); err != nil {
		return fmt.Errorf("write multipart %s part: %w", fieldName, err)
	}
	return nil
}

func openAIImagesExtensionFromMIME(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	default:
		return "png"
	}
}

func parseOpenAIImagesErrorBody(body []byte) (message, code string) {
	if len(body) == 0 {
		return "", ""
	}
	var dto struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &dto); err != nil {
		return "", ""
	}
	return strings.TrimSpace(dto.Error.Message), strings.TrimSpace(dto.Error.Code)
}

func bodyContainsInvalidAPIKey(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	return strings.Contains(strings.ToLower(string(body)), "invalid_api_key")
}

func firstNonEmptyMessage(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// isHTTPTimeoutError 判断是否是 net/http 客户端层 Timeout（包括
// `context deadline exceeded while awaiting headers` / `Client.Timeout exceeded`）。
// 优先用 net.Error.Timeout()，兜底匹配 context.DeadlineExceeded 与字符串特征。
func isHTTPTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "client.timeout exceeded") ||
		strings.Contains(msg, "i/o timeout")
}
