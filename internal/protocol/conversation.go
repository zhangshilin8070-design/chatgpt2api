package protocol

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"chatgpt2api/internal/backend"
	"chatgpt2api/internal/service"
	"chatgpt2api/internal/storage"
	"chatgpt2api/internal/util"

	"github.com/HugoSmits86/nativewebp"
)

type ImageConfig interface {
	ImagesDir() string
	ImageMetadataDir() string
	BaseURL() string
}

// OpenAIAccountReserver 抽象 OpenAI 协议账号池的调度行为，由
// service.OpenAIAccountService 实现。Image_Engine 不直接依赖具体类型，便于
// 单元测试与后续替换。
type OpenAIAccountReserver interface {
	ReserveForUpstreamModel(upstreamModel string, exclude map[string]struct{}) (service.OpenAIAccountReservation, error)
	Release(accountID string)
	HasAvailableForUpstreamModel(upstreamModel string) bool
	MarkModelResult(accountID, upstreamModel string, success bool, errMessage string)
	UpdateModelState(accountID, upstreamModel string, patch map[string]any) (map[string]any, error)
}

// BillingChecker 仅暴露 Image_Engine 所需的余额预检方法，避免引入 BillingService
// 的全部 API 表面。bucket 必须是 util.ImageBucketA 或 util.ImageBucketB。
type BillingChecker interface {
	CheckAvailable(identity service.Identity, amount int, bucket string) error
}

// AutoRouteResolver 把对外模型 "auto" 解析为具体的对外模型与桶，由 Image_Engine
// 在请求入口、扣费之前调用。
//
// 当 originalModel 为 "auto" 或空字符串时，按 Requirement 5 解析；当
// originalModel 是已知的对外模型（gpt-image-2 / codex-gpt-image-2 /
// gemini-3.1-flash-image）时直接通过 util.BucketForModel 校验并返回对应桶。
// 其它取值返回错误。n 是请求中的图像张数，用于桶余额预检（Auto 路径
// 内部调用 BillingChecker.CheckAvailable 时使用）。
type AutoRouteResolver interface {
	Resolve(identity service.Identity, originalModel string, n int) (resolvedExternalModel string, bucket string, err error)
}

// OpenAIImageBackendFactory 根据账号预留构造 OpenAI 协议出图客户端，由 main 注入。
type OpenAIImageBackendFactory func(reservation service.OpenAIAccountReservation) *backend.OpenAIImagesClient

type Engine struct {
	Accounts     *service.AccountService
	Config       ImageConfig
	Storage      storage.JSONDocumentBackend
	Proxy        *service.ProxyService
	Logger       *service.Logger
	CloudStorage *service.CloudStorageService

	ListModelsFunc         func(context.Context) (map[string]any, error)
	StreamImageOutputsFunc func(context.Context, *backend.Client, ConversationRequest, int, int) (<-chan ImageOutput, <-chan error)
	ImageTokenProvider     func(context.Context) (string, error)
	ImageClientFactory     func(string) *backend.Client

	responseContextMu sync.Mutex
	ResponseContexts  *ResponseContextStore

	OpenAIImageBackendFactory OpenAIImageBackendFactory
	OpenAIAccountReserver     OpenAIAccountReserver
	BillingChecker            BillingChecker
	AutoRouteResolver         AutoRouteResolver
}

type ImageOutputSlotAcquirer func(context.Context, int) (func(), error)

// ImageOutputCharger atomically reserves/deducts billing for a single image
// output before it is persisted. It returns a non-nil error to deny saving
// this image. Denials backed by service.BillingLimitError are surfaced to the
// caller as the request-level error.
type ImageOutputCharger func(index int) error

type ConversationRequest struct {
	Model             string
	ResolvedModel     string
	Bucket            string
	Prompt            string
	Messages          []map[string]any
	Images            []string
	InputImageMask    string
	N                 int
	Size              string
	Quality           string
	Background        string
	Moderation        string
	Style             string
	OutputFormat      string
	OutputCompression *int
	PartialImages     *int
	Watermark         string // 透传给 OpenAI Images API（IMAGE-API.local.md §4 扩展字段）
	InputFidelity     string // edits 专属，透传给 OpenAI Images API
	ResponseFormat    string
	BaseURL           string
	OwnerID           string
	OwnerName         string
	MessageAsError    bool
	AcquireImageOutputSlot ImageOutputSlotAcquirer
	ChargeImageOutput      ImageOutputCharger
}

func (r ConversationRequest) Normalized() ConversationRequest {
	r.Size = NormalizeImageGenerationSize(r.Size)
	r.Quality = ImageQualityForModel(r.Model, r.Quality)
	r.OutputFormat = NormalizeImageOutputFormat(r.OutputFormat)
	if !SupportsImageOutputCompression(r.OutputFormat) {
		r.OutputCompression = nil
	} else if r.OutputCompression != nil {
		compression := *r.OutputCompression
		if compression < 0 {
			compression = 0
		} else if compression > 100 {
			compression = 100
		}
		r.OutputCompression = &compression
	}
	return r
}

func ImageQualityForModel(model, quality string) string {
	if strings.TrimSpace(model) == util.ImageModelCodexGPTImage2 {
		return ""
	}
	return strings.TrimSpace(quality)
}

func NormalizeImageOutputFormat(format string) string {
	return service.NormalizeImageOutputFormat(format)
}

func SupportsImageOutputCompression(format string) bool {
	return NormalizeImageOutputFormat(format) == "jpeg"
}

type ImageOutputOptions struct {
	Format              string
	Compression         *int
	TrustUpstreamFormat bool
}

type ImageToolOptions struct {
	Background     string
	Moderation     string
	Style          string
	Watermark      string // 透传给 OpenAI Images API（gpt-image 系扩展字段）
	InputFidelity  string // edits 专属
	PartialImages  *int
	InputImageMask string
}

func ImageOutputOptionsFromPayload(payload map[string]any) ImageOutputOptions {
	format := NormalizeImageOutputFormat(util.Clean(payload["output_format"]))
	options := ImageOutputOptions{Format: format}
	if !SupportsImageOutputCompression(format) {
		return options
	}
	if compression, ok := normalizedImageOutputCompression(payload["output_compression"]); ok {
		options.Compression = &compression
	}
	return options
}

func ImageToolOptionsFromPayload(payload map[string]any) ImageToolOptions {
	options := ImageToolOptions{
		Background:     util.Clean(payload["background"]),
		Moderation:     util.Clean(payload["moderation"]),
		Style:          util.Clean(payload["style"]),
		Watermark:      util.Clean(payload["watermark"]),
		InputFidelity:  util.Clean(payload["input_fidelity"]),
		InputImageMask: responseImageMask(payload["input_image_mask"]),
	}
	if partialImages, ok := normalizedPositiveInt(payload["partial_images"]); ok {
		options.PartialImages = &partialImages
	}
	return options
}

func normalizedImageOutputCompression(value any) (int, bool) {
	if value == nil || strings.TrimSpace(util.Clean(value)) == "" {
		return 0, false
	}
	compression := util.ToInt(value, -1)
	if compression < 0 {
		return 0, false
	}
	if compression > 100 {
		compression = 100
	}
	return compression, true
}

func (r ConversationRequest) SupportsImageGenerationModel() bool {
	return util.IsImageGenerationModel(r.Model)
}

func (r ConversationRequest) UsesResponsesImageRoute() bool {
	model := strings.TrimSpace(r.Model)
	return model == "" || model == util.ImageModelAuto || model == util.ImageModelGPTImage2 || model == util.ImageModelCodexGPTImage2
}

type ConversationState struct {
	Text           string
	ConversationID string
	FileIDs        []string
	SedimentIDs    []string
	Blocked        bool
	ToolInvoked    *bool
	TurnUseCase    string
}

