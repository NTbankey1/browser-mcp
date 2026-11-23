package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// Token is one argument. Quoted tokens are never flags.
type Token struct {
	Text   string
	Quoted bool
}

// CommandLine is one trimmed, non-comment line.
type CommandLine struct {
	// Step is 1-based within the call.
	Step   int
	Source string
}

// ParseError is a line that cannot be parsed; its message names the column,
// never the text, which may be something the agent types into a page.
type ParseError struct{ Message string }

func (e *ParseError) Error() string { return e.Message }

func parseErrorf(format string, args ...any) error {
	return &ParseError{Message: fmt.Sprintf(format, args...)}
}

// ParseCommands splits the tool's input into command lines, dropping blank
// lines and comments.
func ParseCommands(input string) []CommandLine {
	var commands []CommandLine
	for _, raw := range strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n") {
		source := strings.TrimSpace(raw)
		if source == "" || strings.HasPrefix(source, "#") {
			continue
		}
		commands = append(commands, CommandLine{Step: len(commands) + 1, Source: source})
	}
	return commands
}

// CommandName is the line's first word.
func CommandName(source string) string {
	if index := strings.IndexFunc(source, unicode.IsSpace); index >= 0 {
		return source[:index]
	}
	return source
}

var closers = map[byte]byte{'(': ')', '[': ']', '{': '}'}

// Tokenize splits a line into arguments. Brackets, quotes and regexes nest, so
// a locator, a CSS attribute selector or JSON is one token even with spaces.
func Tokenize(line string) ([]Token, error) {
	var tokens []Token
	verbatim := CommandName(line) == "eval"
	i := 0
	for i < len(line) {
		if isSpace(line[i]) {
			i++
			continue
		}
		if line[i] == '"' || line[i] == '\'' {
			end, err := scanQuoted(line, i, line[i])
			if err != nil {
				return nil, err
			}
			text, err := unquote(line, i, end, verbatim)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, Token{Text: text, Quoted: true})
			i = end
			continue
		}
		start := i
		var stack []byte
		for i < len(line) {
			ch := line[i]
			if len(stack) == 0 && isSpace(ch) {
				break
			}
			if closer, ok := closers[ch]; ok {
				stack = append(stack, closer)
				i++
			} else if len(stack) > 0 && ch == stack[len(stack)-1] {
				stack = stack[:len(stack)-1]
				i++
			} else if len(stack) > 0 && (ch == '\'' || ch == '"' || ch == '`') {
				end, err := scanQuoted(line, i, ch)
				if err != nil {
					return nil, err
				}
				i = end
			} else if len(stack) > 0 && ch == '/' && isRegexStart(line, i) {
				end, err := scanQuoted(line, i, '/')
				if err != nil {
					return nil, err
				}
				i = end
				for i < len(line) && line[i] >= 'a' && line[i] <= 'z' {
					i++
				}
			} else {
				i++
			}
		}
		if len(stack) > 0 {
			missing := ""
			for j := len(stack) - 1; j >= 0; j-- {
				missing += string(stack[j])
			}
			return nil, parseErrorf("Unbalanced brackets in the argument at column %d: missing %s", start+1, missing)
		}
		tokens = append(tokens, Token{Text: line[start:i]})
	}
	return tokens, nil
}

func isSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\v' || ch == '\f'
}

// scanQuoted returns the index after the closing quote.
func scanQuoted(line string, start int, quote byte) (int, error) {
	i := start + 1
	for i < len(line) {
		if line[i] == '\\' {
			i += 2
		} else if line[i] == quote {
			return i + 1, nil
		} else {
			i++
		}
	}
	return 0, parseErrorf("Missing closing %c for the quote at column %d", quote, start+1)
}

// unquote decodes a quoted argument. Playwright locators use JSON escaping;
// eval's code keeps JavaScript's own backslashes.
func unquote(line string, start, end int, verbatim bool) (string, error) {
	quote := line[start]
	body := line[start+1 : end-1]
	if verbatim {
		var out strings.Builder
		for i := 0; i < len(body); i++ {
			if body[i] == '\\' && i+1 < len(body) {
				if body[i+1] == quote {
					out.WriteByte(quote)
					i++
					continue
				}
				out.WriteByte('\\')
				continue
			}
			out.WriteByte(body[i])
		}
		return out.String(), nil
	}
	literal := body
	if quote == '\'' {
		// Inside single quotes, \" needs no escape and \' is a quote
		var out strings.Builder
		for i := 0; i < len(literal); i++ {
			if literal[i] == '\\' && i+1 < len(literal) {
				switch literal[i+1] {
				case '\'':
					out.WriteByte('\'')
					i++
					continue
				case '\\':
					out.WriteString(`\\`)
					i++
					continue
				default:
					out.WriteByte('\\')
					continue
				}
			}
			if literal[i] == '"' {
				out.WriteString(`\"`)
				continue
			}
			out.WriteByte(literal[i])
		}
		literal = out.String()
	}
	var decoded string
	if err := json.Unmarshal([]byte(`"`+literal+`"`), &decoded); err != nil {
		return "", parseErrorf("Bad escaping in the quoted argument at column %d. Escape like JSON (\\\" and \\\\)", start+1)
	}
	return decoded, nil
}

func isRegexStart(line string, i int) bool {
	before := strings.TrimRightFunc(line[:i], unicode.IsSpace)
	if before == "" {
		return false
	}
	last := before[len(before)-1]
	return last == '(' || last == ',' || last == ':'
}

// ParsedArgs are a line's positional arguments and flags.
type ParsedArgs struct {
	Positional []Token
	// Values are flags that take one (`--max 500`).
	Values map[string]string
	// Switches are flags without one (`--submit`).
	Switches map[string]bool
}

// Value returns a valued flag.
func (a ParsedArgs) Value(name string) (string, bool) {
	value, ok := a.Values[name]
	return value, ok
}

// On reports a switch, or a valued flag that was given without a value.
func (a ParsedArgs) On(name string) bool {
	if a.Switches[name] {
		return true
	}
	_, ok := a.Values[name]
	return ok
}

// Arg returns positional argument index, or ok=false.
func (a ParsedArgs) Arg(index int) (string, bool) {
	if index < 0 || index >= len(a.Positional) {
		return "", false
	}
	return a.Positional[index].Text, true
}

// FlagSpec is a command's flags: true for one that takes a value.
type FlagSpec map[string]bool

// ParseArgs splits tokens into positionals and flags.
func ParseArgs(command string, tokens []Token, spec FlagSpec) (ParsedArgs, error) {
	args := ParsedArgs{Values: map[string]string{}, Switches: map[string]bool{}}
	flagPattern := func(text string) bool {
		if strings.HasPrefix(text, "--") && len(text) > 2 {
			for _, ch := range text[2:] {
				if !(ch >= 'a' && ch <= 'z') && ch != '-' {
					return false
				}
			}
			return true
		}
		if strings.HasPrefix(text, "-") && len(text) == 2 {
			ch := text[1]
			return ch >= 'a' && ch <= 'z'
		}
		return false
	}
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if token.Quoted || !flagPattern(token.Text) {
			args.Positional = append(args.Positional, token)
			continue
		}
		name := strings.TrimLeft(token.Text, "-")
		takesValue, ok := spec[name]
		if !ok {
			return args, parseErrorf("%s has no %s flag. Run `help %s`", command, token.Text, command)
		}
		if takesValue {
			if i+1 >= len(tokens) {
				return args, parseErrorf("%s needs a value", token.Text)
			}
			args.Values[name] = tokens[i+1].Text
			i++
		} else {
			args.Switches[name] = true
		}
	}
	return args, nil
}
