package protocol

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt2api/internal/backend"
	"chatgpt2api/internal/service"
	"chatgpt2api/internal/util"
)

type testProtocolImageConfig struct {
	root string
}

type testProtocolProxyConfig struct{}

func (testProtocolProxyConfig) Proxy() string { return "" }

// testStaticAutoRouteResolver 是一个最简化的 AutoRouteResolver 测试桩：
// 直接通过 util.BucketForModel 校验 originalModel 并返回对应桶。空串 /
// "auto" 默认解析到桶 A 的 gpt-image-2，方便复用既有测试用例。
type testStaticAutoRouteResolver struct{}

func (testStaticAutoRouteResolver) Resolve(_ service.Identity, originalModel string, _ int) (string, string, error) {
	model := strings.TrimSpace(originalModel)
	if model == "" || model == "auto" {
		return "gpt-image-2", "bucket_a", nil
	}
	switch model {
	case "gpt-image-2":
		return model, "bucket_a", nil
	case "codex-gpt-image-2", "gemini-3.1-flash-image":
		return model, "bucket_b", nil
	default:
		return "", "", fmt.Errorf("model %s is not a billable image model", model)
	}
}

func (c testProtocolImageConfig) ImagesDir() string {
	path := filepath.Join(c.root, "images")
	_ = os.MkdirAll(path, 0o755)
	return path
}

func (c testProtocolImageConfig) ImageMetadataDir() string {
	path := filepath.Join(c.root, "image_metadata")
	_ = os.MkdirAll(path, 0o755)
	return path
}

func (c testProtocolImageConfig) BaseURL() string {
	return "https://example.test"
}

func TestFormatImageResultStoresOwnerName(t *testing.T) {
	config := testProtocolImageConfig{root: t.TempDir()}
	engine := &Engine{Config: config}
	imageData := base64.StdEncoding.EncodeToString([]byte("png-bytes"))

	result := engine.FormatImageResult(
		[]map[string]any{{"b64_json": imageData}},
		"draw",
		"url",
		"https://example.test",
		"linuxdo:41499",
		"Cassianvale",
		123,
		"",
	)
	items, _ := result["data"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("FormatImageResult() data = %#v", result["data"])
	}
	imageURL, _ := items[0]["url"].(string)
	rel := strings.TrimPrefix(imageURL, "https://example.test/images/")
	if rel == imageURL || rel == "" {
		t.Fatalf("image url = %q", imageURL)
	}

	data, err := os.ReadFile(filepath.Join(config.ImageMetadataDir(), filepath.FromSlash(rel)+".json"))
	if err != nil {
		t.Fatalf("ReadFile(metadata) error = %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("Unmarshal(metadata) error = %v", err)
	}
	if meta["owner_id"] != "linuxdo:41499" || meta["owner_name"] != "Cassianvale" {
		t.Fatalf("metadata = %#v", meta)
	}
}

func TestFormatImageResultEncodesRequestedOutputFormat(t *testing.T) {
	config := testProtocolImageConfig{root: t.TempDir()}
	engine := &Engine{Config: config}
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	src.Set(0, 0, color.NRGBA{R: 255, A: 255})
	src.Set(1, 0, color.NRGBA{G: 255, A: 255})
	src.Set(0, 1, color.NRGBA{B: 255, A: 255})
	src.Set(1, 1, color.NRGBA{R: 255, G: 255, B: 255, A: 128})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, src); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	compression := 25

	result := engine.FormatImageResultWithOptions(
		[]map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(encoded.Bytes())}},
		"draw",
		"b64_json",
		"https://example.test",
		"owner-1",
		"Alice",
		123,
		"",
		ImageOutputOptions{Format: "jpeg", Compression: &compression},
	)
	items, _ := result["data"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("FormatImageResultWithOptions() data = %#v", result["data"])
	}
	if items[0]["output_format"] != "jpeg" {
		t.Fatalf("output_format = %#v, want jpeg", items[0]["output_format"])
	}
	imageURL, _ := items[0]["url"].(string)
	if !strings.HasSuffix(imageURL, ".jpg") {
		t.Fatalf("image url = %q, want .jpg suffix", imageURL)
	}
	b64, _ := items[0]["b64_json"].(string)
	converted, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(converted))
	if err != nil {
		t.Fatalf("DecodeConfig() error = %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("decoded format = %q, want jpeg", format)
	}
	rel := strings.TrimPrefix(imageURL, "https://example.test/images/")
	if _, err := os.Stat(filepath.Join(config.ImagesDir(), filepath.FromSlash(rel))); err != nil {
		t.Fatalf("stored jpeg missing: %v", err)
	}
}

func TestFormatImageResultRequestedFormatOverridesUpstreamFormat(t *testing.T) {
	config := testProtocolImageConfig{root: t.TempDir()}
	engine := &Engine{Config: config}
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	src.Set(0, 0, color.NRGBA{R: 255, A: 255})
	src.Set(1, 0, color.NRGBA{G: 255, A: 255})
	src.Set(0, 1, color.NRGBA{B: 255, A: 255})
	src.Set(1, 1, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, src); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	compression := 30

	result := engine.FormatImageResultWithOptions(
		[]map[string]any{{
			"b64_json":      base64.StdEncoding.EncodeToString(encoded.Bytes()),
			"output_format": "png",
		}},
		"draw",
		"b64_json",
		"https://example.test",
		"owner-1",
		"Alice",
		123,
		"",
		ImageOutputOptions{Format: "jpeg", Compression: &compression},
	)
	items, _ := result["data"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("FormatImageResultWithOptions() data = %#v", result["data"])
	}
	if items[0]["output_format"] != "jpeg" {
		t.Fatalf("output_format = %#v, want requested jpeg", items[0]["output_format"])
	}
	imageURL, _ := items[0]["url"].(string)
	if !strings.HasSuffix(imageURL, ".jpg") {
		t.Fatalf("image url = %q, want .jpg suffix", imageURL)
	}
	b64, _ := items[0]["b64_json"].(string)
	converted, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(converted))
	if err != nil {
		t.Fatalf("DecodeConfig() error = %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("decoded format = %q, want jpeg", format)
	}
}

