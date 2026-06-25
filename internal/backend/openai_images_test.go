package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgpt2api/internal/util"
)

// fakeOpenAIImagesUpstream 用于在 httptest.Server 中按测试需要装配响应。
type fakeOpenAIImagesUpstream struct {
	t            *testing.T
	wantPath     string
	respStatus   int
	respBody     string
	capturedReq  *http.Request
	capturedBody []byte
}

func (f *fakeOpenAIImagesUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.wantPath != "" && r.URL.Path != f.wantPath {
		f.t.Fatalf("unexpected request path %q, want %q", r.URL.Path, f.wantPath)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Fatalf("read upstream request body: %v", err)
	}
	f.capturedReq = r.Clone(r.Context())
	f.capturedBody = body
	if f.respStatus == 0 {
		f.respStatus = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.respStatus)
	if f.respBody != "" {
		_, _ = io.WriteString(w, f.respBody)
	}
}

func newOpenAIImagesTestClient(server *httptest.Server) *OpenAIImagesClient {
	return &OpenAIImagesClient{
		HTTPClient: server.Client(),
		BaseURL:    server.URL,
		APIKey:     "sk-test-1234",
	}
}

func TestOpenAIImagesClient_Generate_RequestShape(t *testing.T) {
	upstream := &fakeOpenAIImagesUpstream{
		t:        t,
		wantPath: "/v1/images/generations",
		respBody: `{"created":17,"data":[{"b64_json":"AAA"}]}`,
	}
	server := httptest.NewServer(upstream)
	defer server.Close()

	client := newOpenAIImagesTestClient(server)
	result, err := client.Generate(context.Background(), OpenAIImagesRequest{
		UpstreamModel: "gemini-3.1-flash-image",
		Prompt:        "draw a cat",
		N:             2,
		Size:          "1024x1024",
		OutputFormat:  "png",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if result == nil || len(result.Data) != 1 || result.Data[0].B64JSON != "AAA" {
		t.Fatalf("Generate() result = %#v", result)
	}

	if got := upstream.capturedReq.Method; got != http.MethodPost {
		t.Fatalf("method = %q, want POST", got)
	}
	if got := upstream.capturedReq.Header.Get("Authorization"); got != "Bearer sk-test-1234" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.capturedReq.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := upstream.capturedReq.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q", got)
	}

	var payload map[string]any
	if err := json.Unmarshal(upstream.capturedBody, &payload); err != nil {
		t.Fatalf("decode generation body: %v", err)
	}
	if payload["model"] != "gemini-3.1-flash-image" {
		t.Fatalf("body.model = %#v, want gemini-3.1-flash-image", payload["model"])
	}
	if payload["prompt"] != "draw a cat" {
		t.Fatalf("body.prompt = %#v", payload["prompt"])
	}
	if payload["response_format"] != "b64_json" {
		t.Fatalf("body.response_format = %#v, want b64_json", payload["response_format"])
	}
	// JSON 解码后整数会变成 float64
	if got, _ := payload["n"].(float64); got != 2 {
		t.Fatalf("body.n = %#v, want 2", payload["n"])
	}
	if payload["size"] != "1024x1024" {
		t.Fatalf("body.size = %#v", payload["size"])
	}
	if payload["output_format"] != "png" {
		t.Fatalf("body.output_format = %#v", payload["output_format"])
	}
}

func TestOpenAIImagesClient_Generate_DefaultsOmitOptionalFields(t *testing.T) {
	upstream := &fakeOpenAIImagesUpstream{
		t:        t,
		wantPath: "/v1/images/generations",
		respBody: `{"created":1,"data":[]}`,
	}
	server := httptest.NewServer(upstream)
	defer server.Close()

	client := newOpenAIImagesTestClient(server)
	if _, err := client.Generate(context.Background(), OpenAIImagesRequest{
		UpstreamModel: "gpt-image-2",
		Prompt:        "hello",
	}); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(upstream.capturedBody, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if _, has := payload["n"]; has {
		t.Fatalf("expected n to be omitted, body = %s", string(upstream.capturedBody))
	}
	if _, has := payload["size"]; has {
		t.Fatalf("expected size to be omitted, body = %s", string(upstream.capturedBody))
	}
	if _, has := payload["output_format"]; has {
		t.Fatalf("expected output_format to be omitted, body = %s", string(upstream.capturedBody))
	}
	if payload["response_format"] != "b64_json" {
		t.Fatalf("response_format = %#v", payload["response_format"])
	}
}

func TestOpenAIImagesClient_Edit_RequestShape(t *testing.T) {
	upstream := &fakeOpenAIImagesUpstream{
		t:        t,
		wantPath: "/v1/images/edits",
		respBody: `{"created":42,"data":[{"b64_json":"EDIT"}]}`,
	}
	server := httptest.NewServer(upstream)
	defer server.Close()

	imageA := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A}
	imageB := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0B}
	mask := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0C}

	client := newOpenAIImagesTestClient(server)
	result, err := client.Edit(context.Background(), OpenAIImagesRequest{
		UpstreamModel: "gpt-image-2",
		Prompt:        "edit me",
		N:             3,
		Size:          "1024x1024",
		OutputFormat:  "png",
		InputImages: []ResponsesInputImage{
			{Data: imageA, ContentType: "image/png"},
			{Data: imageB, ContentType: "image/jpeg"},
		},
		InputImageMask: &ResponsesInputImage{Data: mask, ContentType: "image/png"},
	})
	if err != nil {
		t.Fatalf("Edit() error = %v", err)
	}
	if result == nil || len(result.Data) != 1 || result.Data[0].B64JSON != "EDIT" {
		t.Fatalf("Edit() result = %#v", result)
	}

	if got := upstream.capturedReq.Method; got != http.MethodPost {
		t.Fatalf("method = %q", got)
	}
	if got := upstream.capturedReq.Header.Get("Authorization"); got != "Bearer sk-test-1234" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.capturedReq.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q", got)
	}
	contentType := upstream.capturedReq.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("Content-Type %q: %v", contentType, err)
	}
	if mediaType != "multipart/form-data" {
		t.Fatalf("Content-Type media = %q, want multipart/form-data", mediaType)
	}
	boundary := params["boundary"]
	if boundary == "" {
		t.Fatalf("Content-Type missing boundary: %q", contentType)
	}

	reader := multipart.NewReader(strings.NewReader(string(upstream.capturedBody)), boundary)
	form, err := reader.ReadForm(64 << 20)
	if err != nil {
		t.Fatalf("read multipart form: %v", err)
	}
	defer form.RemoveAll()

	wantValues := map[string]string{
		"model":           "gpt-image-2",
		"prompt":          "edit me",
		"n":               "3",
		"size":            "1024x1024",
		"response_format": "b64_json",
		"output_format":   "png",
	}
	for key, want := range wantValues {
		values, ok := form.Value[key]
		if !ok || len(values) == 0 {
			t.Fatalf("multipart form missing field %q", key)
		}
		if values[0] != want {
			t.Fatalf("multipart form[%q] = %q, want %q", key, values[0], want)
		}
	}

	imageParts, ok := form.File["image"]
	if !ok || len(imageParts) != 2 {
		t.Fatalf("multipart image parts = %d, want 2", len(imageParts))
	}
	for index, want := range [][]byte{imageA, imageB} {
		got := readMultipartFile(t, imageParts[index])
		if string(got) != string(want) {
			t.Fatalf("image part %d body mismatch", index)
		}
	}

	maskParts, ok := form.File["mask"]
	if !ok || len(maskParts) != 1 {
		t.Fatalf("multipart mask parts = %d, want 1", len(maskParts))
	}
	if got := readMultipartFile(t, maskParts[0]); string(got) != string(mask) {
		t.Fatalf("mask part body mismatch")
	}
}

