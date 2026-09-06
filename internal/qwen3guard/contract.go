package qwen3guard

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
)

// Safety levels are the exact Qwen3Guard-Gen output tokens. Source: QwenLM/Qwen3Guard
// README regex and the CT-8B chat_template (URLs documented in DESIGN.md section 1.1).
const (
	SafetySafe          = "Safe"
	SafetyUnsafe        = "Unsafe"
	SafetyControversial = "Controversial"
)

// SafetyLevels is the official severity token order from the Qwen3Guard README regex.
var SafetyLevels = []string{SafetySafe, SafetyUnsafe, SafetyControversial}

// PromptCategories is the exact nine-token input category list from the Qwen3Guard
// README parser regex and CT-8B prompt category branch (DESIGN.md section 1.3).
var PromptCategories = []string{"Violent", "Non-violent Illegal Acts", "Sexual Content or Sexual Acts", "PII", "Suicide & Self-Harm", "Unethical Acts", "Politically Sensitive Topics", "Copyright Violation", "Jailbreak"}

// Verdict is the locally validated structured assessment returned by upstream.
type Verdict struct {
	Safety     string   `json:"safety"`
	Categories []string `json:"categories"`
}

// aliases follows sub2api categoryAliases in backend/internal/securityaudit/prompt_qwen3guard.go
// and adds the model-card full spelling Personally Identifiable Information (DESIGN.md 1.3).
var aliases = map[string]string{
	"violent": "Violent", "violence": "Violent",
	"non violent illegal acts": "Non-violent Illegal Acts", "non-violent illegal acts": "Non-violent Illegal Acts",
	"sexual content or sexual acts": "Sexual Content or Sexual Acts", "sexual": "Sexual Content or Sexual Acts",
	"pii": "PII", "personal identifying information": "PII", "personal identifiable information": "PII", "personally identifiable information": "PII",
	"suicide self harm": "Suicide & Self-Harm", "suicide and self harm": "Suicide & Self-Harm", "suicide & self-harm": "Suicide & Self-Harm",
	"unethical acts": "Unethical Acts", "unethical": "Unethical Acts",
	"politically sensitive topics": "Politically Sensitive Topics", "political": "Politically Sensitive Topics",
	"copyright violation": "Copyright Violation", "copyright": "Copyright Violation", "jailbreak": "Jailbreak", "prompt injection": "Jailbreak",
}

func key(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizeUpstreamCategories canonicalizes aliases case/separator-insensitively,
// drops None/empty, and preserves upstream order. Alias and None handling cite
// sub2api prompt_qwen3guard.go categoryAliases and parser token loop.
func NormalizeUpstreamCategories(values []string) ([]string, error) {
	allowedSet := make(map[string]bool, len(PromptCategories))
	for _, c := range PromptCategories {
		allowedSet[key(c)] = true
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.EqualFold(trimmed, "none") || strings.EqualFold(trimmed, "n/a") {
			continue
		}
		k := key(raw)
		canonical, ok := "", false
		for alias, target := range aliases {
			if key(alias) == k {
				canonical, ok = target, true
				break
			}
		}
		if !ok {
			for _, c := range PromptCategories {
				if key(c) == k {
					canonical, ok = c, true
					break
				}
			}
		}
		if !ok || !allowedSet[key(canonical)] {
			return nil, fmt.Errorf("unknown category %q", raw)
		}
		if !seen[key(canonical)] {
			seen[key(canonical)] = true
			out = append(out, canonical)
		}
	}
	return out, nil
}

// ValidateVerdict canonicalizes and validates safety, categories, and the Safe
// consistency invariant from DESIGN.md section 3.2.
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

// Render formats the two-line Qwen3Guard prompt contract. Labels cite the
// Qwen3Guard README regex and comma-space separator cites sub2api parsing.
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

// PromptSystemPolicy contains gateway-authored task/output framing plus the model-card
// Safety Policy definitions. The Qwen3Guard chat-template scaffolding is deliberately
// not replicated, as required by DESIGN.md section 1.5.
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
