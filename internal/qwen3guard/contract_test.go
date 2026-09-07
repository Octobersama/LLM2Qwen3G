package qwen3guard

import (
	"strings"
	"testing"
)

func TestRenderGolden(t *testing.T) {
	for _, tc := range []struct {
		name, safety string
		cats         []string
		want         string
	}{
		{"prompt safe", "Safe", nil, "Safety: Safe\nCategories: None"},
		{"prompt unsafe multi", "Unsafe", []string{"Violent", "PII"}, "Safety: Unsafe\nCategories: Violent, PII"},
		{"prompt controversial", "Controversial", []string{"Jailbreak"}, "Safety: Controversial\nCategories: Jailbreak"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Render(tc.safety, tc.cats); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestCategoryNormalizationAliases(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"VIOLENCE", "Violent"}, {"non_violent-illegal acts", "Non-violent Illegal Acts"},
		{"Sexual", "Sexual Content or Sexual Acts"}, {"PERSONALLY IDENTIFIABLE INFORMATION", "PII"},
		{"personal-identifying_information", "PII"}, {"suicide_and_self-harm", "Suicide & Self-Harm"},
		{"political", "Politically Sensitive Topics"}, {"copyright", "Copyright Violation"}, {"prompt_injection", "Jailbreak"},
		{"None", ""}, {"n/a", ""}, {"", ""},
	} {
		got, err := NormalizeUpstreamCategories([]string{tc.in})
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if tc.want == "" {
			if len(got) != 0 {
				t.Fatalf("%q got %#v", tc.in, got)
			}
		} else if len(got) != 1 || got[0] != tc.want {
			t.Fatalf("%q got %#v want %q", tc.in, got, tc.want)
		}
	}
}

func TestOfficialCategoryLists(t *testing.T) {
	wantPrompt := []string{"Violent", "Non-violent Illegal Acts", "Sexual Content or Sexual Acts", "PII", "Suicide & Self-Harm", "Unethical Acts", "Politically Sensitive Topics", "Copyright Violation", "Jailbreak"}
	if len(PromptCategories) != len(wantPrompt) {
		t.Fatal("prompt category count mismatch")
	}
	for i, v := range wantPrompt {
		if PromptCategories[i] != v {
			t.Fatalf("prompt[%d]=%q", i, PromptCategories[i])
		}
	}
}

func TestValidateVerdict(t *testing.T) {
	if _, err := ValidateVerdict(Verdict{Safety: "Safe", Categories: []string{"Violent"}}); err == nil {
		t.Fatal("Safe with categories must fail")
	}
	v, err := ValidateVerdict(Verdict{Safety: "unsafe", Categories: []string{"violence", "Violent"}})
	if err != nil || v.Safety != "Unsafe" || len(v.Categories) != 1 || v.Categories[0] != "Violent" {
		t.Fatalf("got %#v err %v", v, err)
	}
	// Non-Safe verdicts with empty categories are off-contract (CT-8B pairs
	// Unsafe/Controversial with a category list; None is safe-only) and must be
	// rejected before rendering, not silently rendered as "Categories: None".
	for _, safety := range []string{"Unsafe", "Controversial"} {
		if _, err := ValidateVerdict(Verdict{Safety: safety, Categories: nil}); err == nil {
			t.Fatalf("%s with no categories must fail", safety)
		}
	}
}

func TestSystemPolicyAppendix(t *testing.T) {
	stock := SystemPolicy("")
	// Stock policy: official head + JSON tail, no appendix section.
	if !strings.Contains(stock, "Qwen3Guard Safety Policy") || !strings.Contains(stock, "Output only valid JSON") {
		t.Fatal("stock policy missing head or tail")
	}
	if strings.Contains(stock, "Additional audit focus") {
		t.Fatal("stock policy must not contain appendix section")
	}
	// Appendix is inserted between policy and JSON tail; tail stays last so
	// the operator cannot displace the output-format instruction.
	custom := SystemPolicy("Focus extra hard on PII leaks.")
	if !strings.Contains(custom, "Additional audit focus") || !strings.Contains(custom, "Focus extra hard on PII leaks.") {
		t.Fatal("appendix missing from custom policy")
	}
	if !strings.HasSuffix(custom, "Output only valid JSON matching the requested fields and enum values.") {
		t.Fatal("JSON tail must remain last")
	}
	// Whitespace-only appendix is treated as empty.
	if SystemPolicy("   \n ") != stock {
		t.Fatal("whitespace appendix must equal stock policy")
	}
}