func TestFormatImageResultTrustsCodexUpstreamOutputFormat(t *testing.T) {
	config := testProtocolImageConfig{root: t.TempDir()}
	engine := &Engine{Config: config}
	upstreamBytes := []byte("RIFF\x10\x00\x00\x00WEBPcodex-upstream-bytes")
	compression := 40
	options := imageResultOutputOptions(
		ConversationRequest{Model: "codex-gpt-image-2", OutputFormat: "jpeg", OutputCompression: &compression},
		backend.ResponsesImageEvent{OutputFormat: "webp"},
	)
	if !options.TrustUpstreamFormat {
		t.Fatal("Codex result options should trust upstream format")
	}
	if options.Format != "webp" {
		t.Fatalf("Codex result format = %q, want upstream webp", options.Format)
	}
	if options.Compression != nil {
		t.Fatalf("Codex result compression = %#v, want nil", *options.Compression)
	}

	result := engine.FormatImageResultWithOptions(
		[]map[string]any{{
			"b64_json":      base64.StdEncoding.EncodeToString(upstreamBytes),
			"output_format": "webp",
		}},
		"draw",
		"b64_json",
		"https://example.test",
		"owner-1",
		"Alice",
		123,
		"",
		options,
	)
	items, _ := result["data"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("FormatImageResultWithOptions() data = %#v", result["data"])
	}
	if items[0]["output_format"] != "webp" {
		t.Fatalf("output_format = %#v, want upstream webp", items[0]["output_format"])
	}
	imageURL, _ := items[0]["url"].(string)
	if !strings.HasSuffix(imageURL, ".webp") {
		t.Fatalf("image url = %q, want .webp suffix", imageURL)
	}
	b64, _ := items[0]["b64_json"].(string)
	returnedBytes, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	if !bytes.Equal(returnedBytes, upstreamBytes) {
		t.Fatalf("returned bytes = %q, want upstream bytes %q", returnedBytes, upstreamBytes)
	}
	rel := strings.TrimPrefix(imageURL, "https://example.test/images/")
	storedBytes, err := os.ReadFile(filepath.Join(config.ImagesDir(), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("ReadFile(stored image) error = %v", err)
	}
	if !bytes.Equal(storedBytes, upstreamBytes) {
		t.Fatalf("stored bytes = %q, want upstream bytes %q", storedBytes, upstreamBytes)
	}
}

func TestFormatImageResultIgnoresWebPOutputCompression(t *testing.T) {
	config := testProtocolImageConfig{root: t.TempDir()}
	engine := &Engine{Config: config}
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	src.Set(0, 0, color.NRGBA{R: 255, A: 255})
	src.Set(1, 0, color.NRGBA{G: 255, A: 255})
	src.Set(0, 1, color.NRGBA{B: 255, A: 255})
	src.Set(1, 1, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, src); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	compression := 90

	result := engine.FormatImageResultWithOptions(
		[]map[string]any{{
			"b64_json":           base64.StdEncoding.EncodeToString(encoded.Bytes()),
			"output_format":      "webp",
			"output_compression": 10,
		}},
		"draw",
		"b64_json",
		"https://example.test",
		"owner-1",
		"Alice",
		123,
		"",
		ImageOutputOptions{Format: "webp", Compression: &compression},
	)
	items, _ := result["data"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("FormatImageResultWithOptions() data = %#v", result["data"])
	}
	if items[0]["output_format"] != "webp" {
		t.Fatalf("output_format = %#v, want webp", items[0]["output_format"])
	}
	imageURL, _ := items[0]["url"].(string)
	if !strings.HasSuffix(imageURL, ".webp") {
		t.Fatalf("image url = %q, want .webp suffix", imageURL)
	}
	b64, _ := items[0]["b64_json"].(string)
	converted, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	headerLen := min(len(converted), 12)
	header := converted[:headerLen]
	if !bytes.HasPrefix(header, []byte("RIFF")) || !bytes.Contains(header, []byte("WEBP")) {
		t.Fatalf("converted bytes are not webp: %x", header)
	}
}

func TestImageStreamErrorMessage(t *testing.T) {
	cloudflare := `bootstrap failed: status=403, body=<html><script>window._cf_chl_opt={}</script>Enable JavaScript and cookies to continue</html>`
	if got := imageStreamErrorMessage(cloudflare); got != "upstream returned Cloudflare challenge page; refresh browser fingerprint/session or change proxy" {
		t.Fatalf("cloudflare challenge error = %q", got)
	}

	cases := []string{
		"curl: (35) OpenSSL SSL_connect: SSL_ERROR_SYSCALL",
		"TLS connect error: connection reset by peer",
		"error: OPENSSL_INTERNAL:WRONG_VERSION_NUMBER",
		`Get "https://chatgpt.com/": surf: HTTP/2 request failed: uTLS.HandshakeContext() error: EOF; HTTP/1.1 fallback failed: uTLS.HandshakeContext() error: EOF`,
	}
	for _, input := range cases {
		if got := imageStreamErrorMessage(input); got != "upstream connection failed before TLS handshake completed; check proxy reachability to chatgpt.com or change proxy" {
			t.Fatalf("imageStreamErrorMessage(%q) = %q", input, got)
		}
	}
	if got := imageStreamErrorMessage("upstream returned 500"); got != "upstream returned 500" {
		t.Fatalf("non-connection error = %q", got)
	}
	flowControl := "connection error: FLOW_CONTROL_ERROR"
	if got := imageStreamErrorMessage(flowControl); got != "upstream image stream interrupted by HTTP/2 flow control; retry the request or change proxy if it repeats" {
		t.Fatalf("flow control error = %q", got)
	}
	if got := imageStreamErrorMessage(""); got != "upstream image request failed without error detail" {
		t.Fatalf("empty error = %q", got)
	}
}

func TestHandleImageGenerationsReturnsUpstreamTextResponse(t *testing.T) {
	engine := &Engine{
		ImageTokenProvider: func(context.Context) (string, error) { return "test-token", nil },
		ImageClientFactory: func(string) *backend.Client { return nil },
		AutoRouteResolver:  testStaticAutoRouteResolver{},
	}
	engine.StreamImageOutputsFunc = func(ctx context.Context, client *backend.Client, request ConversationRequest, index, total int) (<-chan ImageOutput, <-chan error) {
		out := make(chan ImageOutput, 1)
		errCh := make(chan error, 1)
		out <- ImageOutput{Kind: "message", Model: request.Model, Index: index, Total: total, Created: time.Now().Unix(), Text: "你好！我是 ChatGPT。", UpstreamEventType: "image_text_response"}
		close(out)
		errCh <- nil
		close(errCh)
		return out, errCh
	}

	result, _, err := engine.HandleImageGenerations(context.Background(), map[string]any{
		"prompt": "你好，你是什么模型？",
		"model":  "gpt-image-2",
	})
	if err == nil {
		t.Fatal("HandleImageGenerations() error = nil, want text-response image error")
	}
	var imageErr *ImageGenerationError
	if !errors.As(err, &imageErr) {
		t.Fatalf("HandleImageGenerations() error = %T %v, want ImageGenerationError", err, err)
	}
	if imageErr.Code != "image_generation_text_response" || imageErr.Message != "你好！我是 ChatGPT。" {
		t.Fatalf("image error = %#v", imageErr)
	}
	if result["output_type"] != "text" {
		t.Fatalf("output_type = %#v, want text in %#v", result["output_type"], result)
	}
	if result["message"] != "你好！我是 ChatGPT。" {
		t.Fatalf("message = %#v, want upstream text", result["message"])
	}
}

func TestHandleImageGenerationsReturnsArbitraryUpstreamImageText(t *testing.T) {
	const upstreamText = "上游返回的任何非排队文本都应该原样返回。"
	engine := &Engine{
		ImageTokenProvider: func(context.Context) (string, error) { return "test-token", nil },
		ImageClientFactory: func(string) *backend.Client { return nil },
		AutoRouteResolver:  testStaticAutoRouteResolver{},
	}
	engine.StreamImageOutputsFunc = func(ctx context.Context, client *backend.Client, request ConversationRequest, index, total int) (<-chan ImageOutput, <-chan error) {
		out := make(chan ImageOutput, 1)
		errCh := make(chan error, 1)
		out <- ImageOutput{Kind: "message", Model: request.Model, Index: index, Total: total, Created: time.Now().Unix(), Text: upstreamText, UpstreamEventType: "image_text_response"}
		close(out)
		errCh <- nil
		close(errCh)
		return out, errCh
	}

	result, _, err := engine.HandleImageGenerations(context.Background(), map[string]any{
		"prompt": "draw",
		"model":  "gpt-image-2",
	})
	if err == nil {
		t.Fatal("HandleImageGenerations() error = nil, want text-response image error")
	}
	if result["output_type"] != "text" || result["message"] != upstreamText {
		t.Fatalf("result = %#v, want arbitrary upstream text response", result)
	}
}

func TestStreamResponsesImageOutputsCompletesWithUpstreamRefusalText(t *testing.T) {
	const upstreamText = "非常抱歉，生成的图片可能违反了关于裸露、色情或情色内容的防护限制。如果你认为此判断有误，请重试或修改提示语。"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<html data-build="build-1"><script src="/backend-api/sentinel/sdk.js"></script></html>`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/sentinel/chat-requirements":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"req-token","proofofwork":{"required":false},"turnstile":{"required":false},"arkose":{"required":false}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/f/conversation/prepare":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"conduit_token":"conduit-token"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/f/conversation":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"title_generation\",\"title\":\"正在处理图片\",\"conversation_id\":\"conv-refused\"}\n\n"))
			_, _ = w.Write([]byte("data: {\"type\":\"message_stream_complete\",\"conversation_id\":\"conv-refused\"}\n\n"))
		case r.Method == http.MethodGet && r.URL.Path == "/backend-api/conversation/conv-refused":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"mapping":{
				"assistant-text":{"message":{"author":{"role":"assistant"},"create_time":3,"content":{"content_type":"text","parts":["` + upstreamText + `"]},"status":"finished_successfully","recipient":"all","metadata":{"model_slug":"gpt-5-5"}}}
			}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	engine := &Engine{
		ImageTokenProvider: func(context.Context) (string, error) { return "test-token", nil },
		ImageClientFactory: func(token string) *backend.Client {
			client := backend.NewClient(token, nil, service.NewProxyService(testProtocolProxyConfig{}))
			client.BaseURL = server.URL
			return client
		},
	}

	outputs, imageErr := engine.StreamImageOutputsWithPool(context.Background(), ConversationRequest{
		Prompt: "edit",
		Model:  "gpt-image-2",
		N:      1,
	})
	result, err := engine.CollectImageOutputs(outputs, imageErr)
	if err != nil {
		t.Fatalf("CollectImageOutputs() err = %v", err)
	}
	if result["output_type"] != "text" || result["message"] != upstreamText {
		t.Fatalf("result = %#v, want upstream refusal text as text output", result)
	}
}

func TestIsFinalImageTextEventIgnoresImageGenMetadataWithResultIDs(t *testing.T) {
	toolFalse := false
	event := backend.ResponsesImageEvent{
		Type:           "server_ste_metadata",
		Text:           "Here is the generated image.",
		ToolInvoked:    &toolFalse,
		TurnUseCase:    "image gen",
		SedimentIDs:    []string{"file_image"},
		ConversationID: "conv-image",
	}

	if isFinalImageTextEvent(event) {
		t.Fatalf("isFinalImageTextEvent(%#v) = true, want false for image generation metadata", event)
	}
}

func TestIsFinalImageTextEventWaitsForBackendTextMarkerOnImageGenRefusal(t *testing.T) {
	event := backend.ResponsesImageEvent{
		Type:        "message_stream_complete",
		Text:        "非常抱歉，生成的图片可能违反了关于裸露、色情或情色内容的防护限制。如果你认为此判断有误，请重试或修改提示语。",
		TurnUseCase: "image gen",
	}

	if isFinalImageTextEvent(event) {
		t.Fatalf("isFinalImageTextEvent(%#v) = true, want false before backend marks final text", event)
	}

	event.Type = "image_text_response"
	if !isFinalImageTextEvent(event) {
		t.Fatalf("isFinalImageTextEvent(%#v) = false, want true after backend marks final text", event)
	}
}

func TestIsFinalImageTextEventKeepsQueuedImageNoticePending(t *testing.T) {
	event := backend.ResponsesImageEvent{
		Type:        "message_stream_complete",
		Text:        "正在处理图片，目前有很多人在创建图片，因此可能需要一点时间。图片准备好后我们会通知你。",
		TurnUseCase: "image gen",
	}

	if isFinalImageTextEvent(event) {
		t.Fatalf("isFinalImageTextEvent(%#v) = true, want false for queued image notice", event)
	}
}

func TestIsTransientImageStreamErrorMessage(t *testing.T) {
	transient := []string{
		"responses SSE read error: stream error: stream ID 1; INTERNAL_ERROR; received from peer",
		"connection error: FLOW_CONTROL_ERROR",
		"http2: client connection lost",
		"unexpected EOF",
		"connection reset by peer",
		"stream closed",
		"bootstrap failed: upstream connection failed before TLS handshake completed; check proxy reachability to chatgpt.com or change proxy",
		`bootstrap failed: Get "https://chatgpt.com/": surf: HTTP/2 request failed: uTLS.HandshakeContext() error: EOF; HTTP/1.1 fallback failed: uTLS.HandshakeContext() error: EOF`,
	}
	for _, input := range transient {
		if !isTransientImageStreamErrorMessage(input) {
			t.Fatalf("isTransientImageStreamErrorMessage(%q) = false, want true", input)
		}
	}

	stable := []string{
		"upstream returned Cloudflare challenge page",
		"You've reached the image generation limit for now.",
		"invalid size: expected WIDTHxHEIGHT",
		"auth_chat_requirements failed: status=401",
	}
	for _, input := range stable {
		if isTransientImageStreamErrorMessage(input) {
			t.Fatalf("isTransientImageStreamErrorMessage(%q) = true, want false", input)
		}
	}
}

func TestStreamImageOutputsWithPoolRunsRequestedImagesConcurrently(t *testing.T) {
	engine := &Engine{
		ImageTokenProvider: func(context.Context) (string, error) { return "test-token", nil },
		ImageClientFactory: func(string) *backend.Client { return nil },
	}

	var mu sync.Mutex
	started := 0
	maxActive := 0
	release := make(chan struct{})
	engine.StreamImageOutputsFunc = func(ctx context.Context, client *backend.Client, request ConversationRequest, index, total int) (<-chan ImageOutput, <-chan error) {
		out := make(chan ImageOutput)
		errCh := make(chan error, 1)
		go func() {
			defer close(out)
			defer close(errCh)
			mu.Lock()
			started++
			if started > maxActive {
				maxActive = started
			}
			mu.Unlock()
			<-release
			out <- ImageOutput{
				Kind:    "result",
				Model:   request.Model,
				Index:   index,
				Total:   total,
				Created: int64(index),
				Data:    []map[string]any{{"url": imageURLForIndex(index)}},
			}
			mu.Lock()
			started--
			mu.Unlock()
			errCh <- nil
		}()
		return out, errCh
	}

	outputs, errCh := engine.StreamImageOutputsWithPool(context.Background(), ConversationRequest{
		Model: "gpt-image-2",
		N:     4,
	})

	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	gotActive := maxActive
	mu.Unlock()
	if gotActive != 4 {
		t.Fatalf("max concurrent image workers = %d, want 4", gotActive)
	}

	close(release)
	for range outputs {
	}
	if err := <-errCh; err != nil {
		t.Fatalf("StreamImageOutputsWithPool() err = %v", err)
	}
}

func TestStreamImageOutputsWithPoolHonorsImageOutputSlotAcquirer(t *testing.T) {
	engine := &Engine{
		ImageTokenProvider: func(context.Context) (string, error) { return "test-token", nil },
		ImageClientFactory: func(string) *backend.Client { return nil },
	}

	var mu sync.Mutex
	active := 0
	maxActive := 0
	release := make(chan struct{})
	slots := make(chan struct{}, 2)
	engine.StreamImageOutputsFunc = func(ctx context.Context, client *backend.Client, request ConversationRequest, index, total int) (<-chan ImageOutput, <-chan error) {
		out := make(chan ImageOutput)
		errCh := make(chan error, 1)
		go func() {
			defer close(out)
			defer close(errCh)
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			mu.Unlock()
			<-release
			out <- ImageOutput{
				Kind:    "result",
				Model:   request.Model,
				Index:   index,
				Total:   total,
				Created: int64(index),
				Data:    []map[string]any{{"url": imageURLForIndex(index)}},
			}
			mu.Lock()
			active--
			mu.Unlock()
			errCh <- nil
		}()
		return out, errCh
	}

	outputs, errCh := engine.StreamImageOutputsWithPool(context.Background(), ConversationRequest{
		Model: "gpt-image-2",
		N:     3,
		AcquireImageOutputSlot: func(ctx context.Context, index int) (func(), error) {
			select {
			case slots <- struct{}{}:
				return func() { <-slots }, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})

	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	gotActive := maxActive
	mu.Unlock()
	if gotActive != 2 {
		t.Fatalf("max concurrent image workers = %d, want 2", gotActive)
	}

	close(release)
	for range outputs {
	}
	if err := <-errCh; err != nil {
		t.Fatalf("StreamImageOutputsWithPool() err = %v", err)
	}
}

func TestStreamImageOutputsWithPoolDoesNotRotateOnGenericUnauthorized(t *testing.T) {
	usedTokens := []string(nil)
	engine := &Engine{
		ImageTokenProvider: func(context.Context) (string, error) {
			token := fmt.Sprintf("token-%d", len(usedTokens)+1)
			usedTokens = append(usedTokens, token)
			return token, nil
		},
		ImageClientFactory: func(string) *backend.Client { return nil },
	}
	engine.StreamImageOutputsFunc = func(ctx context.Context, client *backend.Client, request ConversationRequest, index, total int) (<-chan ImageOutput, <-chan error) {
		out := make(chan ImageOutput)
		errCh := make(chan error, 1)
		close(out)
		errCh <- fmt.Errorf("auth_chat_requirements failed: status=401, body={\"detail\":\"challenge_required\"}")
		close(errCh)
		return out, errCh
	}

	outputs, errCh := engine.StreamImageOutputsWithPool(context.Background(), ConversationRequest{
		Model: "gpt-image-2",
		N:     1,
	})
	for range outputs {
	}
	err := <-errCh
	if err == nil {
		t.Fatal("StreamImageOutputsWithPool() err = nil, want upstream error")
	}
	if len(usedTokens) != 1 {
		t.Fatalf("used tokens = %#v, want one token without pool rotation", usedTokens)
	}
	if !strings.Contains(err.Error(), "challenge_required") {
		t.Fatalf("error = %q, want original upstream detail", err.Error())
	}
}

func TestStreamImageOutputsWithPoolReportsCodexUnauthorizedPermission(t *testing.T) {
	usedTokens := []string(nil)
	engine := &Engine{
		ImageTokenProvider: func(context.Context) (string, error) {
			token := fmt.Sprintf("token-%d", len(usedTokens)+1)
			usedTokens = append(usedTokens, token)
			return token, nil
		},
		ImageClientFactory: func(string) *backend.Client { return nil },
	}
	engine.StreamImageOutputsFunc = func(ctx context.Context, client *backend.Client, request ConversationRequest, index, total int) (<-chan ImageOutput, <-chan error) {
		out := make(chan ImageOutput)
		errCh := make(chan error, 1)
		close(out)
		errCh <- fmt.Errorf("/backend-api/codex/responses failed: status=401, body={\"detail\":\"Unauthorized\"}")
		close(errCh)
		return out, errCh
	}

	outputs, errCh := engine.StreamImageOutputsWithPool(context.Background(), ConversationRequest{
		Model: "codex-gpt-image-2",
		N:     1,
	})
	for range outputs {
	}
	err := <-errCh
	if err == nil {
		t.Fatal("StreamImageOutputsWithPool() err = nil, want permission error")
	}
	if len(usedTokens) != 1 {
		t.Fatalf("used tokens = %#v, want one token without pool rotation", usedTokens)
	}
	if !strings.Contains(err.Error(), "codex-gpt-image-2 需要 Plus / Team / Pro 账号") {
		t.Fatalf("error = %q, want Codex permission guidance", err.Error())
	}
}

func TestCollectImageOutputsKeepsImageOrderByIndex(t *testing.T) {
	outputs := make(chan ImageOutput, 2)
	errCh := make(chan error, 1)
	outputs <- ImageOutput{
		Kind:    "result",
		Index:   2,
		Total:   2,
		Created: 2,
		Data:    []map[string]any{{"url": "https://example.test/second.png"}},
	}
	outputs <- ImageOutput{
		Kind:    "result",
		Index:   1,
		Total:   2,
		Created: 1,
		Data:    []map[string]any{{"url": "https://example.test/first.png"}},
	}
	close(outputs)
	errCh <- nil
	close(errCh)

	result, err := (&Engine{}).CollectImageOutputs(outputs, errCh)
	if err != nil {
		t.Fatalf("CollectImageOutputs() err = %v", err)
	}
	data := result["data"].([]map[string]any)
	if len(data) != 2 {
		t.Fatalf("data len = %d, want 2", len(data))
	}
	if data[0]["url"] != "https://example.test/first.png" || data[1]["url"] != "https://example.test/second.png" {
		t.Fatalf("data order = %#v, want first then second", data)
	}
}

// TestCollectImageOutputsAggregatesUpstreamKind 校验当 ImageOutput.Raw
// 携带 upstream_kind 时，CollectImageOutputs 把它聚合到 result["upstream_kind"]
// 并按「第一张交付图胜出」策略选择标识；同时把 upstream_kind 同步写入
// 每张图的 data 项，便于审计在 detail / data 维度做更细粒度区分。
//
// 场景：第一张为 chatgpt（按 Index=1 的逻辑顺序），第二张回落到 openai_api。
// 由于使用的是「第一张交付图」策略，整体应为 chatgpt。
func TestCollectImageOutputsAggregatesUpstreamKind(t *testing.T) {
	outputs := make(chan ImageOutput, 2)
	errCh := make(chan error, 1)
	// 故意以反序提交（先 Index=2 再 Index=1），验证策略基于「第一个进入
	// channel 的 result」而非按 Index 重排后的第一个，与实现保持一致。
	outputs <- ImageOutput{
		Kind:    "result",
		Index:   1,
		Total:   2,
		Created: 1,
		Data:    []map[string]any{{"url": "https://example.test/first.png"}},
		Raw:     map[string]any{"upstream_kind": util.UpstreamKindChatGPT},
	}
	outputs <- ImageOutput{
		Kind:    "result",
		Index:   2,
		Total:   2,
		Created: 2,
		Data:    []map[string]any{{"url": "https://example.test/second.png"}},
		Raw:     map[string]any{"upstream_kind": util.UpstreamKindOpenAIAPI},
	}
	close(outputs)
	errCh <- nil
	close(errCh)

	result, err := (&Engine{}).CollectImageOutputs(outputs, errCh)
	if err != nil {
		t.Fatalf("CollectImageOutputs() err = %v", err)
	}
	if got := util.Clean(result["upstream_kind"]); got != util.UpstreamKindChatGPT {
		t.Fatalf("aggregate upstream_kind = %q, want %q (first-output-wins)", got, util.UpstreamKindChatGPT)
	}
	data := result["data"].([]map[string]any)
	if len(data) != 2 {
		t.Fatalf("data len = %d, want 2", len(data))
	}
	if got := util.Clean(data[0]["upstream_kind"]); got != util.UpstreamKindChatGPT {
		t.Fatalf("data[0] upstream_kind = %q, want %q", got, util.UpstreamKindChatGPT)
	}
	if got := util.Clean(data[1]["upstream_kind"]); got != util.UpstreamKindOpenAIAPI {
		t.Fatalf("data[1] upstream_kind = %q, want %q", got, util.UpstreamKindOpenAIAPI)
	}
}

// TestCollectImageOutputsOmitsUpstreamKindWhenAbsent 校验当所有
// ImageOutput 都不含 upstream_kind 时，aggregate 不会写出空串字段。
func TestCollectImageOutputsOmitsUpstreamKindWhenAbsent(t *testing.T) {
	outputs := make(chan ImageOutput, 1)
	errCh := make(chan error, 1)
	outputs <- ImageOutput{
		Kind:    "result",
		Index:   1,
		Total:   1,
		Created: 1,
		Data:    []map[string]any{{"url": "https://example.test/only.png"}},
	}
	close(outputs)
	errCh <- nil
	close(errCh)

	result, err := (&Engine{}).CollectImageOutputs(outputs, errCh)
	if err != nil {
		t.Fatalf("CollectImageOutputs() err = %v", err)
	}
	if _, ok := result["upstream_kind"]; ok {
		t.Fatalf("result should not contain upstream_kind when no output annotated it: %#v", result)
	}
	data := result["data"].([]map[string]any)
	if _, ok := data[0]["upstream_kind"]; ok {
		t.Fatalf("data[0] should not contain upstream_kind when not annotated: %#v", data[0])
	}
}

func imageURLForIndex(index int) string {
	switch index {
	case 1:
		return "https://example.test/image-1.png"
	case 2:
		return "https://example.test/image-2.png"
	case 3:
		return "https://example.test/image-3.png"
	case 4:
		return "https://example.test/image-4.png"
	default:
		return "https://example.test/image.png"
	}
}

// =============================================================================
// 双通路与 Auto 路由相关的测试桩与运行期测试
//
// 这些 fake 同时被 conversation_test.go 与 auto_route_test.go 复用，统一在
// 此处定义以避免重复实现。
// =============================================================================

// markModelCall 是 fakeOpenAIReserver.MarkModelResult 调用的快照。
type markModelCall struct {
	AccountID     string
	UpstreamModel string
	Success       bool
	ErrMessage    string
}

// updateModelStateCall 是 fakeOpenAIReserver.UpdateModelState 调用的快照。
type updateModelStateCall struct {
	AccountID     string
	UpstreamModel string
	Patch         map[string]any
}

// fakeOpenAIReserver 实现 OpenAIAccountReserver 接口，按预置的 reservation
// 队列回应 Reserve 请求，并记录 Reserve / Release / MarkModelResult /
// UpdateModelState 调用顺序以便断言。available 决定 HasAvailableForUpstreamModel
// 的返回值，与 reservations 无关，便于 Auto 路由测试单独控制可调度性。
type fakeOpenAIReserver struct {
	mu                 sync.Mutex
	reservations       []service.OpenAIAccountReservation
	available          map[string]bool
	nextReservationIdx int
	reserveCalls       []string
	releaseCalls       []string
	markCalls          []markModelCall
	updateStateCalls   []updateModelStateCall
}

func (r *fakeOpenAIReserver) ReserveForUpstreamModel(upstreamModel string, _ map[string]struct{}) (service.OpenAIAccountReservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reserveCalls = append(r.reserveCalls, upstreamModel)
	if r.nextReservationIdx >= len(r.reservations) {
		return service.OpenAIAccountReservation{}, fmt.Errorf("no available openai-protocol account for %s", upstreamModel)
	}
	reservation := r.reservations[r.nextReservationIdx]
	r.nextReservationIdx++
	return reservation, nil
}

func (r *fakeOpenAIReserver) Release(accountID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseCalls = append(r.releaseCalls, accountID)
}

func (r *fakeOpenAIReserver) HasAvailableForUpstreamModel(upstreamModel string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.available[upstreamModel]
}

func (r *fakeOpenAIReserver) MarkModelResult(accountID, upstreamModel string, success bool, errMessage string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markCalls = append(r.markCalls, markModelCall{
		AccountID:     accountID,
		UpstreamModel: upstreamModel,
		Success:       success,
		ErrMessage:    errMessage,
	})
}

func (r *fakeOpenAIReserver) UpdateModelState(accountID, upstreamModel string, patch map[string]any) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := map[string]any{}
	for k, v := range patch {
		cp[k] = v
	}
	r.updateStateCalls = append(r.updateStateCalls, updateModelStateCall{
		AccountID:     accountID,
		UpstreamModel: upstreamModel,
		Patch:         cp,
	})
	return nil, nil
}

func (r *fakeOpenAIReserver) ReserveCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reserveCalls...)
}

func (r *fakeOpenAIReserver) ReleaseCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.releaseCalls...)
}

func (r *fakeOpenAIReserver) MarkCalls() []markModelCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]markModelCall(nil), r.markCalls...)
}

// fakeBillingChecker 用于 Auto 路由测试，按桶返回预置的 CheckAvailable 错误。
type fakeBillingChecker struct {
	bucketAErr error
	bucketBErr error
}

func (f fakeBillingChecker) CheckAvailable(_ service.Identity, _ int, bucket string) error {
	switch bucket {
	case util.ImageBucketA:
		return f.bucketAErr
	case util.ImageBucketB:
		return f.bucketBErr
	default:
		return fmt.Errorf("unsupported bucket %q", bucket)
	}
}

// fakeChatGPTInspector 提供 AutoChatGPTAccountInspector 的最小实现。
type fakeChatGPTInspector struct {
	available     bool
	paidAvailable bool
}

func (f fakeChatGPTInspector) HasAvailableImageAccount() bool     { return f.available }
func (f fakeChatGPTInspector) HasAvailablePaidImageAccount() bool { return f.paidAvailable }

func TestBuildUpstreamCandidates(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		expected []upstreamCandidate
	}{
		{
			name:     "gpt-image-2 maps to chatgpt only",
			model:    util.ImageModelGPTImage2,
			expected: []upstreamCandidate{{kind: upstreamKindChatGPT}},
		},
		{
			name:  "codex-gpt-image-2 falls back to openai api",
			model: util.ImageModelCodexGPTImage2,
			expected: []upstreamCandidate{
				{kind: upstreamKindChatGPT},
				{kind: upstreamKindOpenAIAPI, upstreamModel: util.ImageModelGPTImage2},
			},
		},
		{
			name:  "gemini-3.1-flash-image maps to openai api only",
			model: util.ImageModelGeminiFlashImage,
			expected: []upstreamCandidate{
				{kind: upstreamKindOpenAIAPI, upstreamModel: util.ImageModelGeminiFlashImage},
			},
		},
		{name: "auto returns empty candidates", model: util.ImageModelAuto, expected: nil},
		{name: "empty string returns empty candidates", model: "", expected: nil},
		{name: "unknown model returns empty candidates", model: "unknown-model", expected: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildUpstreamCandidates(tc.model)
			if !reflect.DeepEqual(got, tc.expected) {
				t.Fatalf("buildUpstreamCandidates(%q) = %#v, want %#v", tc.model, got, tc.expected)
			}
		})
	}
}

func TestRunSingleImageOutputCodexFallbackToOpenAIAPI(t *testing.T) {
	var (
		mu          sync.Mutex
		serverCalls int
	)
	pngBytes := []byte("fake-png-content")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		serverCalls++
		mu.Unlock()
		if got, want := r.URL.Path, "/v1/images/generations"; got != want {
			t.Errorf("server path = %q, want %q", got, want)
		}
		body := map[string]any{
			"created": time.Now().Unix(),
			"data": []map[string]any{
				{"b64_json": base64.StdEncoding.EncodeToString(pngBytes)},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)

	reserver := &fakeOpenAIReserver{
		reservations: []service.OpenAIAccountReservation{{
			AccountID:     "oa_acc1",
			APIKey:        "sk-test-key",
			BaseURL:       server.URL,
			UpstreamModel: util.ImageModelGPTImage2,
		}},
	}

	config := testProtocolImageConfig{root: t.TempDir()}
	engine := &Engine{
		Config: config,
		ImageTokenProvider: func(context.Context) (string, error) {
			return "", errors.New("ChatGPT 账号池为空")
		},
		ImageClientFactory:    func(string) *backend.Client { return nil },
		OpenAIAccountReserver: reserver,
		OpenAIImageBackendFactory: func(rsv service.OpenAIAccountReservation) *backend.OpenAIImagesClient {
			return &backend.OpenAIImagesClient{
				HTTPClient: server.Client(),
				BaseURL:    rsv.BaseURL,
				APIKey:     rsv.APIKey,
			}
		},
	}

	outputs, errCh := engine.StreamImageOutputsWithPool(context.Background(), ConversationRequest{
		Model:         util.ImageModelCodexGPTImage2,
		ResolvedModel: util.ImageModelCodexGPTImage2,
		Bucket:        util.ImageBucketB,
		Prompt:        "draw a cat",
		N:             1,
	})

	var collected []ImageOutput
	for output := range outputs {
		collected = append(collected, output)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("StreamImageOutputsWithPool() err = %v, want nil after fallback", err)
	}
	if len(collected) != 1 {
		t.Fatalf("emitted outputs = %d, want 1: %#v", len(collected), collected)
	}
	if collected[0].Kind != "result" {
		t.Fatalf("output kind = %q, want result", collected[0].Kind)
	}
	if got := collected[0].Raw["upstream_kind"]; got != util.UpstreamKindOpenAIAPI {
		t.Fatalf("upstream_kind = %#v, want %q", got, util.UpstreamKindOpenAIAPI)
	}

	mu.Lock()
	gotCalls := serverCalls
	mu.Unlock()
	if gotCalls != 1 {
		t.Fatalf("server calls = %d, want exactly 1 (fallback should succeed on first try)", gotCalls)
	}

	reserveCalls := reserver.ReserveCalls()
	if !reflect.DeepEqual(reserveCalls, []string{util.ImageModelGPTImage2}) {
		t.Fatalf("reserve calls = %#v, want one for %q", reserveCalls, util.ImageModelGPTImage2)
	}
	markCalls := reserver.MarkCalls()
	expectedMark := markModelCall{
		AccountID:     "oa_acc1",
		UpstreamModel: util.ImageModelGPTImage2,
		Success:       true,
		ErrMessage:    "",
	}
	if len(markCalls) != 1 || markCalls[0] != expectedMark {
		t.Fatalf("mark calls = %#v, want single success call %#v", markCalls, expectedMark)
	}
	releaseCalls := reserver.ReleaseCalls()
	if !reflect.DeepEqual(releaseCalls, []string{"oa_acc1"}) {
		t.Fatalf("release calls = %#v, want [oa_acc1]", releaseCalls)
	}
}

func TestRunSingleImageOutputTransientBudgetSharedAcrossChannels(t *testing.T) {
	var (
		mu          sync.Mutex
		serverCalls int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		serverCalls++
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream unavailable","type":"server_error"}}`))
	}))
	t.Cleanup(server.Close)

	// 提供比预算多一份预留，验证第 4 次 Reserve 不会被发起。
	reservations := make([]service.OpenAIAccountReservation, 0, maxTransientImageStreamAttempts+1)
	for i := 0; i <= maxTransientImageStreamAttempts; i++ {
		reservations = append(reservations, service.OpenAIAccountReservation{
			AccountID:     fmt.Sprintf("oa_%d", i),
			APIKey:        fmt.Sprintf("sk-key-%d", i),
			BaseURL:       server.URL,
			UpstreamModel: util.ImageModelGPTImage2,
		})
	}
	reserver := &fakeOpenAIReserver{reservations: reservations}

	config := testProtocolImageConfig{root: t.TempDir()}
	engine := &Engine{
		Config: config,
		ImageTokenProvider: func(context.Context) (string, error) {
			// ChatGPT 通路立即声明池空 → candidateOutcomeExhausted，0 transient 消耗。
			return "", errors.New("ChatGPT 账号池为空")
		},
		ImageClientFactory:    func(string) *backend.Client { return nil },
		OpenAIAccountReserver: reserver,
		OpenAIImageBackendFactory: func(rsv service.OpenAIAccountReservation) *backend.OpenAIImagesClient {
			return &backend.OpenAIImagesClient{
				HTTPClient: server.Client(),
				BaseURL:    rsv.BaseURL,
				APIKey:     rsv.APIKey,
			}
		},
	}

	outputs, errCh := engine.StreamImageOutputsWithPool(context.Background(), ConversationRequest{
		Model:         util.ImageModelCodexGPTImage2,
		ResolvedModel: util.ImageModelCodexGPTImage2,
		Bucket:        util.ImageBucketB,
		Prompt:        "draw a cat",
		N:             1,
	})

	for output := range outputs {
		t.Errorf("unexpected ImageOutput emitted: %#v", output)
	}
	err := <-errCh
	if err == nil {
		t.Fatal("StreamImageOutputsWithPool() err = nil, want transient failure after retry budget")
	}
	var imageErr *ImageGenerationError
	if !errors.As(err, &imageErr) {
		t.Fatalf("err = %T %v, want *ImageGenerationError", err, err)
	}

	mu.Lock()
	gotCalls := serverCalls
	mu.Unlock()
	if gotCalls != maxTransientImageStreamAttempts {
		t.Fatalf("server calls = %d, want exactly %d (shared retry budget)", gotCalls, maxTransientImageStreamAttempts)
	}
	reserveCalls := reserver.ReserveCalls()
	if len(reserveCalls) != maxTransientImageStreamAttempts {
		t.Fatalf("reserve calls = %d (%#v), want %d (one per transient attempt)", len(reserveCalls), reserveCalls, maxTransientImageStreamAttempts)
	}
	for i, model := range reserveCalls {
		if model != util.ImageModelGPTImage2 {
			t.Fatalf("reserve calls[%d] = %q, want %q", i, model, util.ImageModelGPTImage2)
		}
	}
}


