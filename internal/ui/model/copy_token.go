package model

import (
	"regexp"
	"strings"

	"github.com/clipperhouse/displaywidth"
)

// copyTokenPatterns match the tokens people double-click to copy whole,
// which word segmentation would split at their punctuation. Earlier
// patterns win, so a UUID inside a URL selects the URL.
var copyTokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:https?|ftp|file)://[^\s<>"'\x60]+`),
	regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`),
	// Linear-style issue IDs such as ENG-1234.
	regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-[0-9]+\b`),
}

var tokenBracketOpeners = map[byte]byte{')': '(', ']': '[', '}': '{'}

// copyTokenAt returns the display columns (end exclusive) of the URL, UUID
// or issue ID in line that covers col.
func copyTokenAt(line string, col int) (startCol, endCol int, ok bool) {
	for _, pattern := range copyTokenPatterns {
		for _, loc := range pattern.FindAllStringIndex(line, -1) {
			token := trimTokenTail(line[loc[0]:loc[1]])
			startCol = displaywidth.String(line[:loc[0]])
			endCol = startCol + displaywidth.String(token)
			if col >= startCol && col < endCol {
				return startCol, endCol, true
			}
		}
	}
	return 0, 0, false
}

// trimTokenTail drops the sentence punctuation and unbalanced closing
// brackets that prose puts after a URL, keeping ones the URL itself needs,
// as in https://en.wikipedia.org/wiki/Go_(programming_language).
func trimTokenTail(token string) string {
	for token != "" {
		last := token[len(token)-1]
		switch last {
		case '.', ',', ';', ':', '!', '?':
			token = token[:len(token)-1]
			continue
		}
		opener, isCloser := tokenBracketOpeners[last]
		if !isCloser || strings.Count(token, string(opener)) >= strings.Count(token, string(last)) {
			return token
		}
		token = token[:len(token)-1]
	}
	return token
}