type ConversationEvent map[string]any

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
	// Raw 承载与具体物理上游通路相关的元信息，例如 upstream_kind /
	// revised_prompt。下游审计、任务记录可读取该字段，但不应将其作为协议
	// 兼容点；当前仅写 upstream_kind。
	Raw map[string]any
}

type ImageOutputProgressCallback func([]map[string]any)

type indexedImageOutputData struct {
	index int
	data  []map[string]any
}

type ImageGenerationError struct {
	Message    string
	StatusCode int
	Type       string
	Code       string
	Param      any
}

type imageRunResult struct {
	emitted         bool
	returnedMessage bool
	lastError       string
	err             error
}

func (e *ImageGenerationError) Error() string { return e.Message }

func (e *ImageGenerationError) OpenAIError() map[string]any {
	return map[string]any{"error": map[string]any{"message": e.Message, "type": e.Type, "param": e.Param, "code": e.Code}}
}

func NewImageGenerationError(message string) *ImageGenerationError {
	return &ImageGenerationError{Message: message, StatusCode: 502, Type: "server_error", Code: "upstream_error"}
}

const maxTransientImageStreamAttempts = 3

func isTransientImageStreamErrorMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, strings.ToLower(util.UpstreamConnectionFailureMessage)) {
		return true
	}
	if _, ok := util.SummarizeUpstreamConnectionError(lower); ok {
		return true
	}
	for _, token := range []string{
		"sse read error",
		"responses sse read error",
		"stream error",
		"flow_control_error",
		"internal_error",
		"received from peer",
		"unexpected eof",
		"http2: client connection lost",
		"connection reset by peer",
		"stream closed",
	} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func imageStreamErrorMessage(message string) string {
	text := strings.TrimSpace(message)
	lower := strings.ToLower(text)
	if strings.Contains(lower, "cf_chl") ||
		strings.Contains(lower, "challenge-platform") ||
		strings.Contains(lower, "enable javascript and cookies to continue") ||
		strings.Contains(lower, "cloudflare challenge") {
		return "upstream returned Cloudflare challenge page; refresh browser fingerprint/session or change proxy"
	}
	if detail, ok := util.SummarizeUpstreamConnectionError(text); ok {
		return detail
	}
	if strings.Contains(lower, "flow_control_error") {
		return "upstream image stream interrupted by HTTP/2 flow control; retry the request or change proxy if it repeats"
	}
	if isCodexResponsesUnauthorizedErrorMessage(lower) {
		return "codex-gpt-image-2 需要 Plus / Team / Pro 账号；Free 账号无权访问 Codex 图片接口"
	}
	if text == "" {
		return "upstream image request failed without error detail"
	}
	return text
}

func isCodexResponsesUnauthorizedErrorMessage(message string) bool {
	return strings.Contains(message, "/backend-api/codex/responses failed: status=401") &&
		strings.Contains(message, "unauthorized")
}

func (o ImageOutput) Chunk() map[string]any {
	chunk := map[string]any{
		"object":              "image.generation.chunk",
		"created":             o.Created,
		"model":               o.Model,
		"index":               o.Index,
		"total":               o.Total,
		"progress_text":       o.Text,
		"upstream_event_type": o.UpstreamEventType,
		"data":                []map[string]any{},
	}
	switch o.Kind {
	case "message":
		chunk["object"] = "image.generation.message"
		chunk["message"] = o.Text
		delete(chunk, "progress_text")
		delete(chunk, "upstream_event_type")
	case "result":
		chunk["object"] = "image.generation.result"
		chunk["data"] = o.Data
		delete(chunk, "progress_text")
		delete(chunk, "upstream_event_type")
	}
	return chunk
}

func (e *Engine) TextBackend(accessToken string) *backend.Client {
	return backend.NewClient(accessToken, e.Accounts, e.Proxy)
}

func (e *Engine) ListModels(ctx context.Context) (map[string]any, error) {
	result, err := e.listModels(ctx)
	if err != nil {
		return nil, err
	}
	data := util.AsMapSlice(result["data"])
	seen := map[string]struct{}{}
	for _, item := range data {
		if id := util.Clean(item["id"]); id != "" {
			seen[id] = struct{}{}
		}
	}
	for _, model := range util.ModelList() {
		if _, ok := seen[model]; !ok {
			data = append(data, map[string]any{"id": model, "object": "model", "created": 0, "owned_by": "chatgpt2api", "permission": []any{}, "root": model, "parent": nil})
		}
	}
	result["data"] = data
	return result, nil
}

func (e *Engine) listModels(ctx context.Context) (map[string]any, error) {
	if e != nil && e.ListModelsFunc != nil {
		return e.ListModelsFunc(ctx)
	}
	return backend.NewClient("", e.Accounts, e.Proxy).ListModels(ctx)
}

func (e *Engine) StreamTextDeltas(ctx context.Context, client *backend.Client, request ConversationRequest) (<-chan string, <-chan error) {
	out := make(chan string)
	errCh := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errCh)
		events, convErr := e.ConversationEvents(ctx, client, request.Messages, request.Model, request.Prompt)
		for event := range events {
			if event["type"] != "conversation.delta" {
				continue
			}
			delta := util.Clean(event["delta"])
			if delta == "" {
				continue
			}
			select {
			case out <- delta:
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			}
		}
		if err := <-convErr; err != nil {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	return out, errCh
}

func (e *Engine) CollectText(ctx context.Context, client *backend.Client, request ConversationRequest) (string, error) {
	deltas, errCh := e.StreamTextDeltas(ctx, client, request)
	var parts []string
	for delta := range deltas {
		parts = append(parts, delta)
	}
	return strings.Join(parts, ""), <-errCh
}

func (e *Engine) CollectVisionText(ctx context.Context, client *backend.Client, messages []map[string]any, model string, images []backend.VisionImage) (string, error) {
	deltas, errCh := client.StreamMultimodalConversation(ctx, messages, model, images)
	var parts []string
	for delta := range deltas {
		parts = append(parts, delta)
	}
	return strings.Join(parts, ""), <-errCh
}

func (e *Engine) ConversationEvents(ctx context.Context, client *backend.Client, messages []map[string]any, model, prompt string) (<-chan ConversationEvent, <-chan error) {
	out := make(chan ConversationEvent)
	errCh := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errCh)
		normalized := NormalizeMessages(messages, nil)
		if len(normalized) == 0 && prompt != "" {
			normalized = []map[string]any{{"role": "user", "content": prompt}}
		}
		historyText := AssistantHistoryText(normalized)
		historyMessages := AssistantHistoryMessages(normalized)
		payloads, upstreamErr := client.StreamConversation(ctx, normalized, model, prompt)
		iterErr := IterConversationPayloads(ctx, payloads, historyText, historyMessages, out)
		upErr := <-upstreamErr
		if iterErr != nil {
			errCh <- iterErr
			return
		}
		errCh <- upErr
	}()
	return out, errCh
}

