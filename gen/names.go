package gen

import (
	"strings"
	"unicode"
)

// initialisms Go writes in capitals (golint's list, trimmed), between
// blanks.
const initialisms = " acl api cpu css db dns eof grpc html http https id io ip json oidc sql ssh ssl tcp tls" +
	" ttl udp ui uid uri url uuid xml yaml "

// goName turns a JSON key (snake_case, kebab-case, camelCase, $ref...)
// into an exported Go identifier.
func goName(key string) string {
	var out strings.Builder
	for _, word := range words(key) {
		if lower := strings.ToLower(word); strings.Contains(initialisms, " "+lower+" ") {
			out.WriteString(strings.ToUpper(lower))
			continue
		}
		runes := []rune(word)
		out.WriteString(string(unicode.ToUpper(runes[0])) + string(runes[1:]))
	}
	name := out.String()
	switch {
	case name == "":
		return "Field"
	case unicode.IsDigit([]rune(name)[0]):
		return "X" + name
	}
	return name
}

// words splits key at what is not a letter or digit, and before an
// upper case letter that follows a lower case one.
func words(key string) []string {
	var out []string
	cur := make([]rune, 0, len(key))
	prev := rune(0)
	for _, char := range key {
		switch {
		case !unicode.IsLetter(char) && !unicode.IsDigit(char):
			out, cur = flush(out, cur), cur[:0]
		case unicode.IsUpper(char) && unicode.IsLower(prev):
			out, cur = flush(out, cur), append(cur[:0], char)
		default:
			cur = append(cur, char)
		}
		prev = char
	}
	return flush(out, cur)
}

func flush(out []string, cur []rune) []string {
	if len(cur) == 0 {
		return out
	}
	return append(out, string(cur))
}
