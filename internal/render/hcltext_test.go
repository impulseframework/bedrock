package render

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"

	"github.com/impulseframework/bedrock/internal/derive"
)

// hardTexts are texts that are not their own HCL string literal: each holds a character
// a string written as it comes would end at, or read as something else.
var hardTexts = []struct {
	name string
	text string
}{
	{name: "a double quote", text: `every stored secret field (conditions:"secret") is encrypted with it`},
	{name: "a backslash", text: `the path C:\keys\app and a pattern \d+`},
	{name: "a backslash before a quote", text: `ends in a backslash\"`},
	{name: "a line break", text: "the first line\nand the second"},
	{name: "a carriage return and a tab", text: "one\r\ntwo\tthree"},
	{name: "an interpolation", text: "costs ${var.amount} each, as in ${path.module}"},
	{name: "a directive", text: "a %{ if true }branch%{ endif } of its own"},
	{name: "a dollar and a percent that open nothing", text: "100% of $5, $ { and % {"},
	{name: "what a comment or a heredoc starts with", text: "# not a comment, // nor this, /* nor this */ <<EOT"},
	{name: "an end in a dollar", text: "the price in $"},
	{name: "a brace first", text: "{brace} first"},
	{name: "a character that does not print", text: "a bell \a and a zero-width space \u200b"},
}

// TestExecuteWritesValuesAsTheirPlaceTakesThem: a value written inside a quoted string of
// an HCL template is the string's text whatever it holds, beside the template's own
// interpolations; inside a heredoc its ${ and %{ are text; inside a comment it stays on
// the comment's line; and the file parses. A value written bare is HCL as the render
// wrote it.
func TestExecuteWritesValuesAsTheirPlaceTakesThem(t *testing.T) {
	t.Parallel()

	const src = `# {{.Text}}
locals {
  // {{.Text}}
  whole  = "{{.Text}}"
  inside = "before {{.Text}} after"
  beside = "${local.prefix}-{{.Text}}${local.suffix}"
  nested = "${lookup(local.table, "{{.Text}}", "{{.Text}}")}"
  edged  = "${{.Text}}{{.Text}}{literal}"
  twice  = "{{.Text}}{{.Text}}"
  bare   = {{.Bare}}
  /* {{.Text}} */
  heredoc = <<EOT
a line with {{.Text}} in it
EOT
}
`
	for _, tt := range hardTexts {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := map[string]string{"Text": tt.text, "Bare": `[1, "two", local.three]`}
			content, err := Execute("locals.tf", []byte(src), data)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			body := parsedBody(t, "locals.tf", content)
			locals := body.Blocks[0].Body
			ctx := &hcl.EvalContext{Variables: map[string]cty.Value{"local": cty.ObjectVal(map[string]cty.Value{
				"prefix": cty.StringVal("<"), "suffix": cty.StringVal(">"), "three": cty.NumberIntVal(3),
				"table": cty.MapVal(map[string]cty.Value{"key": cty.StringVal("found")}),
			})}, Functions: map[string]function.Function{"lookup": stdlib.LookupFunc}}
			wants := map[string]string{
				"whole":   tt.text,
				"inside":  "before " + tt.text + " after",
				"beside":  "<-" + tt.text + ">",
				"nested":  tt.text,
				"edged":   "$" + tt.text + tt.text + "{literal}",
				"twice":   tt.text + tt.text,
				"heredoc": "a line with " + tt.text + " in it\n",
			}
			for name, want := range wants {
				value, diags := locals.Attributes[name].Expr.Value(ctx)
				if diags.HasErrors() {
					t.Errorf("%s does not evaluate: %s\n%s", name, diags.Error(), content)

					continue
				}
				if got := value.AsString(); got != want {
					t.Errorf("%s = %q, want %q\n%s", name, got, want, content)
				}
			}
			if _, diags := locals.Attributes["bare"].Expr.Value(ctx); diags.HasErrors() {
				t.Errorf("bare does not evaluate: %s\n%s", diags.Error(), content)
			}
			if len(body.Blocks) != 1 || len(body.Attributes) != 0 || len(locals.Attributes) != len(wants)+1 {
				t.Errorf("the file holds more than the template's one block and its attributes:\n%s", content)
			}
		})
	}
}