func IterConversationPayloads(ctx context.Context, payloads <-chan string, historyText string, historyMessages []string, out chan<- ConversationEvent) error {
	state := &ConversationState{}
	historyIndex := 0
	for payload := range payloads {
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			event := conversationBaseEvent("conversation.done", state)
			event["done"] = true
			select {
			case out <- event:
			case <-ctx.Done():
				return ctx.Err()
			}
			break
		}
		var raw any
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			UpdateConversationState(state, payload, nil)
			event := conversationBaseEvent("conversation.raw", state)
			event["payload"] = payload
			out <- event
			continue
		}
		eventMap, ok := raw.(map[string]any)
		if !ok {
			event := conversationBaseEvent("conversation.event", state)
			event["raw"] = raw
			out <- event
			continue
		}
		UpdateConversationState(state, payload, eventMap)
		if historyIndex < len(historyMessages) && EventAssistantText(eventMap, historyText) == historyMessages[historyIndex] {
			historyIndex++
			state.Text = ""
			continue
		}
		nextText := AssistantText(eventMap, state.Text, historyText)
		if nextText != state.Text {
			delta := nextText
			if strings.HasPrefix(nextText, state.Text) {
				delta = nextText[len(state.Text):]
			}
			state.Text = nextText
			event := conversationBaseEvent("conversation.delta", state)
			event["raw"] = eventMap
			event["delta"] = delta
			out <- event
			continue
		}
		event := conversationBaseEvent("conversation.event", state)
		event["raw"] = eventMap
		out <- event
	}
	return nil
}

func (e *Engine) StreamImageOutputsWithPool(ctx context.Context, request ConversationRequest) (<-chan ImageOutput, <-chan error) {
	request = request.Normalized()
	out := make(chan ImageOutput)
	errCh := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errCh)
		if !request.SupportsImageGenerationModel() {
			errCh <- &ImageGenerationError{Message: "unsupported image model,supported models: " + util.ImageGenerationModelNames(), StatusCode: 502, Type: "server_error", Code: "upstream_error"}
			return
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		resultCh := make(chan imageRunResult, request.N)
		var wg sync.WaitGroup
		for index := 1; index <= request.N; index++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				releaseSlot, err := request.acquireImageOutputSlot(ctx, index)
				if err != nil {
					cancel()
					resultCh <- imageRunResult{lastError: err.Error(), err: err}
					return
				}
				defer releaseSlot()
				result := e.runSingleImageOutput(ctx, out, request, index)
				if result.err != nil {
					cancel()
				}
				resultCh <- result
			}(index)
		}
		go func() {
			wg.Wait()
			close(resultCh)
		}()

		emittedAny := false
		messageOnly := false
		lastError := ""
		for result := range resultCh {
			emittedAny = emittedAny || result.emitted
			messageOnly = messageOnly || result.returnedMessage
			if result.lastError != "" {
				lastError = result.lastError
			}
			if result.err != nil {
				errCh <- result.err
				return
			}
		}
		if messageOnly {
			errCh <- nil
			return
		}
		if !emittedAny {
			errCh <- NewImageGenerationError(imageStreamErrorMessage(lastError))
			return
		}
		errCh <- nil
	}()
	return out, errCh
}

func (r ConversationRequest) acquireImageOutputSlot(ctx context.Context, index int) (func(), error) {
	if r.AcquireImageOutputSlot == nil {
		return noopImageOutputSlotRelease, nil
	}
	release, err := r.AcquireImageOutputSlot(ctx, index)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return noopImageOutputSlotRelease, nil
	}
	return release, nil
}

func noopImageOutputSlotRelease() {}

// imageCandidateOutcome 标识一次物理上游通路尝试的退出原因。
type imageCandidateOutcome int

const (
	// candidateOutcomeSuccess 表示通路执行完成且不应再尝试后续候选；
	// 包括成功产出图片，以及上游合法地以纯文本响应代替图片的情况。
	candidateOutcomeSuccess imageCandidateOutcome = iota
	// candidateOutcomeFatal 表示遇到不可重试的终止性错误，必须立即返回。
	candidateOutcomeFatal
	// candidateOutcomeExhausted 表示当前通路上没有可调度账号，应跳到下
	// 一个候选通路；不消耗 transient 重试预算。
	candidateOutcomeExhausted
)

func (e *Engine) runSingleImageOutput(ctx context.Context, out chan<- ImageOutput, request ConversationRequest, index int) imageRunResult {
	resolvedModel := strings.TrimSpace(request.ResolvedModel)
	if resolvedModel == "" {
		resolvedModel = strings.TrimSpace(request.Model)
	}
	candidates := buildUpstreamCandidates(resolvedModel)
	if len(candidates) == 0 {
		message := fmt.Sprintf("no upstream candidates configured for model %q", resolvedModel)
		return imageRunResult{
			err:       NewImageGenerationError(message),
			lastError: message,
		}
	}

	transientAttempts := 0
	merged := imageRunResult{}
	for _, candidate := range candidates {
		var (
			run     imageRunResult
			outcome imageCandidateOutcome
		)
		switch candidate.kind {
		case upstreamKindChatGPT:
			run, outcome = e.runChatGPTImageCandidate(ctx, out, request, index, &transientAttempts)
		case upstreamKindOpenAIAPI:
			run, outcome = e.runOpenAIImagesCandidate(ctx, out, request, index, candidate, &transientAttempts)
		default:
			continue
		}
		switch outcome {
		case candidateOutcomeSuccess, candidateOutcomeFatal:
			if run.emitted {
				merged.emitted = true
			}
			run.emitted = run.emitted || merged.emitted
			return run
		case candidateOutcomeExhausted:
			if run.emitted {
				merged.emitted = true
			}
			if run.lastError != "" {
				merged.lastError = run.lastError
			}
		}
	}

	merged.err = NewImageGenerationError(noUpstreamCandidateAvailableMessage(resolvedModel, merged.lastError))
	return merged
}

// noUpstreamCandidateAvailableMessage 为「全部物理通路均无可调度账号」场景
// 构造统一的对外错误消息。当上一轮收集到具体的 lastError 时优先携带。
func noUpstreamCandidateAvailableMessage(resolvedModel, lastError string) string {
	switch resolvedModel {
	case util.ImageModelGeminiFlashImage:
		if lastError != "" {
			return fmt.Sprintf("no available openai-protocol account for %s: %s", util.ImageModelGeminiFlashImage, lastError)
		}
		return fmt.Sprintf("no available openai-protocol account for %s", util.ImageModelGeminiFlashImage)
	case util.ImageModelCodexGPTImage2:
		if lastError != "" {
			return fmt.Sprintf("no available upstream account for %s: %s", util.ImageModelCodexGPTImage2, lastError)
		}
		return fmt.Sprintf("no available upstream account for %s", util.ImageModelCodexGPTImage2)
	default:
		if lastError != "" {
			return imageStreamErrorMessage(lastError)
		}
		return fmt.Sprintf("no available upstream account for %s", resolvedModel)
	}
}