func readMultipartFile(t *testing.T, fh *multipart.FileHeader) []byte {
	t.Helper()
	f, err := fh.Open()
	if err != nil {
		t.Fatalf("open multipart file: %v", err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read multipart file: %v", err)
	}
	return body
}

func TestClassifyOpenAIImagesError(t *testing.T) {
	type wantShape struct {
		kind   OpenAIImagesErrorKind
		status int
	}
	tests := []struct {
		name   string
		status int
		body   string
		want   wantShape
	}{
		{
			name:   "401 invalid api key body",
			status: http.StatusUnauthorized,
			body:   `{"error":{"message":"Invalid API key","code":"invalid_api_key"}}`,
			want:   wantShape{kind: OpenAIImagesErrorAuth, status: http.StatusUnauthorized},
		},
		{
			name:   "200 with invalid_api_key marker",
			status: http.StatusOK,
			body:   `{"error":{"message":"bad","code":"invalid_api_key"}}`,
			want:   wantShape{kind: OpenAIImagesErrorAuth, status: http.StatusOK},
		},
		{
			name:   "429 rate limit",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"slow down"}}`,
			want:   wantShape{kind: OpenAIImagesErrorRateLimit, status: http.StatusTooManyRequests},
		},
		{
			name:   "500 transient",
			status: http.StatusInternalServerError,
			body:   `{"error":{"message":"boom"}}`,
			want:   wantShape{kind: OpenAIImagesErrorTransient, status: http.StatusInternalServerError},
		},
		{
			name:   "503 transient",
			status: http.StatusServiceUnavailable,
			body:   ``,
			want:   wantShape{kind: OpenAIImagesErrorTransient, status: http.StatusServiceUnavailable},
		},
		{
			name:   "400 permanent",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"bad request"}}`,
			want:   wantShape{kind: OpenAIImagesErrorPermanent, status: http.StatusBadRequest},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyOpenAIImagesError(tc.status, []byte(tc.body))
			var typed *OpenAIImagesError
			if !errors.As(err, &typed) {
				t.Fatalf("classifyOpenAIImagesError() = %v, want *OpenAIImagesError", err)
			}
			if typed.Kind != tc.want.kind {
				t.Fatalf("kind = %v, want %v", typed.Kind, tc.want.kind)
			}
			if typed.Status != tc.want.status {
				t.Fatalf("status = %v, want %v", typed.Status, tc.want.status)
			}
		})
	}
}