// TestExecuteLeavesOtherFilesAlone: a file that is not HCL is the template's output as it
// is, with no value rewritten.
func TestExecuteLeavesOtherFilesAlone(t *testing.T) {
	t.Parallel()

	content, err := Execute("README.md", []byte("a \"{{.Text}}\" in text\n"), map[string]string{"Text": `quoted "word" and ${this}`})
	if want := "a \"quoted \"word\" and ${this}\" in text\n"; err != nil || string(content) != want {
		t.Errorf("Execute() = %q, %v, want %q", content, err, want)
	}
}

// TestQuote: a string the render's Go code writes is an HCL string literal that reads
// back as the string.
func TestQuote(t *testing.T) {
	t.Parallel()

	for _, tt := range hardTexts {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			expr, diags := hclsyntax.ParseExpression([]byte(Quote(tt.text)), "quote.tf", hcl.InitialPos)
			if diags.HasErrors() {
				t.Fatalf("Quote(%q) = %s does not parse: %s", tt.text, Quote(tt.text), diags.Error())
			}
			value, diags := expr.Value(nil)
			if diags.HasErrors() || value.AsString() != tt.text {
				t.Errorf("Quote(%q) = %s reads back as %q (%s)", tt.text, Quote(tt.text), value.GoString(), diags.Error())
			}
		})
	}
}

// TestParses: a rendered file that does not parse is refused with the file, the line and
// the parser's sentence, as tofu init would refuse the stack; one that parses, and a file
// that is not OpenTofu's, pass.
func TestParses(t *testing.T) {
	t.Parallel()

	broken := `locals {
  secrets = {
    APP_SECRET_KEY = {
      name    = "secret-key"
      purpose = "every stored secret field (conditions:" secret ") is encrypted with."
    }
  }
}
`
	err := Parses("locals.tf", []byte(broken))
	for _, want := range []string{
		"locals.tf as bedrock renders it does not parse, line 5: Missing attribute separator: Expected a newline or comma to mark the beginning of the next attribute.",
		`The line: purpose = "every stored secret field (conditions:" secret ") is encrypted with."`,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parses() of a file that does not parse = %v, want %q", err, want)
		}
	}
	if err := Parses("locals.tf", []byte(strings.Replace(broken, `" secret "`, `\"secret\"`, 1))); err != nil {
		t.Errorf("Parses() of a file that parses = %v", err)
	}
	if err := Parses("README.md", []byte(broken)); err != nil {
		t.Errorf("Parses() of a file that is not OpenTofu's = %v", err)
	}
	// A value written bare is the render's own HCL, and one that is not HCL stops the
	// render with the same refusal: no file is written.
	_, err = Execute("outputs.tf", []byte("locals {\n  port = {{.Port}}\n}\n"), map[string]string{"Port": `80" oops`})
	if want := "outputs.tf as bedrock renders it does not parse, line 2: "; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Execute() with a bare value that is not HCL = %v, want %q", err, want)
	}
}

// skeletonDoc is the doc comment of the config's SecretKey field as the framework's
// skeleton writes it into every new application (impulse v0.5.0), line breaks and all.
const skeletonDoc = `SecretKey declares the application secret: 32 random bytes kept in versions, which
every stored secret field (conditions:"secret") is encrypted with. secret:"versioned"
has the deployment generate it and hand the service every enabled version, each in
a variable of its own named by this one and the version's number (APP_SECRET_KEY_1,
APP_SECRET_KEY_2). No variable has this name itself, so the field stays empty: the
versions are read into the key holder when the level opens
(resource.SecretKeysFromEnv, under this name).
`