// runChatGPTImageCandidate 在 ChatGPT 付费账号池上轮询账号执行单次出图。
//
// 与原 runSingleImageOutput 行为等价，但：
//   - transient 重试预算来自调用方传入的共享计数器，实现跨物理通路的 3 次上限。
//   - 当 nextImageAccessToken 报告池为空（含「Plus 账号不足」误差消息）时退
//     出本通路并返回 candidateOutcomeExhausted，让上层去尝试下一个候选。
//   - 每个发出的 out 上 ImageOutput 都会带上 Raw["upstream_kind"] = chatgpt。
func (e *Engine) runChatGPTImageCandidate(ctx context.Context, out chan<- ImageOutput, request ConversationRequest, index int, transientAttempts *int) (imageRunResult, imageCandidateOutcome) {
	result := imageRunResult{}
	for {
		token, err := e.nextImageAccessToken(ctx, request)
		if err != nil {
			result.lastError = err.Error()
			return result, candidateOutcomeExhausted
		}
		emittedForToken := false
		returnedMessage := false
		returnedResult := false
		rateLimitedForToken := false
		rateLimitMessage := ""
		client := e.newImageClient(token)
		outputs, imageErr := e.StreamImageOutputs(ctx, client, request, index, request.N)
		for output := range outputs {
			if output.Kind == "message" && service.IsAccountRateLimitedErrorMessage(output.Text) {
				rateLimitedForToken = true
				rateLimitMessage = output.Text
				result.lastError = output.Text
				continue
			}
			if output.Kind == "message" && request.MessageAsError {
				if e.Accounts != nil {
					e.Accounts.MarkImageResult(token, false)
				}
				result.err = &ImageGenerationError{Message: firstNonEmpty(output.Text, "Image generation returned a text response instead of image data."), StatusCode: 400, Type: "invalid_request_error", Code: "image_generation_text_response"}
				result.lastError = result.err.Error()
				return result, candidateOutcomeFatal
			}
			if output.Kind == "result" && request.ChargeImageOutput != nil && !output.ChargeHandled {
				if err := request.ChargeImageOutput(index); err != nil {
					var billingErr service.BillingLimitError
					if errors.As(err, &billingErr) {
						result.err = billingErr
						result.lastError = billingErr.Error()
					} else {
						result.err = NewImageGenerationError(err.Error())
						result.lastError = err.Error()
					}
					return result, candidateOutcomeFatal
				}
			}
			result.emitted = true
			emittedForToken = true
			returnedMessage = output.Kind == "message"
			returnedResult = returnedResult || output.Kind == "result"
			annotateImageOutputUpstream(&output, util.UpstreamKindChatGPT)
			out <- output
		}
		err = <-imageErr
		if err == nil {
			if rateLimitedForToken {
				if e.Accounts != nil {
					e.Accounts.MarkImageResult(token, false)
					e.Accounts.ApplyAccountErrorMessage(token, "image_stream", rateLimitMessage)
				}
				continue
			}
			if returnedMessage || !returnedResult {
				if e.Accounts != nil {
					e.Accounts.MarkImageResult(token, false)
				}
				result.returnedMessage = returnedMessage || !returnedResult
				return result, candidateOutcomeSuccess
			}
			if e.Accounts != nil {
				e.Accounts.MarkImageResult(token, true)
			}
			return result, candidateOutcomeSuccess
		}
		var billingErr service.BillingLimitError
		if errors.As(err, &billingErr) {
			result.err = billingErr
			result.lastError = billingErr.Error()
			return result, candidateOutcomeFatal
		}
		if e.Accounts != nil {
			e.Accounts.MarkImageResult(token, false)
		}
		result.lastError = err.Error()
		if e.Accounts != nil {
			if normalized, handled := e.Accounts.ApplyAccountErrorMessage(token, "image_stream", result.lastError); handled {
				result.lastError = normalized
				if service.IsAccountRateLimitedErrorMessage(err.Error()) || !emittedForToken {
					continue
				}
			}
		}
		if !emittedForToken && IsTokenInvalidError(result.lastError) {
			continue
		}
		if !returnedResult && isTransientImageStreamErrorMessage(result.lastError) && *transientAttempts < maxTransientImageStreamAttempts {
			*transientAttempts++
			continue
		}
		result.err = NewImageGenerationError(imageStreamErrorMessage(result.lastError))
		return result, candidateOutcomeFatal
	}
}

// runOpenAIImagesCandidate 在 OpenAI 协议账号池上轮询账号执行单次出图。
//
// 行为约束（参见 Requirements 4A / 4B / 11）：
//   - 401 / invalid_api_key  ：把账号-模型置「异常」，换下一账号，不占 transient 名额。
//   - 429                    ：把账号-模型置「限流」，换下一账号，不占 transient 名额。
//   - 5xx / 网络错误         ：仅累加 fail 计数，占用一次共享 transient 预算后换下一账号。
//   - 其它 4xx              ：永久错误，立即返回 candidateOutcomeFatal。
//   - Reserve 失败           ：当前通路无更多候选账号，返回 candidateOutcomeExhausted。
func (e *Engine) runOpenAIImagesCandidate(ctx context.Context, out chan<- ImageOutput, request ConversationRequest, index int, candidate upstreamCandidate, transientAttempts *int) (imageRunResult, imageCandidateOutcome) {
	result := imageRunResult{}
	if e.OpenAIAccountReserver == nil || e.OpenAIImageBackendFactory == nil {
		return result, candidateOutcomeExhausted
	}
	excluded := map[string]struct{}{}
	for {
		reservation, err := e.OpenAIAccountReserver.ReserveForUpstreamModel(candidate.upstreamModel, excluded)
		if err != nil {
			if result.lastError == "" {
				result.lastError = err.Error()
			}
			return result, candidateOutcomeExhausted
		}
		excluded[reservation.AccountID] = struct{}{}
		run, outcome, transient := e.executeOpenAIImagesAttempt(ctx, out, request, index, candidate, reservation)
		e.OpenAIAccountReserver.Release(reservation.AccountID)

		if run.emitted {
			result.emitted = true
		}
		if run.lastError != "" {
			result.lastError = run.lastError
		}
		switch outcome {
		case candidateOutcomeSuccess, candidateOutcomeFatal:
			run.emitted = run.emitted || result.emitted
			return run, outcome
		}
		// outcome == candidateOutcomeExhausted: 该账号失败但允许尝试下一账号。
		// 仅 5xx / 网络错误（transient）消耗共享重试预算；超过 3 次直接终止。
		if transient {
			*transientAttempts++
			if *transientAttempts >= maxTransientImageStreamAttempts {
				result.err = NewImageGenerationError(imageStreamErrorMessage(result.lastError))
				return result, candidateOutcomeFatal
			}
		}
	}
}

