// Package i18n picks between a Chinese and an English message for the few strings the installer shows to people who did not
// choose Chinese. The language comes from OA_LANG (set by install.sh from its language question / --lang): "en*" selects English,
// anything else (including unset) keeps the historical Chinese text.
package i18n

import (
	"os"
	"strings"
)

// English reports whether English output was requested.
func English() bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(os.Getenv("OA_LANG"))), "en")
}

// T returns en when English was requested, zh otherwise.
func T(zh, en string) string {
	if English() {
		return en
	}
	return zh
}