func TestRunSingleImageOutputTransientBudgetSharedChatGPTToOpenAIAPI(t *testing.T) {
	// 验证 ChatGPT 通路消耗的 transient 预算会从 OpenAIAPI 通路的可用次数
	// 中扣除：ChatGPT 消耗 2 次后，OpenAIAPI 仅剩 1 次重试机会，第二次
	// 上游调用之前 transient 预算应被耗尽并立即终止。
	var (
		mu          sync.Mutex
		serverCalls int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		serverCalls++
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream unavailable","type":"server_error"}}`))
	}))
	t.Cleanup(server.Close)

	tokenIdx := 0
	chatgptCallCount := 0
	engine := &Engine{
		Config: testProtocolImageConfig{root: t.TempDir()},
		ImageTokenProvider: func(context.Context) (string, error) {
			// 提供两个有效 token 让 ChatGPT 通路消耗 2 次 transient，第三次报告池空。
			tokenIdx++
			if tokenIdx > 2 {
				return "", errors.New("ChatGPT 账号池为空")
			}
			return fmt.Sprintf("token-%d", tokenIdx), nil
		},
		ImageClientFactory: func(string) *backend.Client { return nil },
	}
	engine.StreamImageOutputsFunc = func(ctx context.Context, client *backend.Client, request ConversationRequest, index, total int) (<-chan ImageOutput, <-chan error) {
		out := make(chan ImageOutput)
		errCh := make(chan error, 1)
		close(out)
		mu.Lock()
		chatgptCallCount++
		mu.Unlock()
		errCh <- fmt.Errorf("stream closed")
		close(errCh)
		return out, errCh
	}

	reservations := []service.OpenAIAccountReservation{
		{AccountID: "oa_a", APIKey: "sk-a", BaseURL: server.URL, UpstreamModel: util.ImageModelGPTImage2},
		{AccountID: "oa_b", APIKey: "sk-b", BaseURL: server.URL, UpstreamModel: util.ImageModelGPTImage2},
	}
	reserver := &fakeOpenAIReserver{reservations: reservations}
	engine.OpenAIAccountReserver = reserver
	engine.OpenAIImageBackendFactory = func(rsv service.OpenAIAccountReservation) *backend.OpenAIImagesClient {
		return &backend.OpenAIImagesClient{HTTPClient: server.Client(), BaseURL: rsv.BaseURL, APIKey: rsv.APIKey}
	}

	outputs, errCh := engine.StreamImageOutputsWithPool(context.Background(), ConversationRequest{
		Model:         util.ImageModelCodexGPTImage2,
		ResolvedModel: util.ImageModelCodexGPTImage2,
		Bucket:        util.ImageBucketB,
		Prompt:        "draw",
		N:             1,
	})
	for output := range outputs {
		t.Errorf("unexpected output emitted: %#v", output)
	}
	err := <-errCh
	if err == nil {
		t.Fatal("StreamImageOutputsWithPool() err = nil, want failure after shared transient budget exhaustion")
	}
	var imageErr *ImageGenerationError
	if !errors.As(err, &imageErr) {
		t.Fatalf("err = %T %v, want *ImageGenerationError", err, err)
	}

	mu.Lock()
	gotChatgpt := chatgptCallCount
	gotOpenAI := serverCalls
	mu.Unlock()

	if gotChatgpt != 2 {
		t.Fatalf("chatgpt stream invocations = %d, want 2", gotChatgpt)
	}
	if gotOpenAI != 1 {
		t.Fatalf("openai server calls = %d, want 1 (budget already at 2 from chatgpt; only one OpenAIAPI call before exhaustion)", gotOpenAI)
	}
	if total := gotChatgpt + gotOpenAI; total != maxTransientImageStreamAttempts {
		t.Fatalf("total upstream calls = %d, want %d (cross-channel shared budget)", total, maxTransientImageStreamAttempts)
	}
	reserveCalls := reserver.ReserveCalls()
	if len(reserveCalls) != 1 {
		t.Fatalf("reserve calls = %#v, want exactly 1 reservation before budget exhaustion", reserveCalls)
	}
}