func TestOpenAIImagesClient_Generate_ErrorMappingThroughHTTP(t *testing.T) {
	tests := []struct {
		name       string
		respStatus int
		respBody   string
		wantKind   OpenAIImagesErrorKind
	}{
		{
			name:       "401 maps to Auth",
			respStatus: http.StatusUnauthorized,
			respBody:   `{"error":{"message":"Invalid API key","code":"invalid_api_key"}}`,
			wantKind:   OpenAIImagesErrorAuth,
		},
		{
			name:       "429 maps to RateLimit",
			respStatus: http.StatusTooManyRequests,
			respBody:   `{"error":{"message":"rate limited"}}`,
			wantKind:   OpenAIImagesErrorRateLimit,
		},
		{
			name:       "500 maps to Transient",
			respStatus: http.StatusInternalServerError,
			respBody:   `{"error":{"message":"boom"}}`,
			wantKind:   OpenAIImagesErrorTransient,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &fakeOpenAIImagesUpstream{
				t:          t,
				wantPath:   "/v1/images/generations",
				respStatus: tc.respStatus,
				respBody:   tc.respBody,
			}
			server := httptest.NewServer(upstream)
			defer server.Close()

			client := newOpenAIImagesTestClient(server)
			_, err := client.Generate(context.Background(), OpenAIImagesRequest{
				UpstreamModel: "gpt-image-2",
				Prompt:        "draw something",
			})
			var typed *OpenAIImagesError
			if !errors.As(err, &typed) {
				t.Fatalf("Generate() error = %v, want *OpenAIImagesError", err)
			}
			if typed.Kind != tc.wantKind {
				t.Fatalf("kind = %v, want %v", typed.Kind, tc.wantKind)
			}
			if typed.Status != tc.respStatus {
				t.Fatalf("status = %v, want %v", typed.Status, tc.respStatus)
			}
		})
	}
}