// executeOpenAIImagesAttempt 用单个 OpenAIAccountReservation 发起一次 OpenAI
// 协议生图调用并把结果转成 protocol.ImageOutput 透传到 out。
//
// 返回值约定：
//   - imageRunResult.lastError 始终携带最后一次错误描述（成功时为空）以便上层归并。
//   - candidateOutcomeSuccess / Fatal / Exhausted 与 runChatGPTImageCandidate 一致。
//   - 第三个返回值 transient = true 表示本次失败应消耗一次共享 transient 预算。
//     由调用方 runOpenAIImagesCandidate 累加；其他错误类（auth / rate-limit）不消耗预算。
func (e *Engine) executeOpenAIImagesAttempt(ctx context.Context, out chan<- ImageOutput, request ConversationRequest, index int, candidate upstreamCandidate, reservation service.OpenAIAccountReservation) (imageRunResult, imageCandidateOutcome, bool) {
	result := imageRunResult{}
	client := e.OpenAIImageBackendFactory(reservation)
	if client == nil {
		result.lastError = "openai images backend not configured"
		return result, candidateOutcomeExhausted, false
	}

	apiReq := backend.OpenAIImagesRequest{
		UpstreamModel:     candidate.upstreamModel,
		Prompt:            request.Prompt,
		N:                 1,
		Size:              request.Size,
		OutputFormat:      request.OutputFormat,
		OutputCompression: request.OutputCompression,
		Quality:           request.Quality,
		Background:        request.Background,
		Moderation:        request.Moderation,
		Watermark:         request.Watermark,
		InputFidelity:     request.InputFidelity,
		ResponseFormat:    request.ResponseFormat,
	}
	apiReq.InputImages = responsesInputImages(request.Images)
	apiReq.InputImageMask = responsesInputImagePtr(request.InputImageMask)

	var (
		upstreamResult *backend.OpenAIImagesResult
		callErr        error
	)
	// gemini-3.1-flash-image 在聚合服务（newapi 等）上不在
	// /v1/images/generations 白名单内，必须走 /v1/chat/completions 多模态接口，
	// 见 backend.GenerateViaChat 注释。其余上游模型（gpt-image-2 等）继续
	// 用标准 OpenAI Images API。
	useChatAPI := candidate.upstreamModel == util.ImageModelGeminiFlashImage
	switch {
	case useChatAPI && len(apiReq.InputImages) > 0:
		upstreamResult, callErr = client.EditViaChat(ctx, apiReq)
	case useChatAPI:
		upstreamResult, callErr = client.GenerateViaChat(ctx, apiReq)
	case len(apiReq.InputImages) > 0:
		upstreamResult, callErr = client.Edit(ctx, apiReq)
	default:
		upstreamResult, callErr = client.Generate(ctx, apiReq)
	}
	if callErr != nil {
		errResult, outcome, transient := e.handleOpenAIImagesError(callErr, candidate, reservation)
		return errResult, outcome, transient
	}

	backendOutputs := upstreamResult.ToImageOutputs(request.Model, index, request.N, util.UpstreamKindOpenAIAPI)
	if e != nil && e.Logger != nil {
		// 上游入参与返回字段并列记录，便于排查"size=4k 但实际拿到 2k 图"等
		// 与上游响应一致性相关的问题（IMAGE-API.local.md §6：4k 是计价档，
		// 实际像素由上游模型决定，可能仍为 2048）。
		hasB64 := false
		hasURL := false
		hasRevised := false
		if len(backendOutputs) > 0 && len(backendOutputs[0].Data) > 0 {
			datum := backendOutputs[0].Data[0]
			if v := util.Clean(datum["b64_json"]); v != "" {
				hasB64 = true
			}
			if v := util.Clean(datum["url"]); v != "" {
				hasURL = true
			}
			if v := util.Clean(datum["revised_prompt"]); v != "" {
				hasRevised = true
			}
		}
		e.Logger.Info("openai_images upstream returned",
			"upstream_model", candidate.upstreamModel,
			"size", apiReq.Size,
			"output_format", apiReq.OutputFormat,
			"n", len(backendOutputs),
			"has_b64", hasB64,
			"has_url", hasURL,
			"has_revised_prompt", hasRevised,
		)
	}
	if len(backendOutputs) == 0 {
		message := "openai images upstream returned no image data"
		e.OpenAIAccountReserver.MarkModelResult(reservation.AccountID, candidate.upstreamModel, false, message)
		result.lastError = message
		return result, candidateOutcomeExhausted, false
	}

	for _, backendOutput := range backendOutputs {
		chargeHandled := false
		formatted, err := e.FormatImageResultWithCharge(
			backendOutput.Data,
			request.Prompt,
			request.ResponseFormat,
			request.BaseURL,
			request.OwnerID,
			request.OwnerName,
			backendOutput.Created,
			"",
			ImageOutputOptions{Format: request.OutputFormat, Compression: request.OutputCompression},
			func() error {
				if request.ChargeImageOutput == nil {
					return nil
				}
				if err := request.ChargeImageOutput(index); err != nil {
					return err
				}
				chargeHandled = true
				return nil
			},
		)
		if err != nil {
			var billingErr service.BillingLimitError
			if errors.As(err, &billingErr) {
				result.err = billingErr
				result.lastError = billingErr.Error()
			} else {
				result.err = NewImageGenerationError(err.Error())
				result.lastError = err.Error()
			}
			e.OpenAIAccountReserver.MarkModelResult(reservation.AccountID, candidate.upstreamModel, false, "charge denied: "+err.Error())
			return result, candidateOutcomeFatal, false
		}
		data := util.AsMapSlice(formatted["data"])
		if len(data) == 0 {
			message := "openai images upstream returned undecodable image data"
			e.OpenAIAccountReserver.MarkModelResult(reservation.AccountID, candidate.upstreamModel, false, message)
			result.lastError = message
			return result, candidateOutcomeExhausted, false
		}
		emitted := ImageOutput{
			Kind:          "result",
			Model:         request.Model,
			Index:         backendOutput.Index,
			Total:         backendOutput.Total,
			Created:       backendOutput.Created,
			Data:          data,
			ChargeHandled: chargeHandled,
		}
		annotateImageOutputUpstream(&emitted, util.UpstreamKindOpenAIAPI)
		result.emitted = true
		out <- emitted
	}
	e.OpenAIAccountReserver.MarkModelResult(reservation.AccountID, candidate.upstreamModel, true, "")
	return result, candidateOutcomeSuccess, false
}

// handleOpenAIImagesError 把上游错误归类映射为账号-模型状态变更与候选层退出动作。
//
// 第三个返回值表示本次失败是否应消耗一次共享 transient 重试预算（仅 5xx /
// 网络错误为 true），由调用方 runOpenAIImagesCandidate 累加。
func (e *Engine) handleOpenAIImagesError(callErr error, candidate upstreamCandidate, reservation service.OpenAIAccountReservation) (imageRunResult, imageCandidateOutcome, bool) {
	result := imageRunResult{}
	result.lastError = callErr.Error()
	var typed *backend.OpenAIImagesError
	if !errors.As(callErr, &typed) {
		// 非预期错误：保守归类为永久错误，立即上抛。
		e.OpenAIAccountReserver.MarkModelResult(reservation.AccountID, candidate.upstreamModel, false, "permanent: "+callErr.Error())
		result.err = NewImageGenerationError(callErr.Error())
		return result, candidateOutcomeFatal, false
	}
	switch typed.Kind {
	case backend.OpenAIImagesErrorAuth:
		_, _ = e.OpenAIAccountReserver.UpdateModelState(reservation.AccountID, candidate.upstreamModel, map[string]any{
			"status":        openAIAccountModelStatusError,
			"error_message": "invalid_api_key: " + typed.Message,
		})
		e.OpenAIAccountReserver.MarkModelResult(reservation.AccountID, candidate.upstreamModel, false, "invalid_api_key: "+typed.Message)
		return result, candidateOutcomeExhausted, false
	case backend.OpenAIImagesErrorRateLimit:
		_, _ = e.OpenAIAccountReserver.UpdateModelState(reservation.AccountID, candidate.upstreamModel, map[string]any{
			"status":        openAIAccountModelStatusRateLimited,
			"error_message": "rate_limited: " + typed.Message,
		})
		e.OpenAIAccountReserver.MarkModelResult(reservation.AccountID, candidate.upstreamModel, false, "rate_limited: "+typed.Message)
		return result, candidateOutcomeExhausted, false
	case backend.OpenAIImagesErrorTimeout:
		// 客户端 timeout 与限流同类：标记限流并换下一账号，但不占
		// transient 重试预算（参见 OpenAIImagesErrorKind 与
		// runOpenAIImagesCandidate 注释）。
		_, _ = e.OpenAIAccountReserver.UpdateModelState(reservation.AccountID, candidate.upstreamModel, map[string]any{
			"status":        openAIAccountModelStatusRateLimited,
			"error_message": "timeout: " + typed.Message,
		})
		e.OpenAIAccountReserver.MarkModelResult(reservation.AccountID, candidate.upstreamModel, false, "timeout: "+typed.Message)
		return result, candidateOutcomeExhausted, false
	case backend.OpenAIImagesErrorTransient:
		e.OpenAIAccountReserver.MarkModelResult(reservation.AccountID, candidate.upstreamModel, false, "transient: "+typed.Message)
		return result, candidateOutcomeExhausted, true
	default:
		e.OpenAIAccountReserver.MarkModelResult(reservation.AccountID, candidate.upstreamModel, false, "permanent: "+typed.Message)
		result.err = NewImageGenerationError(typed.Error())
		return result, candidateOutcomeFatal, false
	}
}

// annotateImageOutputUpstream 把物理上游通路标识写入 ImageOutput.Raw。
//
// Raw 在多数 ChatGPT 通路输出上之前并不存在，这里按需懒初始化并写入
// upstream_kind。已存在的 Raw 字段保持不变。
func annotateImageOutputUpstream(output *ImageOutput, upstreamKind string) {
	if output == nil || strings.TrimSpace(upstreamKind) == "" {
		return
	}
	if output.Raw == nil {
		output.Raw = map[string]any{}
	}
	output.Raw["upstream_kind"] = upstreamKind
}

