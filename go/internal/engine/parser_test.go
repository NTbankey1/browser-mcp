package engine

import "testing"

func TestParseCommands(t *testing.T) {
	lines := ParseCommands("\n  # a comment\ngoto https://example.com\r\n\nsnapshot -i\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %+v", len(lines), lines)
	}
	if lines[0].Source != "goto https://example.com" || lines[0].Step != 1 {
		t.Fatalf("first line: %+v", lines[0])
	}
	if lines[1].Source != "snapshot -i" || lines[1].Step != 2 {
		t.Fatalf("second line: %+v", lines[1])
	}
}

func TestTokenizeKeepsLocatorsAndQuotesWhole(t *testing.T) {
	tokens, err := Tokenize(`click getByRole('button', { name: /Sub mit/i })`)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 {
		t.Fatalf("got %d tokens, want 2: %+v", len(tokens), tokens)
	}
	if got := tokens[1].Text; got != `getByRole('button', { name: /Sub mit/i })` {
		t.Fatalf("locator token: %q", got)
	}

	tokens, err = Tokenize(`fill getByLabel("Email") "a b"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 3 || tokens[2].Text != "a b" || !tokens[2].Quoted {
		t.Fatalf("quoted argument: %+v", tokens)
	}

	tokens, err = Tokenize(`click a[title="x y"]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[1].Text != `a[title="x y"]` {
		t.Fatalf("attribute selector: %+v", tokens)
	}
}

func TestTokenizeEvalKeepsBackslashes(t *testing.T) {
	tokens, err := Tokenize(`eval "() => /\d+/.test('12')"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[1].Text != `() => /\d+/.test('12')` {
		t.Fatalf("eval code: %+v", tokens)
	}
}

func TestTokenizeRejectsUnbalancedBrackets(t *testing.T) {
	if _, err := Tokenize(`click a[title`); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := Tokenize(`fill "unterminated`); err == nil {
		t.Fatal("expected an error")
	}
}

func TestParseArgs(t *testing.T) {
	tokens, err := Tokenize(`fill getByLabel("Email") "hi" --submit`)
	if err != nil {
		t.Fatal(err)
	}
	args, err := ParseArgs("fill", tokens[1:], FlagSpec{"submit": false})
	if err != nil {
		t.Fatal(err)
	}
	if len(args.Positional) != 2 || !args.Switches["submit"] {
		t.Fatalf("args: %+v", args)
	}

	tokens, _ = Tokenize(`snapshot -i --depth 2 --max all`)
	args, err = ParseArgs("snapshot", tokens[1:], FlagSpec{"i": false, "depth": true, "max": true})
	if err != nil {
		t.Fatal(err)
	}
	if !args.Switches["i"] || args.Values["depth"] != "2" || args.Values["max"] != "all" {
		t.Fatalf("args: %+v", args)
	}

	if _, err := ParseArgs("snapshot", []Token{{Text: "--nope"}}, FlagSpec{}); err == nil {
		t.Fatal("an unknown flag was accepted")
	}
	if _, err := ParseArgs("snapshot", []Token{{Text: "--depth"}}, FlagSpec{"depth": true}); err == nil {
		t.Fatal("a flag without its value was accepted")
	}
}

func TestQuotedTokensAreNeverFlags(t *testing.T) {
	args, err := ParseArgs("eval", []Token{{Text: "--max", Quoted: true}}, FlagSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if len(args.Positional) != 1 {
		t.Fatalf("a quoted --max became a flag: %+v", args)
	}
}

func TestLocatorParsing(t *testing.T) {
	target, err := ParseTarget(`getByRole('button', { name: 'Go', exact: true }).first()`)
	if err != nil {
		t.Fatal(err)
	}
	if target.Kind != "locator" || len(target.Calls) != 2 {
		t.Fatalf("target: %+v", target)
	}
	if _, err := ParseTarget(`getByRole('button', { nope: 1 })`); err == nil {
		t.Fatal("an unknown option was accepted")
	}
}

func TestReadCap(t *testing.T) {
	if value, _ := ReadCapFor("", false); value != ReadCap {
		t.Fatalf("default cap %d", value)
	}
	if value, _ := ReadCapFor("all", true); value != ReadCapMax {
		t.Fatalf("all cap %d", value)
	}
	if value, _ := ReadCapFor("500", true); value != 500 {
		t.Fatalf("500 cap %d", value)
	}
	if _, err := ReadCapFor("nope", true); err == nil {
		t.Fatal("a bad --max was accepted")
	}
}