// TestSecretPurposeFromADocComment: a secret's purpose is the first sentence of its
// field's doc comment, the application's own text, and the stack's locals.tf holds it as
// that text whatever it holds: the framework skeleton's own comment on SecretKey, which
// names a struct tag with its quotes, and a comment with each character a string literal
// escapes.
func TestSecretPurposeFromADocComment(t *testing.T) {
	t.Parallel()

	docs := []struct {
		name string
		doc  string
	}{
		{name: "the skeleton's comment on SecretKey", doc: skeletonDoc},
	}
	for _, hard := range hardTexts {
		docs = append(docs, struct {
			name string
			doc  string
		}{name: hard.name, doc: "SecretKey holds " + hard.text})
	}
	for _, tt := range docs {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := deriveFixture(t, "beacon", "placement-beacon.json")
			found := false
			for i := range m.Secrets {
				if m.Secrets[i].Variable.Name == "APP_SECRET_KEY" {
					m.Secrets[i].Variable.Doc, found = tt.doc, true
				}
			}
			if !found {
				t.Fatal("the fixture declares no APP_SECRET_KEY")
			}
			files, err := Render(m)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			var content []byte
			for _, f := range files {
				if f.Path == "locals.tf" && !f.Root {
					content = f.Content
				}
			}
			body := parsedBody(t, "locals.tf", content)
			var purpose string
			for _, block := range body.Blocks {
				attr, ok := block.Body.Attributes["secrets"]
				if block.Type != "locals" || !ok {
					continue
				}
				value, diags := objectItem(t, objectItem(t, attr.Expr, "APP_SECRET_KEY"), "purpose").Value(nil)
				if diags.HasErrors() {
					t.Fatalf("the purpose is not a string of its own: %s\n%s", diags.Error(), content)
				}
				purpose = value.AsString()
			}
			if want := firstSentence(tt.doc); purpose != want {
				t.Errorf("locals.secrets.APP_SECRET_KEY.purpose = %q, want the comment's first sentence %q", purpose, want)
			}
		})
	}
}

// parsedBody parses the rendered file and answers its body, failing the test when it
// does not parse.
func parsedBody(t *testing.T, name string, content []byte) *hclsyntax.Body {
	t.Helper()

	file, diags := hclsyntax.ParseConfig(content, name, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("%s does not parse: %s\n%s", name, diags.Error(), content)
	}

	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		t.Fatalf("%s is not native HCL syntax", name)
	}

	return body
}

// TestLabelsFromThePlacement: the placement's labels are the organization's own words,
// keys and values, and the stack's locals.tf holds each as it was written: a value with
// any character a string literal escapes, and a key HCL does not take as an identifier.
func TestLabelsFromThePlacement(t *testing.T) {
	t.Parallel()

	for _, tt := range hardTexts {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := deriveFixture(t, "beacon", "placement-beacon.json")
			m.Placement.Labels = map[string]string{"note": tt.text, "team.name": "keepers", "cost-center": "c-42"}
			files, err := Render(m)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			var content []byte
			for _, f := range files {
				if f.Path == "locals.tf" && !f.Root {
					content = f.Content
				}
			}
			var labels map[string]cty.Value
			for _, block := range parsedBody(t, "locals.tf", content).Blocks {
				attr, ok := block.Body.Attributes["base_labels"]
				if block.Type != "locals" || !ok {
					continue
				}
				value, diags := attr.Expr.Value(&hcl.EvalContext{Variables: map[string]cty.Value{
					"var":   cty.ObjectVal(map[string]cty.Value{"environment": cty.StringVal("tst")}),
					"local": cty.ObjectVal(map[string]cty.Value{"app": cty.StringVal("beacon")}),
				}})
				if diags.HasErrors() {
					t.Fatalf("base_labels does not evaluate: %s\n%s", diags.Error(), content)
				}
				labels = value.AsValueMap()
			}
			for key, want := range m.Placement.Labels {
				if got, ok := labels[key]; !ok || got.AsString() != want {
					t.Errorf("base_labels[%q] = %#v, want %q", key, got, want)
				}
			}
		})
	}
}

// TestPortDefaultIsANumber: the port's default is the one value of the code's that the
// stack writes as a number and not as a string, so a default that is not a port number
// is refused by name before any file is rendered.
func TestPortDefaultIsANumber(t *testing.T) {
	t.Parallel()

	m := deriveFixture(t, "beacon", "placement-beacon.json")
	for i := range m.Variables {
		if m.Variables[i].Role == derive.RolePort {
			m.Variables[i].Default = `8080, x = "y"`
		}
	}
	_, err := Render(m)
	if want := `has the default "8080, x = \"y\"", which is not a port number`; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Render() with a port default that is not a number = %v, want %q", err, want)
	}
}