// 模型粒度状态字面量。protocol 包不依赖 service 包的内部常量，这里就近定义，
// 与 service.OpenAIAccountService.UpdateModelState 接受的取值对齐。
const (
	openAIAccountModelStatusError       = "异常"
	openAIAccountModelStatusRateLimited = "限流"
)

func (e *Engine) StreamImageOutputs(ctx context.Context, client *backend.Client, request ConversationRequest, index, total int) (<-chan ImageOutput, <-chan error) {
	if e.StreamImageOutputsFunc != nil {
		return e.StreamImageOutputsFunc(ctx, client, request, index, total)
	}
	return e.StreamResponsesImageOutputs(ctx, client, request, index, total)
}

func (e *Engine) nextImageAccessToken(ctx context.Context, request ConversationRequest) (string, error) {
	if e.ImageTokenProvider != nil {
		return e.ImageTokenProvider(ctx)
	}
	allow := imageAccountAllowFunc(request)
	if allow != nil && e.Accounts != nil && !e.Accounts.HasAvailableMatchingAccount(allow) {
		return "", fmt.Errorf("账号池中没有可用的 Plus / ProLite / Pro / Team 账号，%s 仅支持付费账号，请先添加付费账号", request.Model)
	}
	return e.Accounts.GetAvailableAccessTokenFor(ctx, allow)
}

// imageAccountAllowFunc returns the account-pool filter required to serve the
// given request, or nil when any account type is acceptable. codex-gpt-image-2
// goes through /backend-api/codex/responses, which OpenAI restricts to paid
// plans, so we must avoid handing it a Free account.
func imageAccountAllowFunc(request ConversationRequest) func(map[string]any) bool {
	if strings.TrimSpace(request.Model) == util.ImageModelCodexGPTImage2 {
		return service.IsPaidImageAccount
	}
	return nil
}

func (e *Engine) newImageClient(token string) *backend.Client {
	if e.ImageClientFactory != nil {
		return e.ImageClientFactory(token)
	}
	return backend.NewClient(token, e.Accounts, e.Proxy)
}

func (e *Engine) StreamResponsesImageOutputs(ctx context.Context, client *backend.Client, request ConversationRequest, index, total int) (<-chan ImageOutput, <-chan error) {
	out := make(chan ImageOutput)
	errCh := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errCh)
		prompt := buildResponsesImagePrompt(request.Prompt, request.Size, request.Model)
		if strings.TrimSpace(prompt) == "" {
			prompt = request.Prompt
		}
		events, upstreamErr := client.StreamResponsesImage(ctx, backend.ResponsesImageRequest{
			Prompt:            prompt,
			Model:             request.Model,
			Size:              request.Size,
			Quality:           request.Quality,
			Background:        request.Background,
			Moderation:        request.Moderation,
			Style:             request.Style,
			OutputFormat:      request.OutputFormat,
			OutputCompression: request.OutputCompression,
			PartialImages:     request.PartialImages,
			InputImages:       responsesInputImages(request.Images),
			InputImageMask:    responsesInputImagePtr(request.InputImageMask),
		})
		emitted := false
		seen := map[string]struct{}{}
		for event := range events {
			if event.PartialImage != "" {
				out <- ImageOutput{Kind: "progress", Model: request.Model, Index: index, Total: total, Created: firstNonZeroInt64(event.Created, time.Now().Unix()), Text: event.Text, UpstreamEventType: event.Type}
				continue
			}
			if isFinalImageTextEvent(event) {
				emitted = true
				out <- ImageOutput{Kind: "message", Model: request.Model, Index: index, Total: total, Created: firstNonZeroInt64(event.Created, time.Now().Unix()), Text: strings.TrimSpace(event.Text), UpstreamEventType: event.Type}
				continue
			}
			if event.Result == "" {
				continue
			}
			key := firstNonEmpty(event.ItemID, event.Result)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			item := map[string]any{
				"b64_json":       event.Result,
				"revised_prompt": firstNonEmpty(event.RevisedPrompt, prompt),
				"output_format":  firstNonEmpty(event.OutputFormat, request.OutputFormat),
			}
			if event.Background != "" {
				item["background"] = event.Background
			}
			created := firstNonZeroInt64(event.Created, time.Now().Unix())
			chargeHandled := false
			result, err := e.FormatImageResultWithCharge([]map[string]any{item}, prompt, request.ResponseFormat, request.BaseURL, request.OwnerID, request.OwnerName, created, "", imageResultOutputOptions(request, event), func() error {
				if request.ChargeImageOutput == nil {
					return nil
				}
				if err := request.ChargeImageOutput(index); err != nil {
					return err
				}
				chargeHandled = true
				return nil
			})
			if err != nil {
				errCh <- err
				return
			}
			data := util.AsMapSlice(result["data"])
			if len(data) > 0 {
				emitted = true
				out <- ImageOutput{Kind: "result", Model: request.Model, Index: index, Total: total, Created: created, Data: data, ChargeHandled: chargeHandled}
			}
		}
		if err := <-upstreamErr; err != nil {
			errCh <- err
			return
		}
		if !emitted {
			errCh <- fmt.Errorf("upstream image stream completed without image output")
			return
		}
		errCh <- nil
	}()
	return out, errCh
}

func imageResultOutputOptions(request ConversationRequest, event backend.ResponsesImageEvent) ImageOutputOptions {
	if strings.TrimSpace(request.Model) == util.ImageModelCodexGPTImage2 {
		return ImageOutputOptions{Format: firstNonEmpty(event.OutputFormat, request.OutputFormat), TrustUpstreamFormat: true}
	}
	return ImageOutputOptions{Format: request.OutputFormat, Compression: request.OutputCompression}
}

func responsesInputImages(values []string) []backend.ResponsesInputImage {
	out := make([]backend.ResponsesInputImage, 0, len(values))
	for _, value := range values {
		image := responsesInputImage(value)
		if len(image.Data) > 0 {
			out = append(out, image)
		}
	}
	return out
}

func responsesInputImagePtr(value string) *backend.ResponsesInputImage {
	image := responsesInputImage(value)
	if len(image.Data) == 0 {
		return nil
	}
	return &image
}

func responsesInputImage(value string) backend.ResponsesInputImage {
	value = strings.TrimSpace(value)
	if value == "" {
		return backend.ResponsesInputImage{}
	}
	contentType := "image/png"
	dataPart := value
	if strings.HasPrefix(value, "data:") {
		header, data, ok := strings.Cut(value, ",")
		if ok {
			dataPart = data
			if mimeType := strings.TrimPrefix(strings.Split(header, ";")[0], "data:"); mimeType != "" {
				contentType = mimeType
			}
		}
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(dataPart))
	if err != nil {
		return backend.ResponsesInputImage{}
	}
	return backend.ResponsesInputImage{Data: data, ContentType: contentType}
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func isFinalImageTextEvent(event backend.ResponsesImageEvent) bool {
	if strings.TrimSpace(event.Text) == "" || event.Result != "" {
		return false
	}
	if event.Type == "image_text_response" {
		return true
	}
	if event.Blocked {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(event.TurnUseCase), "text") {
		return true
	}
	if responsesImageEventHasResultPointers(event) || isResponsesImageGenerationUseCase(event.TurnUseCase) {
		return false
	}
	return event.ToolInvoked != nil && !*event.ToolInvoked
}

func responsesImageEventHasResultPointers(event backend.ResponsesImageEvent) bool {
	return len(filterResponsesImageIDs(event.FileIDs)) > 0 || len(filterResponsesImageIDs(event.SedimentIDs)) > 0
}

func filterResponsesImageIDs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == "file_upload" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func isResponsesImageGenerationUseCase(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "_", " ")
	normalized = strings.ReplaceAll(normalized, "-", " ")
	normalized = strings.Join(strings.Fields(normalized), " ")
	return normalized == "image gen" || normalized == "image generation"
}

