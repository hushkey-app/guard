package telemetry

import "testing"

func TestStripANSI(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "no escapes here", "no escapes here"},
		{"empty", "", ""},
		{
			"tinted console line",
			"\x1b[38;2;87;194;110m[09:49:25.015] [71643]\x1b[39m [http-client] Generated in 193ms",
			"[09:49:25.015] [71643] [http-client] Generated in 193ms",
		},
		{"reset", "\x1b[0mplain\x1b[m", "plain"},
		{"cursor move", "a\x1b[2Kb", "ab"},
		{"osc bel", "\x1b]0;title\x07body", "body"},
		{"osc st", "\x1b]8;;http://x\x1b\\link", "link"},
		{"two byte escape", "a\x1b(Bb", "ab"},
		{"truncated csi", "a\x1b[38;2;87", "a"},
		{"lone escape", "a\x1b", "a"},
		{"brackets survive", "[not an escape]", "[not an escape]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripANSI(c.in); got != c.want {
				t.Fatalf("stripANSI(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