// TestExecuteAtTheEdges: what a value may not do, it does not. A value that is nothing
// keeps the ones beside it apart from the template's braces; a value of dollars alone
// stands before an interpolation; a value that ends in a percent stands before a
// directive; an asterisk and a slash do not close a block comment across a value's edge.
// A value that holds a NUL, the marks of another render, or a line that is its heredoc's
// end is refused, and the render writes nothing.
func TestExecuteAtTheEdges(t *testing.T) {
	t.Parallel()

	ctx := &hcl.EvalContext{Variables: map[string]cty.Value{"local": cty.ObjectVal(map[string]cty.Value{"s": cty.StringVal(">")})}}
	reads := []struct {
		name string
		src  string
		data map[string]string
		want string
	}{
		{name: "an empty value before a brace", src: `a = "{{.A}}{{.B}}{lit}"`, data: map[string]string{"A": "x$", "B": ""}, want: "x${lit}"},
		{name: "an empty value before an interpolation", src: `a = "{{.A}}{{.B}}${local.s}"`, data: map[string]string{"A": "x$", "B": ""}, want: "x$>"},
		{name: "dollars alone before an interpolation", src: `a = "{{.A}}${local.s}"`, data: map[string]string{"A": "$$"}, want: "$$>"},
		{name: "a percent before a directive", src: `a = "{{.A}}%{ if true }yes%{ endif }"`, data: map[string]string{"A": "50%"}, want: "50%yes"},
		{name: "a dollar, then a value that starts with a brace", src: `a = "{{.A}}{{.B}}"`, data: map[string]string{"A": "x$", "B": "{y}"}, want: "x${y}"},
		{name: "a block comment's end across the edge", src: "/* {{.A}}/ and *{{.B}} */\na = \"{{.A}}\"", data: map[string]string{"A": "x*", "B": "/y"}, want: "x*"},
	}
	for _, tt := range reads {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			content, err := Execute("edges.tf", []byte(tt.src+"\n"), tt.data)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			body := parsedBody(t, "edges.tf", content)
			value, diags := body.Attributes["a"].Expr.Value(ctx)
			if diags.HasErrors() || value.AsString() != tt.want || len(body.Attributes) != 1 || len(body.Blocks) != 0 {
				t.Errorf("a = %#v (%s), want %q alone in the file:\n%s", value, diags.Error(), tt.want, content)
			}
		})
	}

	refusals := []struct {
		name    string
		src     string
		value   string
		wantErr string
	}{
		{name: "a NUL", src: `a = "{{.A}}"`, value: "x\x00y", wantErr: "holds a NUL character"},
		{name: "what looks like a mark", src: "# {{.A}}", value: "ok\x00)\nresource \"x\" \"y\" {}\n#\x00(", wantErr: "holds a NUL character"},
		{name: "the render's own word", src: `a = "{{.A}}"`, value: "x7Qbedrockvalue000000", wantErr: "which the render keeps for itself"},
		{name: "the heredoc's end", src: "a = <<EOT\n{{.A}}\nEOT", value: "x\nEOT\nb = <<EOT\ny", wantErr: "holds a line that is the heredoc's own end (EOT)"},
		{name: "a dollar between a dollar and an interpolation", src: `a = "{{.B}}{{.A}}${local.s}"`, value: "$", wantErr: "cannot be written as text there"},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			content, err := Execute("edges.tf", []byte(tt.src+"\n"), map[string]string{"A": tt.value, "B": "$"})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Execute() = %v, want %q; it rendered:\n%s", err, tt.wantErr, content)
			}
		})
	}
}

// TestCommentBlocksStayComments: the comment blocks the view builds itself, the defaults
// terraform.tfvars restates among them, hold the placement's words on their lines: a
// line break in the placeholder image or in the hosted domain starts no argument of its
// own in the seeded file.
func TestCommentBlocksStayComments(t *testing.T) {
	t.Parallel()

	m := deriveFixture(t, "harbor", "placement.json")
	m.Placement.PlaceholderImage = "example.test/image:1\ninjected = 1"
	m.Placement.HostedDomain = "harbor.example\"\nalso_injected = 2 #"
	files, err := Render(m)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	for _, f := range files {
		if f.Path != "terraform.tfvars" || f.Root {
			continue
		}
		body := parsedBody(t, f.Path, f.Content)
		for name := range body.Attributes {
			if strings.Contains(name, "injected") {
				t.Errorf("terraform.tfvars holds the argument %s, which the placement's words started:\n%s", name, f.Content)
			}
		}

		return
	}
	t.Fatal("no terraform.tfvars is rendered")
}

// TestVariableNameIsAName: a variable's name is the code's own word, and the stack
// writes it as a name, so one that is not a name is refused before any file is rendered.
func TestVariableNameIsAName(t *testing.T) {
	t.Parallel()

	m := deriveFixture(t, "beacon", "placement-beacon.json")
	m.Variables[0].Name = `APP_X" = 1, "Y`
	_, err := Render(m)
	if want := "is not a name the stack can write"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Render() with a variable name that is not a name = %v, want %q", err, want)
	}
}
