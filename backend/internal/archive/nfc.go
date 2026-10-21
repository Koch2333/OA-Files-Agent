package archive

import "unicode/utf8"

// The standard library has no Unicode normalisation, so this is a deliberately
// small NFC composer covering Latin letters with the common combining marks
// (the usual macOS/NFD source of mismatched names). CJK text is unaffected.
// Other decomposed sequences are left untouched (documented limitation).
var composeTable = map[rune]map[rune]rune{}

func init() {
	add := func(mark rune, bases, composed string) {
		b := []rune(bases)
		c := []rune(composed)
		m := composeTable[mark]
		if m == nil {
			m = map[rune]rune{}
			composeTable[mark] = m
		}
		for i := range b {
			m[b[i]] = c[i]
		}
	}
	add(0x0300, "AEIOUaeiou", "ÀÈÌÒÙàèìòù")
	add(0x0301, "AEIOUYaeiouy", "ÁÉÍÓÚÝáéíóúý")
	add(0x0302, "AEIOUaeiou", "ÂÊÎÔÛâêîôû")
	add(0x0303, "ANOano", "ÃÑÕãñõ")
	add(0x0308, "AEIOUYaeiouy", "ÄËÏÖÜŸäëïöüÿ")
	add(0x030A, "Aa", "Åå")
	add(0x0327, "Cc", "Çç")
	add(0x030C, "SsZzCcRrEe", "ŠšŽžČčŘřĚě")
}

func nfc(s string) string {
	if !utf8.ValidString(s) {
		s = string([]rune(s))
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if m, ok := composeTable[r]; ok && len(out) > 0 {
			if c, ok := m[out[len(out)-1]]; ok {
				out[len(out)-1] = c
				continue
			}
		}
		out = append(out, r)
	}
	return string(out)
}
