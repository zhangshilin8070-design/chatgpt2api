package service

import (
	"testing"
)

func TestIndustryPromptResolveFiveBranches(t *testing.T) {
	backend := newTestStorageBackend(t)

	s := NewIndustryPromptService(backend)
	// seed writes 10 defaults; use "ecommerce" as the exemplar.

	// Branch 1: empty industry key -> ("", none)
	final, source, err := s.ResolveForUser("user-1", "")
	if err != nil {
		t.Fatalf("branch1: %v", err)
	}
	if final != "" || source != IndustrySourceNone {
		t.Fatalf("branch1: expected empty/none, got %q/%q", final, source)
	}

	// Branch 2: valid enabled preset, no user override -> public_preset
	final, source, err = s.ResolveForUser("user-1", "ecommerce")
	if err != nil {
		t.Fatalf("branch2: %v", err)
	}
	if final == "" || source != IndustrySourcePublicPreset {
		t.Fatalf("branch2: expected public_preset with content, got %q/%q", final, source)
	}

	// Branch 3: user override wins over enabled preset
	if _, err := s.PutOverride("user-1", "ecommerce", "our-store-style"); err != nil {
		t.Fatalf("put override: %v", err)
	}
	final, source, _ = s.ResolveForUser("user-1", "ecommerce")
	if final != "our-store-style" || source != IndustrySourceUserOverride {
		t.Fatalf("branch3: expected user_override with our-store-style, got %q/%q", final, source)
	}

	// Branch 4: user override is empty string (explicit-empty) -> still user_override, empty prompt
	if _, err := s.PutOverride("user-1", "ecommerce", ""); err != nil {
		t.Fatalf("put empty override: %v", err)
	}
	final, source, _ = s.ResolveForUser("user-1", "ecommerce")
	if final != "" || source != IndustrySourceUserOverride {
		t.Fatalf("branch4: expected empty user_override, got %q/%q", final, source)
	}

	// Branch 5: preset deleted, no user override on the deleted key -> none
	presets := s.ListPresets("", "")
	var ecomID string
	for _, p := range presets {
		if p["industry_key"] == "ecommerce" {
			ecomID = p["id"].(string)
			break
		}
	}
	if ecomID == "" {
		t.Fatal("branch5: cannot find ecommerce preset")
	}
	// Fresh user without override
	s.DeleteOverride("user-1", "ecommerce")
	s.DeletePreset(ecomID)
	final, source, _ = s.ResolveForUser("user-2", "ecommerce")
	if final != "" || source != IndustrySourceNone {
		t.Fatalf("branch5: expected none for user without override on deleted preset, got %q/%q", final, source)
	}
}

func TestComposeIndustryFinalPrompt(t *testing.T) {
	final, snippet := ComposeIndustryFinalPrompt("industry.", "user prompt.")
	if final != "industry.\n\nuser prompt." {
		t.Fatalf("compose: unexpected %q", final)
	}
	if snippet != "industry." {
		t.Fatalf("compose snippet: %q", snippet)
	}
	// Empty industry -> user prompt unchanged, empty snippet
	final, snippet = ComposeIndustryFinalPrompt("", "user prompt.")
	if final != "user prompt." || snippet != "" {
		t.Fatalf("compose empty: %q / %q", final, snippet)
	}
}
