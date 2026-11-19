// hcltext.go is how a value reaches a rendered OpenTofu file (.tf, .tfvars). A template
// is text with actions in it, and an action's value is whatever the model holds: a name
// bedrock derived, or text from the application's code or its placement (a field's doc
// comment, a label, a reason). Written as it comes, a double quote in such text ends the
// string it stands in, and the file no longer parses, or parses as something else.
//
// So no template writes a value itself. Execute marks every action's output, puts the
// rendered text through HCL's own scanner to see where each value landed, and writes it
// as that place takes it: inside a quoted string as the string's text (a double quote, a
// backslash, a line break, ${ and %{ each written so that HCL reads them as text); inside
// a heredoc with ${ and %{ written as text, and never with a line that ends the heredoc;
// inside a comment so that it stays in the comment (a line comment's value on its line);
// and anywhere else as it is, since there the value is HCL the render wrote (a number, a
// reference, a list). A string the render builds in Go is written with Quote. The file is
// then formatted as tofu fmt writes it and parsed, and a file that does not parse is an
// error of the render, never a file on disk.

package render

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"

	"github.com/go-playground/errors/v5"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// The word a marked value is replaced by while the text is scanned, with the value's
// number after it: a word HCL's scanner keeps whole wherever it stands, and which no
// template and no value may hold.
const (
	valueWord  = "x7Qbedrockvalue"
	valueDigit = 6
	// markBytes is how many random bytes a render's marks carry.
	markBytes = 12
	// lineShown bounds the line a parse refusal quotes.
	lineShown = 200
)

// marks are what one render puts around every action's output: a NUL and a word drawn
// for this render alone, so that no value can hold a mark, whatever its text.
type marks struct {
	open, shut string
}

func newMarks() (marks, error) {
	word := make([]byte, markBytes)
	if _, err := rand.Read(word); err != nil {
		return marks{}, errors.Wrap(err, "rand.Read()")
	}
	mark := "\x00" + hex.EncodeToString(word)

	return marks{open: mark + "(", shut: mark + ")"}, nil
}

// IsHCL reports whether the file is one OpenTofu parses as HCL: a .tf or a .tfvars.
func IsHCL(name string) bool {
	ext := path.Ext(name)

	return ext == ".tf" || ext == ".tfvars"
}

// Execute renders the template src as the file name (the template's name without its
// extension) from data. An HCL file's values are written as the place each lands in
// takes them, and the file is formatted and must parse; any other file is the template's
// output as it is.
func Execute(name string, src []byte, data any) ([]byte, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(string(src))
	if err != nil {
		return nil, errors.Wrapf(err, "template.Parse(): %s", name)
	}
	if !IsHCL(name) {
		var buf bytes.Buffer
		if err := t.Execute(&buf, data); err != nil {
			return nil, errors.Wrapf(err, "template.Execute(): %s", name)
		}

		return buf.Bytes(), nil
	}
	m, err := newMarks()
	if err != nil {
		return nil, err
	}
	for _, each := range t.Templates() {
		if each.Tree != nil {
			markActions(each.Root, m)
		}
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, errors.Wrapf(err, "template.Execute(): %s", name)
	}
	placed, err := placeValues(name, buf.Bytes(), m)
	if err != nil {
		return nil, err
	}
	content := Formatted(name, placed)
	if err := Parses(name, content); err != nil {
		return nil, err
	}

	return content, nil
}

// markActions puts the marks around every action of the list that prints a value, and of
// the lists inside it.
func markActions(list *parse.ListNode, m marks) {
	if list == nil {
		return
	}
	nodes := make([]parse.Node, 0, len(list.Nodes))
	for _, node := range list.Nodes {
		switch n := node.(type) {
		case *parse.ActionNode:
			if len(n.Pipe.Decl) == 0 {
				nodes = append(nodes, markText(n.Pos, m.open), n, markText(n.Pos, m.shut))

				continue
			}
		case *parse.IfNode:
			markActions(n.List, m)
			markActions(n.ElseList, m)
		case *parse.RangeNode:
			markActions(n.List, m)
			markActions(n.ElseList, m)
		case *parse.WithNode:
			markActions(n.List, m)
			markActions(n.ElseList, m)
		}
		nodes = append(nodes, node)
	}
	list.Nodes = nodes
}

func markText(pos parse.Pos, text string) *parse.TextNode {
	return &parse.TextNode{NodeType: parse.NodeText, Pos: pos, Text: []byte(text)}
}

// place is where a value landed in the rendered text, as HCL's scanner reads it.
type place int

const (
	// placeBare is anywhere a value is HCL itself: the render wrote it as it must stand.
	placeBare place = iota
	placeQuoted
	placeHeredoc
	placeLineComment
	placeBlockComment
)

// landing is one value's place, and for a heredoc the word that ends it.
type landing struct {
	in  place
	end string
}