func (e *Engine) CollectImageOutputs(outputs <-chan ImageOutput, errCh <-chan error) (map[string]any, error) {
	return e.CollectImageOutputsWithProgress(outputs, errCh, nil)
}

func (e *Engine) CollectImageOutputsWithProgress(outputs <-chan ImageOutput, errCh <-chan error, onProgress ImageOutputProgressCallback) (map[string]any, error) {
	var created int64
	var results []indexedImageOutputData
	message := ""
	var progress []string
	// firstUpstreamKind 采用「第一张交付图的物理上游胜出」策略：当跨候选通路
	// 混合产出时（例如 codex-gpt-image-2 在 ChatGPT 部分成功后回落到
	// OpenAIAPI），以第一张成功交付的图所标注的 upstream_kind 作为本次任务
	// 的整体上游标识，写入审计字段。详见 _Requirements: 6.5。
	firstUpstreamKind := ""
	for output := range outputs {
		if created == 0 {
			created = output.Created
		}
		switch output.Kind {
		case "progress":
			if output.Text != "" {
				progress = append(progress, output.Text)
			}
		case "message":
			message = output.Text
		case "result":
			upstreamKind := ""
			if raw, ok := output.Raw["upstream_kind"].(string); ok {
				upstreamKind = strings.TrimSpace(raw)
			}
			if firstUpstreamKind == "" && upstreamKind != "" {
				firstUpstreamKind = upstreamKind
			}
			// 把 upstream_kind 同步写入每张图的 data 项，便于 /api/logs 与
			// /api/creation-tasks 在 detail / data 维度做更细粒度审计。
			if upstreamKind != "" {

				for _, item := range output.Data {
					if item == nil {
						continue
					}
					if existing := util.Clean(item["upstream_kind"]); existing == "" {
						item["upstream_kind"] = upstreamKind
					}
				}
			}
			results = append(results, indexedImageOutputData{index: output.Index, data: output.Data})
			if onProgress != nil {
				onProgress(indexedImageDataWithPlaceholders(results))
			}
		}
	}
	streamErr := <-errCh
	if created == 0 {
		created = time.Now().Unix()
	}
	data := denseIndexedImageData(results)
	if streamErr != nil && onProgress != nil {
		data = indexedImageDataWithPlaceholders(results)
	}
	result := map[string]any{"created": created, "data": data}
	if firstUpstreamKind != "" {
		result["upstream_kind"] = firstUpstreamKind
	}
	if len(data) == 0 {
		if text := strings.TrimSpace(message); text != "" {
			result["message"] = text
			result["output_type"] = "text"
		} else if text := strings.TrimSpace(strings.Join(progress, "")); text != "" {
			result["message"] = text
		}
	}
	if streamErr != nil {
		if imageErr, ok := streamErr.(*ImageGenerationError); ok && imageErr.Code == "image_generation_text_response" {
			result["output_type"] = "text"
		}
		if result["message"] == nil {
			result["message"] = streamErr.Error()
		}
		return result, streamErr
	}
	return result, nil
}

func denseIndexedImageData(results []indexedImageOutputData) []map[string]any {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].index == results[j].index {
			return i < j
		}
		return results[i].index < results[j].index
	})
	data := make([]map[string]any, 0)
	for _, item := range results {
		data = append(data, cloneImageOutputData(item.data)...)
	}
	return data
}

func indexedImageDataWithPlaceholders(results []indexedImageOutputData) []map[string]any {
	maxIndex := 0
	for _, item := range results {
		if item.index > maxIndex {
			maxIndex = item.index
		}
	}
	if maxIndex < 1 {
		return nil
	}
	data := make([]map[string]any, maxIndex)
	for i := range data {
		data[i] = map[string]any{}
	}
	for _, item := range results {
		if item.index < 1 || len(item.data) == 0 {
			continue
		}
		cloned := cloneImageOutputData(item.data)
		data[item.index-1] = cloned[0]
		data = append(data, cloned[1:]...)
	}
	return data
}

func cloneImageOutputData(items []map[string]any) []map[string]any {
	if len(items) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if item == nil {
			out = append(out, map[string]any{})
			continue
		}
		out = append(out, util.CopyMap(item))
	}
	return out
}

func (e *Engine) FormatImageResult(items []map[string]any, prompt, responseFormat, baseURL, ownerID, ownerName string, created int64, message string) map[string]any {
	return e.FormatImageResultWithOptions(items, prompt, responseFormat, baseURL, ownerID, ownerName, created, message, ImageOutputOptions{})
}

func (e *Engine) FormatImageResultWithOptions(items []map[string]any, prompt, responseFormat, baseURL, ownerID, ownerName string, created int64, message string, options ImageOutputOptions) map[string]any {
	result, _ := e.formatImageResultWithOptions(items, prompt, responseFormat, baseURL, ownerID, ownerName, created, message, options, nil)
	return result
}

func (e *Engine) FormatImageResultWithCharge(items []map[string]any, prompt, responseFormat, baseURL, ownerID, ownerName string, created int64, message string, options ImageOutputOptions, charge func() error) (map[string]any, error) {
	return e.formatImageResultWithOptions(items, prompt, responseFormat, baseURL, ownerID, ownerName, created, message, options, charge)
}

