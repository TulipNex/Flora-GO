package msgbuilder

import (
	"strings"
	"unicode"

	"github.com/polymorfa/hypermeow/proto/waAICommonDeprecated"
	"google.golang.org/protobuf/proto"
)

var codeTypeNames = map[int32]string{
	0: "DEFAULT",
	1: "KEYWORD",
	2: "METHOD",
	3: "STR",
	4: "NUMBER",
	5: "COMMENT",
}

var langKeywords = map[string]map[string]struct{}{
	"go": {
		"func": {}, "package": {}, "import": {}, "return": {}, "var": {}, "const": {},
		"type": {}, "struct": {}, "interface": {}, "if": {}, "else": {}, "for": {},
		"range": {}, "switch": {}, "case": {}, "select": {}, "default": {}, "go": {},
		"defer": {}, "chan": {}, "map": {}, "nil": {}, "true": {}, "false": {},
		"make": {}, "new": {}, "len": {}, "cap": {}, "append": {}, "copy": {},
	},
	"js": {
		"function": {}, "return": {}, "const": {}, "let": {}, "var": {}, "if": {},
		"else": {}, "for": {}, "while": {}, "switch": {}, "case": {}, "break": {},
		"continue": {}, "class": {}, "import": {}, "export": {}, "from": {}, "async": {},
		"await": {}, "new": {}, "this": {}, "try": {}, "catch": {}, "throw": {},
		"typeof": {}, "instanceof": {}, "null": {}, "undefined": {}, "true": {}, "false": {},
	},
	"javascript": {
		"function": {}, "return": {}, "const": {}, "let": {}, "var": {}, "if": {},
		"else": {}, "for": {}, "while": {}, "switch": {}, "case": {}, "break": {},
		"continue": {}, "class": {}, "import": {}, "export": {}, "from": {}, "async": {},
		"await": {}, "new": {}, "this": {}, "try": {}, "catch": {}, "throw": {},
		"typeof": {}, "instanceof": {}, "null": {}, "undefined": {}, "true": {}, "false": {},
	},
	"ts": {
		"function": {}, "return": {}, "const": {}, "let": {}, "var": {}, "if": {},
		"else": {}, "for": {}, "while": {}, "switch": {}, "case": {}, "break": {},
		"continue": {}, "class": {}, "interface": {}, "type": {}, "import": {}, "export": {},
		"from": {}, "async": {}, "await": {}, "new": {}, "this": {}, "try": {}, "catch": {},
		"throw": {}, "typeof": {}, "instanceof": {}, "null": {}, "undefined": {}, "true": {}, "false": {},
	},
	"typescript": {
		"function": {}, "return": {}, "const": {}, "let": {}, "var": {}, "if": {},
		"else": {}, "for": {}, "while": {}, "switch": {}, "case": {}, "break": {},
		"continue": {}, "class": {}, "interface": {}, "type": {}, "import": {}, "export": {},
		"from": {}, "async": {}, "await": {}, "new": {}, "this": {}, "try": {}, "catch": {},
		"throw": {}, "typeof": {}, "instanceof": {}, "null": {}, "undefined": {}, "true": {}, "false": {},
	},
	"python": {
		"def": {}, "return": {}, "if": {}, "elif": {}, "else": {}, "for": {},
		"while": {}, "in": {}, "import": {}, "from": {}, "as": {}, "class": {},
		"try": {}, "except": {}, "finally": {}, "with": {}, "pass": {}, "break": {},
		"continue": {}, "lambda": {}, "async": {}, "await": {}, "yield": {}, "None": {},
		"True": {}, "False": {}, "not": {}, "and": {}, "or": {}, "is": {}, "global": {},
	},
	"py": {
		"def": {}, "return": {}, "if": {}, "elif": {}, "else": {}, "for": {},
		"while": {}, "in": {}, "import": {}, "from": {}, "as": {}, "class": {},
		"try": {}, "except": {}, "finally": {}, "with": {}, "pass": {}, "break": {},
		"continue": {}, "lambda": {}, "async": {}, "await": {}, "yield": {}, "None": {},
		"True": {}, "False": {}, "not": {}, "and": {}, "or": {}, "is": {}, "global": {},
	},
	"bash": {
		"if": {}, "then": {}, "else": {}, "elif": {}, "fi": {}, "for": {},
		"in": {}, "do": {}, "done": {}, "while": {}, "case": {}, "esac": {},
		"echo": {}, "exit": {}, "return": {}, "function": {}, "export": {}, "local": {},
	},
	"sh": {
		"if": {}, "then": {}, "else": {}, "elif": {}, "fi": {}, "for": {},
		"in": {}, "do": {}, "done": {}, "while": {}, "case": {}, "esac": {},
		"echo": {}, "exit": {}, "return": {}, "function": {}, "export": {}, "local": {},
	},
	"sql": {
		"select": {}, "from": {}, "where": {}, "insert": {}, "into": {}, "update": {},
		"delete": {}, "join": {}, "left": {}, "right": {}, "inner": {}, "outer": {},
		"on": {}, "group": {}, "by": {}, "order": {}, "limit": {}, "having": {},
		"create": {}, "table": {}, "drop": {}, "alter": {}, "and": {}, "or": {},
		"not": {}, "null": {}, "as": {}, "primary": {}, "key": {}, "distinct": {},
	},
}