// placeValues takes the marks out of the rendered text and writes each marked value as
// its place takes it.
func placeValues(name string, marked []byte, m marks) ([]byte, error) {
	if bytes.Contains(marked, []byte(valueWord)) {
		return nil, errors.Newf("%s holds the word %s, which the render keeps for itself", name, valueWord)
	}
	var values []string
	var scanned bytes.Buffer
	for rest := marked; ; {
		open := bytes.Index(rest, []byte(m.open))
		if open < 0 {
			scanned.Write(rest)

			break
		}
		value := rest[open+len(m.open):]
		length := bytes.Index(value, []byte(m.shut))
		if length < 0 {
			return nil, errors.Newf("%s: a value's mark is not closed", name)
		}
		scanned.Write(rest[:open])
		scanned.WriteString(word(len(values)))
		values = append(values, string(value[:length]))
		rest = value[length+len(m.shut):]
	}
	// A NUL is in no OpenTofu file, and it is what a mark starts with: a value that
	// holds one is refused, whatever follows it.
	if bytes.IndexByte(scanned.Bytes(), 0) >= 0 {
		return nil, errors.Newf("%s holds a NUL character, which no OpenTofu file takes", name)
	}
	for _, value := range values {
		if strings.ContainsRune(value, 0) {
			return nil, errors.Newf("a value written into %s holds a NUL character, which no OpenTofu file takes: %.*q", name, lineShown, value)
		}
	}
	src := scanned.Bytes()
	landings := valueLandings(name, src, len(values))
	texts := make([]string, len(values))
	for i, value := range values {
		text, err := placedText(value, landings[i])
		if err != nil {
			return nil, errors.Wrapf(err, "%s", name)
		}
		texts[i] = text
	}
	var out bytes.Buffer
	for rest := src; ; {
		at := bytes.Index(rest, []byte(valueWord))
		if at < 0 {
			out.Write(rest)

			break
		}
		end := at + len(valueWord) + valueDigit
		index, err := strconv.Atoi(string(rest[at+len(valueWord) : end]))
		if err != nil || index >= len(values) {
			return nil, errors.Newf("%s: a value's word is not whole", name)
		}
		out.Write(rest[:at])
		rest = rest[end:]
		text, err := apart(texts[index], landings[index].in, out.Bytes(), following(rest, texts))
		if err != nil {
			return nil, errors.Wrapf(err, "%s", name)
		}
		out.WriteString(text)
	}

	return out.Bytes(), nil
}

// word is the word the value of the number stands as while the text is scanned.
func word(index int) string {
	return fmt.Sprintf("%s%0*d", valueWord, valueDigit, index)
}

// following is the text after a value as it will stand: values that are empty are not
// there, so what follows them is what follows this one.
func following(rest []byte, texts []string) []byte {
	for bytes.HasPrefix(rest, []byte(valueWord)) && len(rest) >= len(valueWord)+valueDigit {
		index, err := strconv.Atoi(string(rest[len(valueWord) : len(valueWord)+valueDigit]))
		if err != nil || index >= len(texts) || texts[index] != "" {
			break
		}
		rest = rest[len(valueWord)+valueDigit:]
	}

	return rest
}

// valueLandings scans the text, each value standing in it as its word, and answers where
// each value landed. A word the scanner did not keep whole in one token is left bare.
func valueLandings(name string, src []byte, count int) []landing {
	landings := make([]landing, count)
	var heredocs []string
	// The scanner's diagnostics are the parse's to report, on the finished file.
	tokens, _ := hclsyntax.LexConfig(src, name, hcl.InitialPos)
	for _, token := range tokens {
		var at landing
		switch token.Type {
		case hclsyntax.TokenOHeredoc:
			heredocs = append(heredocs, strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(string(token.Bytes), "<<"), "-")))

			continue
		case hclsyntax.TokenCHeredoc:
			if len(heredocs) > 0 {
				heredocs = heredocs[:len(heredocs)-1]
			}

			continue
		case hclsyntax.TokenQuotedLit:
			at.in = placeQuoted
		case hclsyntax.TokenStringLit:
			at.in = placeHeredoc
			if len(heredocs) > 0 {
				at.end = heredocs[len(heredocs)-1]
			}
		case hclsyntax.TokenComment:
			at.in = placeLineComment
			if bytes.HasPrefix(token.Bytes, []byte("/*")) {
				at.in = placeBlockComment
			}
		default:
			continue
		}
		for rest := token.Bytes; ; {
			found := bytes.Index(rest, []byte(valueWord))
			if found < 0 || len(rest) < found+len(valueWord)+valueDigit {
				break
			}
			if index, err := strconv.Atoi(string(rest[found+len(valueWord) : found+len(valueWord)+valueDigit])); err == nil && index < count {
				landings[index] = at
			}
			rest = rest[found+len(valueWord)+valueDigit:]
		}
	}

	return landings
}

