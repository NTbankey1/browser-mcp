package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Output limits, matching the TypeScript engine so a native run costs the
// same tokens.
const (
	ReadCap     = 10_000
	ReadCapMax  = 75_000
	ResponseCap = 80_000
	MaxImages   = 3
	// EchoMax cuts echoed command lines.
	EchoMax = 300
	// StatusReserve is room kept for status lines, the trailer and notices.
	StatusReserve = 2_000
)

// StepError's message is meant for the model as is.
type StepError struct{ Message string }

func (e *StepError) Error() string { return e.Message }

func stepErrorf(format string, args ...any) error {
	return &StepError{Message: fmt.Sprintf(format, args...)}
}

// Error codes: a stable word for the class of failure, so an agent can branch
// on it instead of matching prose.
const (
	CodeNoElement    = "no-element"
	CodeTimeout      = "timeout"
	CodeHTTP         = "http"
	CodeDialog       = "dialog-open"
	CodeDisconnected = "disconnected"
	CodeRefused      = "refused"
	CodeVisualDiff   = "visual-diff"
	// CodeImageNotFound: `find --image` and `click --image` found no match.
	CodeImageNotFound = "image-not-found"
	// CodeImageAmbiguous: several places match the image about equally.
	CodeImageAmbiguous = "image-ambiguous"
)

// classify picks the code for a message, or "" when it fits none.
func classify(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "wasn't found on screen"):
		return CodeImageNotFound
	case strings.Contains(lower, "the image matches"):
		return CodeImageAmbiguous
	case strings.Contains(lower, "no element"), strings.Contains(lower, "waiting for locator"), strings.Contains(lower, "there's no "), strings.Contains(lower, "nothing matches"):
		return CodeNoElement
	case strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "timed out"), strings.Contains(lower, "timeout"), strings.Contains(lower, "didn't answer"):
		return CodeTimeout
	case strings.Contains(lower, "dialog") && strings.Contains(lower, "opened"):
		return CodeDialog
	case strings.Contains(lower, "disconnected"):
		return CodeDisconnected
	case strings.Contains(lower, "doesn't allow"), strings.Contains(lower, "isn't allowed"), strings.Contains(lower, "not allowed"):
		return CodeRefused
	case strings.Contains(lower, "differs from"):
		return CodeVisualDiff
	case strings.HasPrefix(message, "HTTP "):
		return CodeHTTP
	}
	return ""
}

// ErrorCode is the class of a failure, "" when it fits none.
func ErrorCode(err error) string { return classify(ErrorMessage(err)) }

// ErrorMessageWithCode prefixes the message with its code, for a step line.
func ErrorMessageWithCode(err error) string {
	message := ErrorMessage(err)
	if code := classify(message); code != "" {
		return "[" + code + "] " + message
	}
	return message
}

// redactHeader hides the value of a header that carries a credential.
func redactHeader(line string) string {
	colon := strings.Index(line, ":")
	if colon < 0 {
		return line
	}
	name := strings.ToLower(strings.TrimSpace(line[:colon]))
	switch name {
	case "authorization", "cookie", "set-cookie", "proxy-authorization", "x-api-key":
		return line[:colon+1] + " [redacted]"
	}
	return line
}

// ErrorMessage turns an error into one line for the model, without call logs.
func ErrorMessage(err error) string {
	var step *StepError
	if errors.As(err, &step) {
		return step.Message
	}
	message := strings.SplitN(err.Error(), "\n", 2)[0]
	for _, prefix := range []string{"page.", "locator.", "frame.", "elementHandle.", "keyboard.", "mouse.", "browserContext.", "cdpSession."} {
		if strings.HasPrefix(message, prefix) {
			if index := strings.Index(message, ": "); index > 0 {
				message = message[index+2:]
			}
			break
		}
	}
	return strings.TrimPrefix(message, "Error: ")
}

// ReadCapFor parses --max: a number of characters, or all.
func ReadCapFor(flag string, set bool) (int, error) {
	if !set || flag == "" {
		return ReadCap, nil
	}
	if flag == "all" {
		return ReadCapMax, nil
	}
	var value int
	if _, err := fmt.Sscanf(flag, "%d", &value); err != nil || value <= 0 {
		return 0, stepErrorf("--max takes a number of characters or all, e.g. --max 2000")
	}
	if value > ReadCapMax {
		return ReadCapMax, nil
	}
	return value, nil
}

// Cap cuts text to max at a line break when one is nearby, and says how to
// read all of it.
func Cap(text string, max int, hint string) string {
	if len(text) <= max {
		return text
	}
	if hint == "" {
		hint = "narrow it with `find \"<text>\"`, a target (`snapshot <target>`), `--depth N` or `-i`"
	}
	cut := strings.LastIndex(text[:max], "\n")
	if cut < max*8/10 {
		cut = max
	}
	all := ""
	if max < ReadCapMax {
		if len(text) <= ReadCapMax {
			all = "--max all reads all of it, or "
		} else {
			all = fmt.Sprintf("--max all reads the first %d, or ", ReadCapMax)
		}
	}
	return fmt.Sprintf("%s\n… cut at %d of %d characters: %s%s", text[:cut], cut, len(text), all, hint)
}

// CapTail keeps the end of a log (the newest entries).
func CapTail(text string, max int) string {
	if len(text) <= max {
		return text
	}
	start := strings.Index(text[len(text)-max:], "\n")
	if start < 0 {
		start = len(text) - max
	} else {
		start += len(text) - max
	}
	return fmt.Sprintf("… %d earlier characters cut; add --filter or raise --max\n%s", start, text[start+1:])
}

// CompactJSON formats a value for eval and cdp.
func CompactJSON(value any) string {
	if value == nil {
		return "null"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

// Truncate shortens text to max characters.
func Truncate(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + "…"
}
