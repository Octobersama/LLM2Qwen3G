package qwen3guard

import "testing"

func TestParseUpstreamJSON(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      Verdict
		wantErr   bool
	}{
		{"valid", `{"safety":"Safe","categories":[]}`, Verdict{Safety: "Safe", Categories: []string{}}, false},
		{"fenced", "```json\n{\"safety\":\"Unsafe\",\"categories\":[\"Violent\"]}\n```", Verdict{Safety: "Unsafe", Categories: []string{"Violent"}}, false},
		{"junk", `prefix {"safety":"Safe","categories":["None"]} suffix`, Verdict{Safety: "Safe", Categories: []string{"None"}}, false},
		{"invalid", `{nope}`, Verdict{}, true},
		{"missing safety", `{"categories":[]}`, Verdict{}, true},
		{"missing categories", `{"safety":"Safe"}`, Verdict{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseUpstreamJSON(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil || got.Safety != tc.want.Safety || len(got.Categories) != len(tc.want.Categories) {
				t.Fatalf("got %#v err=%v", got, err)
			}
		})
	}
}
func TestValidateJSONVariants(t *testing.T) {
	for _, tc := range []struct {
		name       string
		v          Verdict
		wantSafety string
		wantErr    bool
	}{
		{"case safety", Verdict{Safety: "sAfE", Categories: []string{"none"}}, "Safe", false},
		{"unknown", Verdict{Safety: "Unsafe", Categories: []string{"mystery"}}, "", true},
		{"safe inconsistency", Verdict{Safety: "Safe", Categories: []string{"PII"}}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateVerdict(tc.v)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil || got.Safety != tc.wantSafety {
				t.Fatalf("got %#v err=%v", got, err)
			}
		})
	}
}
