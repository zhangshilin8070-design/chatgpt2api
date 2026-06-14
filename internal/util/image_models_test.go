package util

import "testing"

func TestImageGenerationModelSetExcludesTextModels(t *testing.T) {
	for _, model := range []string{
		ImageModelAuto,
		ImageModelGPTImage2,
		ImageModelCodexGPTImage2,
		ImageModelGeminiFlashImage,
	} {
		if !IsImageGenerationModel(model) {
			t.Fatalf("IsImageGenerationModel(%q) = false, want true", model)
		}
	}

	for _, model := range []string{
		ImageModelGPTMini,
		ImageModelGPT53Mini,
		ImageModelGPT5,
		ImageModelGPT51,
		ImageModelGPT52,
		ImageModelGPT53,
		ImageModelGPT54,
		ImageModelGPT55,
	} {
		if IsImageGenerationModel(model) {
			t.Fatalf("IsImageGenerationModel(%q) = true, want false", model)
		}
	}
}

func TestResponsesImageToolModelsIncludeTextModels(t *testing.T) {
	for _, model := range []string{
		ImageModelAuto,
		ImageModelGPTImage2,
		ImageModelCodexGPTImage2,
		ImageModelGeminiFlashImage,
		ImageModelGPT5,
		ImageModelGPT54,
		ImageModelGPT55,
	} {
		if !IsResponsesImageToolModel(model) {
			t.Fatalf("IsResponsesImageToolModel(%q) = false, want true", model)
		}
	}
	if IsImageGenerationModel(ImageModelGPT55) {
		t.Fatalf("IsImageGenerationModel(%q) = true, want false for /v1/images routes", ImageModelGPT55)
	}
}

func TestModelListIncludesTextAndImageModels(t *testing.T) {
	wantOrder := []string{
		ImageModelGPTImage2,
		ImageModelCodexGPTImage2,
		ImageModelGeminiFlashImage,
		ImageModelAuto,
		ImageModelGPTMini,
		ImageModelGPT53Mini,
		ImageModelGPT5,
		ImageModelGPT51,
		ImageModelGPT52,
		ImageModelGPT53,
		ImageModelGPT54,
		ImageModelGPT55,
	}
	gotOrder := ModelList()
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("len(ModelList()) = %d, want %d: %#v", len(gotOrder), len(wantOrder), gotOrder)
	}
	for index, want := range wantOrder {
		if gotOrder[index] != want {
			t.Fatalf("ModelList()[%d] = %q, want %q; full list: %#v", index, gotOrder[index], want, gotOrder)
		}
	}

	got := map[string]struct{}{}
	for _, model := range ModelList() {
		got[model] = struct{}{}
	}

	for _, model := range []string{
		ImageModelAuto,
		ImageModelGPTImage2,
		ImageModelCodexGPTImage2,
		ImageModelGeminiFlashImage,
		ImageModelGPTMini,
		ImageModelGPT53Mini,
		ImageModelGPT5,
		ImageModelGPT53,
		ImageModelGPT54,
		ImageModelGPT55,
	} {
		if _, ok := got[model]; !ok {
			t.Fatalf("ModelList() missing %q", model)
		}
	}
}

func TestBucketForModel(t *testing.T) {
	cases := []struct {
		name       string
		external   string
		wantBucket string
		wantErr    bool
	}{
		{name: "gpt-image-2 maps to bucket_a", external: ImageModelGPTImage2, wantBucket: ImageBucketA},
		{name: "codex-gpt-image-2 maps to bucket_b", external: ImageModelCodexGPTImage2, wantBucket: ImageBucketB},
		{name: "gemini-3.1-flash-image maps to bucket_b", external: ImageModelGeminiFlashImage, wantBucket: ImageBucketB},
		{name: "auto returns error", external: ImageModelAuto, wantErr: true},
		{name: "empty string returns error", external: "", wantErr: true},
		{name: "unknown dall-e-3 returns error", external: "dall-e-3", wantErr: true},
		{name: "unknown gpt-5 returns error", external: ImageModelGPT5, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BucketForModel(tc.external)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("BucketForModel(%q) = (%q, nil), want error", tc.external, got)
				}
				if got != "" {
					t.Fatalf("BucketForModel(%q) returned bucket %q on error, want empty", tc.external, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("BucketForModel(%q) returned unexpected error: %v", tc.external, err)
			}
			if got != tc.wantBucket {
				t.Fatalf("BucketForModel(%q) = %q, want %q", tc.external, got, tc.wantBucket)
			}
		})
	}
}

func TestUpstreamModelForExternal(t *testing.T) {
	cases := []struct {
		name         string
		external     string
		wantUpstream string
		wantErr      bool
	}{
		{name: "gpt-image-2 maps to gpt-image-2", external: ImageModelGPTImage2, wantUpstream: ImageModelGPTImage2},
		{name: "codex-gpt-image-2 maps to gpt-image-2", external: ImageModelCodexGPTImage2, wantUpstream: ImageModelGPTImage2},
		{name: "gemini-3.1-flash-image maps to gemini-3.1-flash-image", external: ImageModelGeminiFlashImage, wantUpstream: ImageModelGeminiFlashImage},
		{name: "auto returns error", external: ImageModelAuto, wantErr: true},
		{name: "empty string returns error", external: "", wantErr: true},
		{name: "unknown dall-e-3 returns error", external: "dall-e-3", wantErr: true},
		{name: "unknown gpt-5 returns error", external: ImageModelGPT5, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UpstreamModelForExternal(tc.external)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("UpstreamModelForExternal(%q) = (%q, nil), want error", tc.external, got)
				}
				if got != "" {
					t.Fatalf("UpstreamModelForExternal(%q) returned upstream %q on error, want empty", tc.external, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("UpstreamModelForExternal(%q) returned unexpected error: %v", tc.external, err)
			}
			if got != tc.wantUpstream {
				t.Fatalf("UpstreamModelForExternal(%q) = %q, want %q", tc.external, got, tc.wantUpstream)
			}
		})
	}
}