func TestOpenAIImagesClient_Generate_NetworkErrorIsTransient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close() // 立即关闭以制造网络错误
	client := &OpenAIImagesClient{
		HTTPClient: server.Client(),
		BaseURL:    server.URL,
		APIKey:     "sk-test",
	}
	_, err := client.Generate(context.Background(), OpenAIImagesRequest{
		UpstreamModel: "gpt-image-2",
		Prompt:        "draw",
	})
	var typed *OpenAIImagesError
	if !errors.As(err, &typed) {
		t.Fatalf("Generate() error = %v, want *OpenAIImagesError", err)
	}
	if typed.Kind != OpenAIImagesErrorTransient {
		t.Fatalf("kind = %v, want Transient", typed.Kind)
	}
	if typed.Status != 0 {
		t.Fatalf("status = %v, want 0 for network error", typed.Status)
	}
}

func TestOpenAIImagesResult_ToImageOutputs(t *testing.T) {
	result := &OpenAIImagesResult{
		Data: []OpenAIImageDatum{
			{B64JSON: "AAA", RevisedPrompt: "revised-1"},
			{B64JSON: "BBB"},
		},
	}
	const (
		startIndex = 3
		total      = 5
	)
	outputs := result.ToImageOutputs("gemini-3.1-flash-image", startIndex, total, util.UpstreamKindOpenAIAPI)
	if len(outputs) != 2 {
		t.Fatalf("outputs len = %d, want 2", len(outputs))
	}
	for i, output := range outputs {
		if output.Model != "gemini-3.1-flash-image" {
			t.Fatalf("outputs[%d].Model = %q", i, output.Model)
		}
		if output.Index != startIndex+i {
			t.Fatalf("outputs[%d].Index = %d, want %d", i, output.Index, startIndex+i)
		}
		if output.Total != total {
			t.Fatalf("outputs[%d].Total = %d, want %d", i, output.Total, total)
		}
		if got := output.Raw["upstream_kind"]; got != util.UpstreamKindOpenAIAPI {
			t.Fatalf("outputs[%d].Raw[upstream_kind] = %v, want %q", i, got, util.UpstreamKindOpenAIAPI)
		}
		if len(output.Data) != 1 {
			t.Fatalf("outputs[%d].Data len = %d, want 1", i, len(output.Data))
		}
	}
	if got := outputs[0].Data[0]["b64_json"]; got != "AAA" {
		t.Fatalf("outputs[0] b64_json = %v, want AAA", got)
	}
	if got := outputs[1].Data[0]["b64_json"]; got != "BBB" {
		t.Fatalf("outputs[1] b64_json = %v, want BBB", got)
	}
	if got := outputs[0].Raw["revised_prompt"]; got != "revised-1" {
		t.Fatalf("outputs[0].Raw[revised_prompt] = %v, want revised-1", got)
	}
	if _, has := outputs[1].Raw["revised_prompt"]; has {
		t.Fatalf("outputs[1].Raw should not carry revised_prompt when source is empty")
	}
}

func TestOpenAIImagesResult_ToImageOutputs_NoUpstreamKindOmitsRaw(t *testing.T) {
	result := &OpenAIImagesResult{Data: []OpenAIImageDatum{{B64JSON: "AAA"}}}
	outputs := result.ToImageOutputs("gpt-image-2", 0, 1, "")
	if len(outputs) != 1 {
		t.Fatalf("outputs len = %d", len(outputs))
	}
	if _, has := outputs[0].Raw["upstream_kind"]; has {
		t.Fatalf("Raw[upstream_kind] should be absent when upstreamKind is empty")
	}
}

func TestOpenAIImagesResult_ToImageOutputs_EmptyDataReturnsNil(t *testing.T) {
	result := &OpenAIImagesResult{}
	if outputs := result.ToImageOutputs("gpt-image-2", 0, 0, util.UpstreamKindOpenAIAPI); outputs != nil {
		t.Fatalf("outputs = %#v, want nil for empty data", outputs)
	}
	var nilResult *OpenAIImagesResult
	if outputs := nilResult.ToImageOutputs("gpt-image-2", 0, 0, util.UpstreamKindOpenAIAPI); outputs != nil {
		t.Fatalf("nil result outputs = %#v, want nil", outputs)
	}
}