func (e *Engine) formatImageResultWithOptions(items []map[string]any, prompt, responseFormat, baseURL, ownerID, ownerName string, created int64, message string, options ImageOutputOptions, charge func() error) (map[string]any, error) {
	defaultFormat := NormalizeImageOutputFormat(options.Format)
	hasRequestedFormat := strings.TrimSpace(options.Format) != ""
	var data []map[string]any
	for _, item := range items {
		b64 := util.Clean(item["b64_json"])
		var imageBytes []byte
		var err error
		if b64 != "" {
			imageBytes, err = base64.StdEncoding.DecodeString(b64)
			if err != nil {
				continue
			}
		} else if remoteURL := util.Clean(item["url"]); remoteURL != "" {
			// 上游忽略 response_format=b64_json，直接返回 url 时下载本地落地。
			// 由 Engine.Proxy 提供 HTTP 客户端；下载失败该 item 跳过，由调用
			// 方在所有 item 都失败时报 undecodable。
			imageBytes, err = e.downloadImageBytes(remoteURL)
			if err != nil {
				continue
			}
		} else {
			continue
		}
		revised := firstNonEmpty(util.Clean(item["revised_prompt"]), prompt)
		itemOptions := options
		if hasRequestedFormat {
			itemOptions.Format = defaultFormat
		} else if itemFormat := strings.TrimSpace(util.Clean(item["output_format"])); itemFormat != "" {
			itemOptions.Format = NormalizeImageOutputFormat(itemFormat)
		}
		if itemOptions.Format == "" {
			itemOptions.Format = defaultFormat
		}
		if !SupportsImageOutputCompression(itemOptions.Format) {
			itemOptions.Compression = nil
		}
		if itemOptions.Compression == nil {
			if SupportsImageOutputCompression(itemOptions.Format) {
				if compression, ok := normalizedImageOutputCompression(item["output_compression"]); ok {
					itemOptions.Compression = &compression
				}
			}
		}
		if !itemOptions.TrustUpstreamFormat {
			imageBytes, err = encodeImageBytes(imageBytes, itemOptions)
			if err != nil {
				continue
			}
		}
		if charge != nil {
			if err := charge(); err != nil {
				if created == 0 {
					created = time.Now().Unix()
				}
				result := map[string]any{"created": created, "data": data}
				if message != "" && len(data) == 0 {
					result["message"] = message
				}
				return result, err
			}
		}
		outputFormat := NormalizeImageOutputFormat(itemOptions.Format)
		saved := e.SaveImageBytesForOwnerWithFormat(imageBytes, baseURL, ownerID, ownerName, outputFormat)
		responseItem := map[string]any{"url": saved.URL, "revised_prompt": revised, "output_format": outputFormat}
		if saved.CloudURL != "" {
			responseItem["cloud_url"] = saved.CloudURL
			responseItem["storage_type"] = saved.StorageType
			responseItem["encrypted"] = saved.Encrypted
		}
		if responseFormat == "b64_json" {
			responseItem["b64_json"] = base64.StdEncoding.EncodeToString(imageBytes)
		}
		data = append(data, responseItem)
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	result := map[string]any{"created": created, "data": data}
	if message != "" && len(data) == 0 {
		result["message"] = message
	}
	return result, nil
}

// savedImageResult holds the result of saving an image, including cloud storage metadata.
type savedImageResult struct {
	URL         string // Primary URL: DirectURL for S3 Direct, local proxy for encrypted
	CloudURL    string // Raw cloud storage URL (empty if no cloud storage)
	StorageType string // "local" or "cloud"
	Encrypted   bool   // Whether the cloud URL content is encrypted
}

func (e *Engine) SaveImageBytes(imageData []byte, baseURL string) string {
	return e.SaveImageBytesForOwner(imageData, baseURL, "", "").URL
}

func (e *Engine) SaveImageBytesForOwner(imageData []byte, baseURL, ownerID, ownerName string) savedImageResult {
	return e.SaveImageBytesForOwnerWithFormat(imageData, baseURL, ownerID, ownerName, "png")
}

func (e *Engine) SaveImageBytesForOwnerWithFormat(imageData []byte, baseURL, ownerID, ownerName, outputFormat string) savedImageResult {
	outputFormat = NormalizeImageOutputFormat(outputFormat)
	sum := md5.Sum(imageData)
	filename := fmt.Sprintf("%d_%s.%s", time.Now().Unix(), hex.EncodeToString(sum[:]), imageFileExtension(outputFormat))
	relativeDir := filepath.Join(time.Now().Format("2006"), time.Now().Format("01"), time.Now().Format("02"))
	rel := filepath.Join(relativeDir, filename)
	filePath := filepath.Join(e.Config.ImagesDir(), rel)
	_ = os.MkdirAll(filepath.Dir(filePath), 0o755)

	// Always write to local disk first as a safety net.
	_ = os.WriteFile(filePath, imageData, 0o644)
	e.writeImageOwnerMetadata(rel, ownerID, ownerName)

	if baseURL == "" {
		baseURL = e.Config.BaseURL()
	}
	localURL := strings.TrimRight(baseURL, "/") + "/images/" + filepath.ToSlash(rel)

	// Cloud storage: upload synchronously and return cloud metadata.
	result := savedImageResult{URL: localURL, StorageType: "local"}
	if e.CloudStorage != nil && e.CloudStorage.Enabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		record, err := e.CloudStorage.UploadImage(ctx, imageData, filename)
		if err != nil {
			if e.Logger != nil {
				e.Logger.Warning("cloud upload failed, keeping local file", "filename", filename, "error", err)
			}
			return result
		}
		if err := e.CloudStorage.SaveRecord(ctx, rel, record); err != nil {
			if e.Logger != nil {
				e.Logger.Warning("cloud record save failed", "filename", filename, "error", err)
			}
			return result
		}
		// Upload succeeded and record saved. Remove local file to save disk space.
		if removeErr := os.Remove(filePath); removeErr != nil && !os.IsNotExist(removeErr) {
			if e.Logger != nil {
				e.Logger.Warning("failed to remove local file after cloud upload", "path", filePath, "error", removeErr)
			}
		}
		result.StorageType = "cloud"
		result.CloudURL = record.CloudURL
		result.Encrypted = record.EncryptKey != ""
		if record.DirectURL != "" {
			// S3 Direct mode: return the public S3 URL as primary URL
			result.URL = record.DirectURL
		}
		// Encrypted mode: keep local proxy URL as primary (server decrypts on access)
	}
	return result
}

func imageFileExtension(outputFormat string) string {
	if NormalizeImageOutputFormat(outputFormat) == "jpeg" {
		return "jpg"
	}
	return NormalizeImageOutputFormat(outputFormat)
}

func encodeImageBytes(data []byte, options ImageOutputOptions) ([]byte, error) {
	format := NormalizeImageOutputFormat(options.Format)
	if format == "png" {
		return data, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	switch format {
	case "jpeg":
		quality := 90
		if options.Compression != nil {
			quality = 100 - *options.Compression
			if quality < 1 {
				quality = 1
			} else if quality > 100 {
				quality = 100
			}
		}
		if err := jpeg.Encode(&buf, flattenAlpha(img), &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
	case "webp":
		if err := nativewebp.Encode(&buf, img, nil); err != nil {
			return nil, err
		}
	default:
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func flattenAlpha(img image.Image) image.Image {
	bounds := img.Bounds()
	out := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			alpha := int(a)
			out.Set(x, y, color.RGBA{
				R: blendOverWhite(int(r), alpha),
				G: blendOverWhite(int(g), alpha),
				B: blendOverWhite(int(b), alpha),
				A: 255,
			})
		}
	}
	return out
}

func blendOverWhite(channel, alpha int) uint8 {
	value := (channel*alpha + 0xffff*(0xffff-alpha)) / 0xffff
	return uint8(value >> 8)
}

func (e *Engine) writeImageOwnerMetadata(rel, ownerID, ownerName string) {
	ownerID = strings.TrimSpace(ownerID)
	ownerName = strings.TrimSpace(ownerName)
	if e == nil || e.Config == nil || ownerID == "" {
		return
	}
	value := map[string]any{"owner_id": ownerID, "updated_at": time.Now().UTC().Format(time.RFC3339Nano)}
	if ownerName != "" {
		value["owner_name"] = ownerName
	}
	if e.Storage != nil {
		_ = e.Storage.SaveJSONDocument(imageOwnerDocumentName(rel), value)
		return
	}
	metaPath := filepath.Join(e.Config.ImageMetadataDir(), filepath.FromSlash(filepath.ToSlash(rel))+".json")
	_ = os.MkdirAll(filepath.Dir(metaPath), 0o755)
	data, err := json.Marshal(value)
	if err == nil {
		_ = os.WriteFile(metaPath, data, 0o644)
	}
}

func imageOwnerDocumentName(rel string) string {
	return "image_metadata/" + filepath.ToSlash(rel) + ".json"
}

// downloadImageBytes 用 Engine.Proxy 提供的 HTTP 客户端下载远程图片 URL 的
// 二进制数据。仅供 formatImageResultWithOptions 在上游忽略
// response_format=b64_json 而直接返回 url 时回填使用。
//
// timeout 设置为 5 分钟（与 OpenAIImageBackendFactory 的 180s 同量级）。
// 状态码非 2xx、Body 读取失败、字节数为 0 都视为下载失败，返回 error。
func (e *Engine) downloadImageBytes(url string) ([]byte, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil, fmt.Errorf("empty url")
	}
	var client = http.DefaultClient
	if e != nil && e.Proxy != nil {
		client = e.Proxy.HTTPClient(5 * time.Minute)
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build image url request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download image url: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download image url returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read image url body: %w", err)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("download image url returned empty body")
	}
	return body, nil
}
