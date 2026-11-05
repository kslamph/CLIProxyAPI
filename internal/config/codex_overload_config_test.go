package config

import "testing"

// The overload-retry-after-seconds key must load from the canonical upstream
// path and from both historical aliases, and its accessor must distinguish an
// unset key (default 5) from an explicit kill switch (0 / negative).
func TestCodexOverloadRetryAfterSecondsAliasesAndDefault(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    int
		pointer bool
	}{
		{name: "unset defaults to 5", raw: "{}\n", want: DefaultCodexOverloadRetryAfterSeconds, pointer: false},
		{name: "canonical upstream path", raw: "upstream:\n  codex: {overload-retry-after-seconds: 7}\n", want: 7, pointer: true},
		{name: "legacy top-level codex path", raw: "codex: {overload-retry-after-seconds: 7}\n", want: 7, pointer: true},
		{name: "historical oauth providers path", raw: "oauth:\n  providers:\n    codex: {overload-retry-after-seconds: 7}\n", want: 7, pointer: true},
		{name: "explicit zero is the kill switch", raw: "upstream:\n  codex: {overload-retry-after-seconds: 0}\n", want: 0, pointer: true},
		{name: "negative is the kill switch", raw: "upstream:\n  codex: {overload-retry-after-seconds: -3}\n", want: 0, pointer: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, errParse := ParseConfigBytes([]byte(tc.raw))
			if errParse != nil {
				t.Fatal(errParse)
			}
			got := cfg.Codex.OverloadRetryAfterSecondsValue()
			if got != tc.want {
				t.Fatalf("OverloadRetryAfterSecondsValue() = %d, want %d", got, tc.want)
			}
			if !tc.pointer && cfg.Codex.OverloadRetryAfterSeconds != nil {
				t.Fatalf("unset key must leave the pointer nil, got %v", *cfg.Codex.OverloadRetryAfterSeconds)
			}
		})
	}
}