// === Chat-completions 协议（用于 gemini-3.1-flash-image）单测 ===

// TestGenerateViaChatExtractsDataURLImage 校验响应里 message.content 含
// data:image/...;base64,... data URL 时被正确解析为 OpenAIImageDatum.B64JSON。
//
// 这是聚合服务（如 newapi.qianqianye.com）上 gemini 路径最常见的形态。
func TestGenerateViaChatExtractsDataURLImage(t *testing.T) {
	upstream := &fakeOpenAIImagesUpstream{
		t:          t,
		wantPath:   "/v1/chat/completions",
		respStatus: http.StatusOK,
		respBody: `{"choices":[{"message":{"content":"这是你要的图：data:image/png;base64,iVBORw0KGgoAAAA"}}]}`,
	}
	server := httptest.NewServer(upstream)
	defer server.Close()

	client := &OpenAIImagesClient{HTTPClient: server.Client(), BaseURL: server.URL, APIKey: "sk-test"}
	result, err := client.GenerateViaChat(context.Background(), OpenAIImagesRequest{
		UpstreamModel: util.ImageModelGeminiFlashImage,
		Prompt:        "一只猫",
	})
	if err != nil {
		t.Fatalf("GenerateViaChat error: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("data length = %d, want 1: %#v", len(result.Data), result.Data)
	}
	if result.Data[0].B64JSON != "iVBORw0KGgoAAAA" {
		t.Fatalf("b64_json = %q, want iVBORw0KGgoAAAA", result.Data[0].B64JSON)
	}
	if result.Data[0].URL != "" {
		t.Fatalf("url should be empty for data URL form: %q", result.Data[0].URL)
	}

	// 验证请求体：messages[0].content 是 [{type:text}, ...]，model = gemini-3.1-flash-image。
	var payload map[string]any
	if err := json.Unmarshal(upstream.capturedBody, &payload); err != nil {
		t.Fatalf("decode captured request: %v", err)
	}
	if payload["model"] != util.ImageModelGeminiFlashImage {
		t.Fatalf("model = %v, want %s", payload["model"], util.ImageModelGeminiFlashImage)
	}
	if payload["stream"] != false {
		t.Fatalf("stream = %v, want false", payload["stream"])
	}
}

// TestGenerateViaChatExtractsMarkdownImage 校验响应里 message.content 是
// "看图：![](https://example.com/x.png)" 这种 markdown 形态时，URL 字段被取出。
func TestGenerateViaChatExtractsMarkdownImage(t *testing.T) {
	upstream := &fakeOpenAIImagesUpstream{
		t:          t,
		wantPath:   "/v1/chat/completions",
		respStatus: http.StatusOK,
		respBody:   `{"choices":[{"message":{"content":"![cat](https://cdn.example.com/cat.png)"}}]}`,
	}
	server := httptest.NewServer(upstream)
	defer server.Close()
	client := &OpenAIImagesClient{HTTPClient: server.Client(), BaseURL: server.URL, APIKey: "sk-test"}
	result, err := client.GenerateViaChat(context.Background(), OpenAIImagesRequest{
		UpstreamModel: util.ImageModelGeminiFlashImage,
		Prompt:        "一只猫",
	})
	if err != nil {
		t.Fatalf("GenerateViaChat error: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("data length = %d, want 1", len(result.Data))
	}
	if result.Data[0].URL != "https://cdn.example.com/cat.png" {
		t.Fatalf("url = %q, want https://cdn.example.com/cat.png", result.Data[0].URL)
	}
}

// TestGenerateViaChatExtractsImagesArray 校验 OpenRouter 风格响应：
// choices[0].message.images = [{"image_url":{"url":"..."}}]。
func TestGenerateViaChatExtractsImagesArray(t *testing.T) {
	upstream := &fakeOpenAIImagesUpstream{
		t:          t,
		wantPath:   "/v1/chat/completions",
		respStatus: http.StatusOK,
		respBody: `{"choices":[{"message":{"content":"","images":[{"type":"image_url","image_url":{"url":"https://r2.example.com/a.png"}}]}}]}`,
	}
	server := httptest.NewServer(upstream)
	defer server.Close()
	client := &OpenAIImagesClient{HTTPClient: server.Client(), BaseURL: server.URL, APIKey: "sk-test"}
	result, err := client.GenerateViaChat(context.Background(), OpenAIImagesRequest{
		UpstreamModel: util.ImageModelGeminiFlashImage,
		Prompt:        "一只猫",
	})
	if err != nil {
		t.Fatalf("GenerateViaChat error: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("data length = %d, want 1", len(result.Data))
	}
	if result.Data[0].URL != "https://r2.example.com/a.png" {
		t.Fatalf("url = %q, want https://r2.example.com/a.png", result.Data[0].URL)
	}
}

// TestGenerateViaChatNoImagePayloadIsPermanent 当聚合服务返回 200 但 content
// 是纯文本（没有任何图像数据）时，应归类为 OpenAIImagesErrorPermanent，由
// Engine 把账号-模型 model_states 标记为异常，避免无限轮询同一账号。
func TestGenerateViaChatNoImagePayloadIsPermanent(t *testing.T) {
	upstream := &fakeOpenAIImagesUpstream{
		t:          t,
		wantPath:   "/v1/chat/completions",
		respStatus: http.StatusOK,
		respBody:   `{"choices":[{"message":{"content":"我无法生成这张图。"}}]}`,
	}
	server := httptest.NewServer(upstream)
	defer server.Close()
	client := &OpenAIImagesClient{HTTPClient: server.Client(), BaseURL: server.URL, APIKey: "sk-test"}
	_, err := client.GenerateViaChat(context.Background(), OpenAIImagesRequest{
		UpstreamModel: util.ImageModelGeminiFlashImage,
		Prompt:        "一只猫",
	})
	if err == nil {
		t.Fatal("expected error when chat response has no image payload")
	}
	var typed *OpenAIImagesError
	if !errors.As(err, &typed) {
		t.Fatalf("expected *OpenAIImagesError, got %T", err)
	}
	if typed.Kind != OpenAIImagesErrorPermanent {
		t.Fatalf("error kind = %d, want OpenAIImagesErrorPermanent", typed.Kind)
	}
}

// TestExecuteClassifiesTimeoutAsTimeoutKind 校验 net/http 客户端 timeout
// （context.DeadlineExceeded）被识别为 OpenAIImagesErrorTimeout。
//
// timeout 不消耗 transient 重试预算（参见 OpenAIImagesErrorKind 注释），
// 否则一次慢上游就会把 3 次跨通路重试预算全部耗光。
func TestExecuteClassifiesTimeoutAsTimeoutKind(t *testing.T) {
	// 让上游故意 hang 2s，客户端 50ms 就超时。
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 模拟"等待 headers 期间被 cancel"的场景：直到 ctx 结束都不写响应。
		<-r.Context().Done()
	}))
	defer hang.Close()

	httpClient := *hang.Client()
	httpClient.Timeout = 50 * time.Millisecond
	client := &OpenAIImagesClient{HTTPClient: &httpClient, BaseURL: hang.URL, APIKey: "sk-test"}
	_, err := client.Generate(context.Background(), OpenAIImagesRequest{
		UpstreamModel: util.ImageModelGPTImage2,
		Prompt:        "x",
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var typed *OpenAIImagesError
	if !errors.As(err, &typed) {
		t.Fatalf("expected *OpenAIImagesError, got %T", err)
	}
	if typed.Kind != OpenAIImagesErrorTimeout {
		t.Fatalf("error kind = %d, want OpenAIImagesErrorTimeout (%d)", typed.Kind, OpenAIImagesErrorTimeout)
	}
}
