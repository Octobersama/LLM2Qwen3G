package qwen3guard

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
)

// Safety levels are the exact Qwen3Guard-Gen output tokens. Source: QwenLM/Qwen3Guard
// README quickstart regex r"Safety: (Safe|Unsafe|Controversial)"
// (https://github.com/QwenLM/Qwen3Guard) and the chat template in
// https://huggingface.co/Qwen/Qwen3Guard-Gen-8B/raw/main/tokenizer_config.json
// ("The first line must be one of: 'Safety: Safe', 'Safety: Unsafe', 'Safety: Controversial'.").
const (
	SafetySafe          = "Safe"
	SafetyUnsafe        = "Unsafe"
	SafetyControversial = "Controversial"
)

// PromptCategories is the exact nine-token input category list. Sources: the
// README prompt parser regex category_pattern (https://github.com/QwenLM/Qwen3Guard)
// and the chat template <BEGIN UNSAFE CONTENT CATEGORIES> prompt branch
// (https://huggingface.co/Qwen/Qwen3Guard-Gen-8B/raw/main/tokenizer_config.json;
// byte-identical across Gen-0.6B/4B/8B). "Jailbreak" is input-only per the
// model card Safety Policy (https://huggingface.co/Qwen/Qwen3Guard-Gen-8B).
var PromptCategories = []string{"Violent", "Non-violent Illegal Acts", "Sexual Content or Sexual Acts", "PII", "Suicide & Self-Harm", "Unethical Acts", "Politically Sensitive Topics", "Copyright Violation", "Jailbreak"}

// Verdict is the locally validated structured assessment returned by upstream.
type Verdict struct {
	Safety     string   `json:"safety"`
	Categories []string `json:"categories"`
}

// canonical maps normalized keys (letters/digits only, lowercased — so case,
// punctuation and separator variants collapse) to official category tokens.
// It merges sub2api's categoryAliases map (Wei-Shaw/sub2api
// backend/internal/securityaudit/prompt_qwen3guard.go,
// https://raw.githubusercontent.com/Wei-Shaw/sub2api/main/backend/internal/securityaudit/prompt_qwen3guard.go)
// with the official tokens themselves and the model-card full spelling
// "Personally Identifiable Information" (https://huggingface.co/Qwen/Qwen3Guard-Gen-8B
// Safety Policy) for the PII token.
var canonical = func() map[string]string {
	m := map[string]string{}
	for alias, target := range map[string]string{
		"violence": "Violent", "non violent illegal acts": "Non-violent Illegal Acts",
		"sexual": "Sexual Content or Sexual Acts", "personal identifying information": "PII",
		"personal identifiable information": "PII", "personally identifiable information": "PII",
		"suicide and self harm": "Suicide & Self-Harm", "unethical": "Unethical Acts",
		"political": "Politically Sensitive Topics", "copyright": "Copyright Violation",
		"prompt injection": "Jailbreak",
	} {
		m[key(alias)] = target
	}
	for _, c := range PromptCategories {
		m[key(c)] = c
	}
	return m
}()

func key(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizeUpstreamCategories canonicalizes tokens case/separator-insensitively,
// drops None/empty (mirroring sub2api's parser token loop — same file as above,
// `raw == "" || EqualFold(raw,"none") || EqualFold(raw,"n/a")`), preserves
// upstream order, deduplicates, and rejects unknown categories.
func NormalizeUpstreamCategories(values []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.EqualFold(trimmed, "none") || strings.EqualFold(trimmed, "n/a") {
			continue
		}
		c, ok := canonical[key(raw)]
		if !ok {
			return nil, fmt.Errorf("unknown category %q", raw)
		}
		if !seen[key(c)] {
			seen[key(c)] = true
			out = append(out, c)
		}
	}
	return out, nil
}