// placedText writes the value as its place takes it, before it is kept apart from what
// stands against it (apart).
func placedText(value string, at landing) (string, error) {
	switch at.in {
	case placeQuoted:
		return quotedText(value), nil
	case placeHeredoc:
		// A heredoc is text as it comes, line breaks and all, which is how the render's
		// own scripts stand in one; a line that is the heredoc's end would close it.
		for _, line := range strings.FieldsFunc(value, func(r rune) bool { return r == '\n' || r == '\r' }) {
			if at.end != "" && strings.TrimSpace(line) == at.end {
				return "", errors.Newf("a value written into a heredoc holds a line that is the heredoc's own end (%s), which would close it: %.*q", at.end, lineShown, value)
			}
		}

		return strings.NewReplacer("${", "$${", "%{", "%%{").Replace(value), nil
	case placeLineComment:
		return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(value), nil
	case placeBlockComment:
		return strings.ReplaceAll(value, "*/", "* /"), nil
	default:
		return value, nil
	}
}

// apart keeps a value's text from making, with what stands against it, something neither
// holds alone. before is the text already written and after the text that follows.
//
// In a string or a heredoc a $ or % directly before a brace opens a template sequence.
// Where the brace starts the value and the character ends the text before it, or the
// character ends the value and the brace follows, the character is doubled, which HCL
// reads as the character and the brace. Where the value ends in the character and a
// sequence of the template's own follows (${ after $, %{ after %), doubling would turn
// that sequence into text, so the characters the value ends in are written as a string
// of their own (${"$"}), which cannot stand directly after such a character either: a
// value of nothing but those characters, between one and a sequence, is refused.
//
// In a block comment an asterisk and a slash that meet across the value's edge would
// close the comment, and a space keeps them apart.
func apart(text string, in place, before, after []byte) (string, error) {
	if text == "" {
		return text, nil
	}
	if in == placeBlockComment {
		if n := len(before); text[0] == '/' && n > 0 && before[n-1] == '*' {
			text = " " + text
		}
		if text[len(text)-1] == '*' && bytes.HasPrefix(after, []byte("/")) {
			text += " "
		}

		return text, nil
	}
	if in != placeQuoted && in != placeHeredoc {
		return text, nil
	}
	opens := func(b []byte) bool { return len(b) > 0 && (b[len(b)-1] == '$' || b[len(b)-1] == '%') }
	if text[0] == '{' && opens(before) {
		text = string(before[len(before)-1]) + text
	}
	last := text[len(text)-1]
	if last != '$' && last != '%' {
		return text, nil
	}
	switch {
	case bytes.HasPrefix(after, []byte("{")):
		text += string(last)
	case bytes.HasPrefix(after, []byte{last, '{'}):
		kept := strings.TrimRight(text, "$%")
		if kept == "" && opens(before) {
			return "", errors.Newf("the value %q stands between a %c and a template sequence, and cannot be written as text there", text, before[len(before)-1])
		}
		text = kept + `${"` + text[len(kept):] + `"}`
	}

	return text, nil
}

// Quote writes s as an HCL string literal, quotes and all, as HCL's own writer writes a
// string: a double quote, a backslash, a line break and a tab as their escapes, ${ and
// %{ as $${ and %%{, and a character that does not print by its code point. It is how
// the render's Go code writes a string where a template takes HCL as it is.
func Quote(s string) string {
	return string(hclwrite.TokensForValue(cty.StringVal(s)).Bytes())
}

// QuoteList writes the strings as an HCL list of string literals on one line.
func QuoteList(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, item := range items {
		quoted = append(quoted, Quote(item))
	}

	return "[" + strings.Join(quoted, ", ") + "]"
}

// Key writes a map's key: the name as it is where HCL takes it as an identifier, and a
// string literal otherwise, so a key from a placement's own words never ends the map.
func Key(name string) string {
	if hclsyntax.ValidIdentifier(name) {
		return name
	}

	return Quote(name)
}

// OneLine is the text on one line, each line break a space: how the render's Go code
// writes a value into a line comment it builds itself.
func OneLine(s string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
}

// quotedText is the text of s between a string literal's quotes.
func quotedText(s string) string {
	quoted := Quote(s)

	return quoted[1 : len(quoted)-1]
}

// Parses refuses a rendered OpenTofu file that does not parse, with the file, the line
// and the parser's own sentence: tofu would refuse the whole stack at its first step,
// after the check that compares the files with the render had passed them. The file is
// bedrock's to write so that it parses, whatever the code and the placement hold, so the
// refusal says whose defect it is and quotes the line, which is in no file to look at.
func Parses(name string, content []byte) error {
	if !IsHCL(name) {
		return nil
	}
	_, diags := hclsyntax.ParseConfig(content, name, hcl.InitialPos)
	for _, diag := range diags {
		if diag.Severity != hcl.DiagError {
			continue
		}
		where, quoted := "", ""
		if diag.Subject != nil {
			where = fmt.Sprintf(", line %d", diag.Subject.Start.Line)
			if lines := bytes.Split(content, []byte("\n")); diag.Subject.Start.Line >= 1 && diag.Subject.Start.Line <= len(lines) {
				quoted = fmt.Sprintf(" The line: %.*s", lineShown, bytes.TrimSpace(lines[diag.Subject.Start.Line-1]))
			}
		}

		return errors.Newf("%s as bedrock renders it does not parse%s: %s: %s OpenTofu would refuse the stack, so the file is not written; this is a defect of bedrock's render to report with the line.%s", name, where, diag.Summary, diag.Detail, quoted)
	}

	return nil
}
