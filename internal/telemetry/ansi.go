package telemetry

import "strings"

// stripANSI removes terminal escape sequences from a log body.
//
// Anything that logs through a tinted console handler — guard's own
// console.Setup among them — writes the colour codes into the line itself, so
// they arrive over OTLP as part of the body. A browser renders them as
// literal `[38;2;87;194;110m` noise, and a substring filter over the message
// fails to match text that has a colour code sitting in the middle of it.
// The colours mean nothing outside a terminal, so they are dropped on the way
// in rather than papered over at render time.
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+1 >= len(s) { // A trailing ESC introduces nothing; drop it.
			break
		}
		switch s[i+1] {
		case '[': // CSI: parameters, then intermediates, then one final byte.
			j := i + 2
			for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
				j++
			}
			for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
				j++
			}
			if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
				j++
			}
			i = j
		case ']': // OSC: runs until BEL or the string terminator ESC \.
			j := i + 2
			for j < len(s) {
				if s[j] == 0x07 {
					j++
					break
				}
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					j += 2
					break
				}
				j++
			}
			i = j
		default:
			// Everything else is ESC, zero or more intermediates, one final
			// byte — two bytes for a reset, three for a charset selection
			// like ESC ( B.
			j := i + 1
			for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
		}
	}
	return b.String()
}
