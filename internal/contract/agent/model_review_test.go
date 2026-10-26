package agent

import "testing"

func TestCompletionReviewProfileSelection(t *testing.T) {
	c := ModelConfiguration{DefaultProfile: "default", DecisionProfile: "business", MemoryExtractionProfile: "memory"}
	if got := c.ProfileID("completion_review"); got != "business" {
		t.Fatalf("review must inherit decision, got %q", got)
	}
	c.CompletionReviewProfile = "review"
	if c.ProfileID("completion_review") != "review" || c.ProfileID("decision") != "business" || c.ProfileID("memory_extraction") != "memory" {
		t.Fatal("review selection changed other purposes", c)
	}
	c.Profiles = map[string]ModelProfile{}
	for _, id := range []string{"default", "business", "memory"} {
		c.Profiles[id] = ModelProfile{APIType: "compatible_chat", Model: "synthetic", BaseURL: "https://example.com", APIKeyRef: "MODEL_KEY"}
	}
	if err := c.Validate(); ErrorCode(err) != "model_selection_invalid" {
		t.Fatal("missing review profile accepted", err)
	}
	c.Profiles["review"] = c.Profiles["business"]
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