// TokenizeCode mem-parsing kode sumber menjadi potongan token tersorot (syntax highlighting)
// sesuai protokol Meta AI Rich Response (GenAICodeUXPrimitive dan AIRichResponseCodeMetadata).
func TokenizeCode(lang, code string) ([]*waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeBlock, []map[string]any) {
	langLower := strings.ToLower(strings.TrimSpace(lang))
	if langLower == "" || langLower == "txt" || langLower == "text" || langLower == "plaintext" {
		protoBlocks := []*waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeBlock{
			{
				HighlightType: waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeHighlightType(0).Enum(),
				CodeContent:   proto.String(code),
			},
		}
		unified := []map[string]any{
			{
				"content": code,
				"type":    "DEFAULT",
			},
		}
		return protoBlocks, unified
	}

	keywords, hasKeywords := langKeywords[langLower]
	isSQL := langLower == "sql"

	type rawToken struct {
		content string
		typeID  int32
	}

	rawTokens := make([]rawToken, 0)
	push := func(content string, typeID int32) {
		if content == "" {
			return
		}
		if len(rawTokens) > 0 && rawTokens[len(rawTokens)-1].typeID == typeID {
			rawTokens[len(rawTokens)-1].content += content
		} else {
			rawTokens = append(rawTokens, rawToken{content: content, typeID: typeID})
		}
	}

	runes := []rune(code)
	n := len(runes)
	i := 0

	for i < n {
		c := runes[i]

		// 1. Whitespace
		if unicode.IsSpace(c) {
			start := i
			for i < n && unicode.IsSpace(runes[i]) {
				i++
			}
			push(string(runes[start:i]), 0)
			continue
		}

		// 2. Line comment // or /* */
		if c == '/' && i+1 < n && runes[i+1] == '/' {
			start := i
			for i < n && runes[i] != '\n' {
				i++
			}
			push(string(runes[start:i]), 5) // COMMENT
			continue
		}
		if c == '/' && i+1 < n && runes[i+1] == '*' {
			start := i
			i += 2
			for i+1 < n && !(runes[i] == '*' && runes[i+1] == '/') {
				i++
			}
			if i+1 < n {
				i += 2
			} else {
				i = n
			}
			push(string(runes[start:i]), 5) // COMMENT
			continue
		}

		// 3. Hash comments (#) for python, bash
		if c == '#' && (langLower == "python" || langLower == "py" || langLower == "bash" || langLower == "sh") {
			start := i
			for i < n && runes[i] != '\n' {
				i++
			}
			push(string(runes[start:i]), 5) // COMMENT
			continue
		}

		// 4. SQL comment (--)
		if isSQL && c == '-' && i+1 < n && runes[i+1] == '-' {
			start := i
			for i < n && runes[i] != '\n' {
				i++
			}
			push(string(runes[start:i]), 5) // COMMENT
			continue
		}

		// 5. Strings: ", ', `
		if c == '"' || c == '\'' || c == '`' {
			quote := c
			start := i
			i++
			for i < n {
				if runes[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if runes[i] == quote {
					i++
					break
				}
				i++
			}
			push(string(runes[start:i]), 3) // STR
			continue
		}

		// 6. Numbers
		if unicode.IsDigit(c) {
			start := i
			for i < n && (unicode.IsDigit(runes[i]) || runes[i] == '.' || runes[i] == '_' || runes[i] == 'x' || runes[i] == 'X') {
				i++
			}
			push(string(runes[start:i]), 4) // NUMBER
			continue
		}

		// 7. Identifiers / Words
		if unicode.IsLetter(c) || c == '_' || c == '$' {
			start := i
			for i < n && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) || runes[i] == '_' || runes[i] == '$') {
				i++
			}
			word := string(runes[start:i])
			typeID := int32(0) // DEFAULT

			lookupWord := word
			if isSQL {
				lookupWord = strings.ToLower(word)
			}

			if hasKeywords {
				if _, ok := keywords[lookupWord]; ok {
					typeID = 1 // KEYWORD
				}
			}

			if typeID == 0 {
				// Cek apakah pemanggilan fungsi: nama(...)
				j := i
				for j < n && unicode.IsSpace(runes[j]) {
					j++
				}
				if j < n && runes[j] == '(' {
					typeID = 2 // METHOD
				}
			}

			push(word, typeID)
			continue
		}

		// 8. Simbol / Operator lainnya
		push(string(c), 0)
		i++
	}

	protoBlocks := make([]*waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeBlock, 0, len(rawTokens))
	unified := make([]map[string]any, 0, len(rawTokens))

	for _, t := range rawTokens {
		protoBlocks = append(protoBlocks, &waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeBlock{
			HighlightType: waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeHighlightType(t.typeID).Enum(),
			CodeContent:   proto.String(t.content),
		})
		typeName := codeTypeNames[t.typeID]
		if typeName == "" {
			typeName = "DEFAULT"
		}
		unified = append(unified, map[string]any{
			"content": t.content,
			"type":    typeName,
		})
	}

	return protoBlocks, unified
}