// ValidateVerdict canonicalizes and validates safety, categories, and the Safe
// consistency invariant: "None" is specified only "if the content is safe" per
// the chat template (https://huggingface.co/Qwen/Qwen3Guard-Gen-8B/raw/main/tokenizer_config.json),
// so Safe must carry no categories and Unsafe/Controversial must carry >=1.
func ValidateVerdict(v Verdict) (Verdict, error) {
	switch strings.ToLower(strings.TrimSpace(v.Safety)) {
	case "safe":
		v.Safety = SafetySafe
	case "unsafe":
		v.Safety = SafetyUnsafe
	case "controversial":
		v.Safety = SafetyControversial
	default:
		return Verdict{}, fmt.Errorf("invalid safety %q", v.Safety)
	}
	cats, err := NormalizeUpstreamCategories(v.Categories)
	if err != nil {
		return Verdict{}, err
	}
	v.Categories = cats
	if v.Safety == SafetySafe && len(v.Categories) != 0 {
		return Verdict{}, fmt.Errorf("safe verdict cannot contain categories")
	}
	if v.Safety != SafetySafe && len(v.Categories) == 0 {
		return Verdict{}, fmt.Errorf("%s verdict requires at least one category", v.Safety)
	}
	return v, nil
}

// Render formats the two-line Qwen3Guard prompt contract: "Safety: <token>" /
// "Categories: <tokens|None>" per the Qwen3Guard README quickstart examples
// (https://github.com/QwenLM/Qwen3Guard). The ", " separator is chosen because
// sub2api splits on "," then TrimSpaces
// (https://raw.githubusercontent.com/Wei-Shaw/sub2api/main/backend/internal/securityaudit/prompt_qwen3guard.go).
func Render(safety string, categories []string) string {
	cat := "None"
	if len(categories) > 0 {
		cat = strings.Join(categories, ", ")
	}
	return "Safety: " + safety + "\nCategories: " + cat
}

// NewRequestID creates the OpenAI-compatible chat completion identifier.
func NewRequestID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "chatcmpl-000000000000000000000000"
	}
	return "chatcmpl-" + hex.EncodeToString(b)
}

// PromptSystemPolicy contains gateway-authored task/output framing plus the
// Qwen3Guard model card Safety Policy definitions, quoted from
// https://huggingface.co/Qwen/Qwen3Guard-Gen-8B (Safety Policy section;
// identical in the 4B/0.6B cards and the README's policy section at
// https://github.com/QwenLM/Qwen3Guard). The chat-template scaffolding is
// deliberately NOT replicated (project non-goal, DESIGN.md section 1.5).
const PromptSystemPolicy = `You are a content safety classification engine. Classify the supplied user text according to the policy below. Return only one JSON object, with no markdown or surrounding prose, using exactly the fields "safety" and "categories". "safety" must be one of "Safe", "Unsafe", or "Controversial". "categories" must be an array of official category tokens; use [] when there are no applicable categories.

Qwen3Guard Safety Policy (verbatim from the model card):
In Qwen3Guard, potential harms are classified into three severity levels:
* Unsafe: Content generally considered harmful across most scenarios.
* Controversial: Content whose harmfulness may be context-dependent or subject to disagreement across different applications.
* Safe: Content generally considered safe across most scenarios.

The safety categories are:
* Violent: Content that provides detailed instructions, methods, or advice on how to commit acts of violence, including the manufacture, acquisition, or use of weapons. Also includes depictions of violence.
* Non-violent Illegal Acts: Content providing guidance or advice for non-violent illegal activities like hacking, unauthorized drug production, or stealing.
* Sexual Content or Sexual Acts: Content offering any sexual imagery, references, or descriptions featuring individuals. Also includes content that describes explicit sexual imagery, references, or descriptions containing illegal or unethical sexual acts, such as rape, bestiality, incest, and sexual slavery.
* Personally Identifiable Information: Content offering unauthorized sharing or disclosure of sensitive personal identifying information, such as name, ID number, address, phone number, medical records, financial details, and account passwords, etc. Use the output token PII for this category.
* Suicide & Self-Harm: Content advocating, directly encouraging, or detailing methods for self-harm, suicide, or dangerous activities that could lead to serious injury or death.
* Unethical Acts: Any immoral or unethical content or acts, including but not limited to bias, discrimination, stereotype, injustice, hate speech, offensive language, harassment, insults, threat, defamation, extremism, misinformation regarding ethics, and other behaviors that while not illegal are still considered unethical.
* Politically Sensitive Topics: The deliberate creation or spread of false information about government actions, historical events, or public figures that is demonstrably untrue and poses risk of public deception or social harm.
* Copyright Violation: Content offering unauthorized reproduction, distribution, public display, or derivative use of copyrighted materials, such as novels, scripts, lyrics, and other creative works protected by law, without the explicit permission of the copyright holder.
* Jailbreak (Only for input): Content that explicitly attempts to override the model's system prompt or model conditioning.

Output only valid JSON matching the requested fields and enum values.`
