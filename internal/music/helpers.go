package music

import "strings"

func SafeName(s string) string {
	s = strings.TrimSpace(s)

	if len(s) == 0 {
		return "unknown"
	}

	r := []rune(s)

	if len(r) > 100 {
		r = r[:100]
	}

	return string(r)
}
