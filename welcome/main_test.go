package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// asProgram is the variable that makes the test binary run the program in place of
// the tests, which is how the test of the program starts it: with a port, with and
// without arguments, and with the signal Cloud Run stops an instance with.
const asProgram = "WELCOME_TEST_AS_PROGRAM"

func TestMain(m *testing.M) {
	if os.Getenv(asProgram) == "1" {
		main()
	}
	os.Exit(m.Run())
}

// TestProgram runs the program as Cloud Run does: on the port PORT names, with no
// arguments (a single-site application's service) and with a site's binary as its
// arguments (a multi-site application's), which it does not read. Either way it
// answers the page over HTTP/1.1 and over unencrypted HTTP/2 with prior knowledge
// (the port name h2c), and stops cleanly on SIGTERM.
func TestProgram(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "no arguments"},
		{name: "a site's binary as the arguments", args: []string{"/console"}},
		{name: "arguments shaped like flags", args: []string{"--port", "1", "-h"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			port := freePort(t)
			cmd := exec.CommandContext(t.Context(), os.Args[0], test.args...)
			cmd.Env = append(os.Environ(), asProgram+"=1", "PORT="+port)
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatalf("starting the program: %v", err)
			}
			address := "http://127.0.0.1:" + port + "/any/path?of=the&site"
			awaitServing(t, address)
			for _, protocol := range []string{"HTTP/1.1", "HTTP/2.0"} {
				spoken, status, body := get(t, protocol, address)
				if spoken != protocol || status != http.StatusOK || !bytes.Equal(body, page) {
					t.Errorf("over %s: answered %s %d with %d bytes, want %s 200 with the page's %d", protocol, spoken, status, len(body), protocol, len(page))
				}
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatalf("stopping the program: %v", err)
			}
			if err := cmd.Wait(); err != nil {
				t.Errorf("the program stopped with %v, want a clean exit; it wrote:\n%s", err, output.String())
			}
		})
	}
}

// TestServe holds serve to its two protocols on one listener and to its stop: it
// answers until its context ends, and then returns without an error.
func TestServe(t *testing.T) {
	t.Parallel()

	handler, err := newHandler(page)
	if err != nil {
		t.Fatalf("newHandler() error = %v", err)
	}
	var config net.ListenConfig
	listener, err := config.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, listener, handler)
	}()
	address := "http://" + listener.Addr().String() + "/"
	for _, protocol := range []string{"HTTP/1.1", "HTTP/2.0"} {
		spoken, status, body := get(t, protocol, address)
		if spoken != protocol || status != http.StatusOK || !bytes.Equal(body, page) {
			t.Errorf("over %s: answered %s %d with %d bytes, want %s 200 with the page's %d", protocol, spoken, status, len(body), protocol, len(page))
		}
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve() error = %v, want none once its context ends", err)
		}
	case <-time.After(shutdownTimeout + 5*time.Second):
		t.Error("serve() did not return once its context ended")
	}
}

// TestHandler holds the handler to its answer: the page with 200 at every path and for
// every method (a HEAD gets the headers alone), never indexed, never kept, under a
// content policy that admits the page's own style and script by their hashes and
// nothing from anywhere else.
func TestHandler(t *testing.T) {
	t.Parallel()

	handler, err := newHandler(page)
	if err != nil {
		t.Fatalf("newHandler() error = %v", err)
	}
	wantPolicy := "default-src 'none'; style-src " + hashOf(t, "style") + "; script-src " + hashOf(t, "script") +
		"; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	for _, test := range []struct {
		method string
		path   string
		body   bool
	}{
		{method: http.MethodGet, path: "/", body: true},
		{method: http.MethodGet, path: "/console/api/user/session", body: true},
		{method: http.MethodGet, path: "/favicon.ico", body: true},
		{method: http.MethodGet, path: "/healthz?probe=1", body: true},
		{method: http.MethodPost, path: "/console/api/orders", body: true},
		{method: http.MethodDelete, path: "/anything", body: true},
		{method: http.MethodHead, path: "/"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), test.method, test.path, strings.NewReader("ignored")))
			if recorder.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", recorder.Code)
			}
			for name, want := range map[string]string{
				"Content-Type":            "text/html; charset=utf-8",
				"Content-Length":          fmt.Sprint(len(page)),
				"Content-Security-Policy": wantPolicy,
				"Cache-Control":           "no-store",
				"X-Robots-Tag":            "noindex",
				"X-Content-Type-Options":  "nosniff",
				"Referrer-Policy":         "no-referrer",
			} {
				if got := recorder.Header().Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			want := page
			if !test.body {
				want = nil
			}
			if got := recorder.Body.Bytes(); !bytes.Equal(got, want) {
				t.Errorf("the body is %d bytes, want %d", len(got), len(want))
			}
		})
	}
}

// TestContentPolicy holds the policy to exactly one style and one script: a page with
// none, or with a second one, has no policy and so is not served.
func TestContentPolicy(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		page    string
		wantErr string
	}{
		{name: "one of each", page: "<style>a{}</style><script>1</script>"},
		{name: "no style", page: "<script>1</script>", wantErr: "the page holds 0 <style> element(s) without attributes, and its content policy names exactly one"},
		{name: "two scripts", page: "<style>a{}</style><script>1</script><script>2</script>", wantErr: "the page holds 2 <script> element(s) without attributes, and its content policy names exactly one"},
		{name: "a script closed before it opens", page: "<style>a{}</style></script><script>", wantErr: "the page closes its <script> element before it opens it"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := contentPolicy([]byte(test.page))
			switch {
			case test.wantErr == "" && err != nil:
				t.Errorf("contentPolicy() error = %v, want none", err)
			case test.wantErr != "" && (err == nil || err.Error() != test.wantErr):
				t.Errorf("contentPolicy() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

// TestPage holds the page to what it promises: a whole document that says what it is
// for in its settled words, asks for nothing from anywhere (the only address in it
// is the XML namespace of the icon written into its link), keeps nothing in the
// browser, follows the device's light or dark setting and its reduced-motion setting,
// opens with its one switch off, and carries nothing of the preview it was designed in.
func TestPage(t *testing.T) {
	t.Parallel()

	text := string(page)
	for _, want := range []string{
		"<!doctype html>",
		`<html lang="en">`,
		`<meta name="robots" content="noindex">`,
		"<title>Impulse Framework</title>",
		`<h1 class="mark">`,
		`aria-label="Impulse Framework"`,
		"The site is up and running, waiting for the first application release.",
		"<b>impulse,</b> noun. A force applied over a short time",
		`<span class="formula">(J = F &times; &Delta;t):</span>`,
		`it is what sets things in <span class="pair"><span class="mover">motion</span>`,
		// The words a ripple draws out of the last word, and the switch that turns them
		// on, off as the page opens.
		`data-words="action progress momentum results" hidden>action</span>`,
		`<button class="switch" type="button" aria-pressed="false"`,
		"@media (prefers-color-scheme: dark)",
		"@media (prefers-reduced-motion: reduce)",
		`window.matchMedia("(prefers-reduced-motion: reduce)")`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	for _, unwanted := range []string{"data-theme", "claude", "@import", "src=", "srcset=", "fetch(", "XMLHttpRequest", "WebSocket", "<iframe", "<img", "<link rel=\"stylesheet\"", " style=\"", "localStorage", "sessionStorage", "document.cookie"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("the page holds %q", unwanted)
		}
	}
	const namespace = "http://www.w3.org/2000/svg"
	if rest := strings.ReplaceAll(text, namespace, ""); strings.Contains(rest, "http:") || strings.Contains(rest, "https:") || strings.Contains(rest, `"//`) {
		t.Errorf("the page names an address beside the SVG namespace of its icon")
	}
	for _, reference := range regexp.MustCompile(`url\([^)]*\)`).FindAllString(text, -1) {
		if reference != "url(#cobalt)" {
			t.Errorf("the page refers to %s, want nothing but its own gradient", reference)
		}
	}
	if links := regexp.MustCompile(`<link [^>]*>`).FindAllString(text, -1); len(links) != 1 || !strings.HasPrefix(links[0], `<link rel="icon" href="data:image/svg+xml,`) {
		t.Errorf("the page's links are %q, want the one icon written into its own link", links)
	}
}

// versionFile is the record of the image's version: the version, which is the image's
// tag, and the SHA-256 of what the image is made of (sources, below).
const versionFile = "VERSION"

// TestVersion holds the record to the sources. A tag is published once and never
// moved, so the image a version names is made of exactly the sources recorded for it:
// a change to any of them takes the next version.
func TestVersion(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(versionFile)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	record := regexp.MustCompile(`\A(v[1-9]\d*) ([0-9a-f]{64})\n\z`).FindStringSubmatch(string(data))
	if record == nil {
		t.Fatalf("%s holds %q, want one line: the version (v1, v2, ...), a space, and the SHA-256 of the image's sources", versionFile, data)
	}
	if got := sources(t); got != record[2] {
		t.Errorf(`the image's sources are %s, and %s records %s for %s.

A tag is never moved once it is published (.github/workflows/welcome-image.yml refuses
a version whose published image was made of other sources). When %s is published
already, this change takes the next version: raise it in %s with the hash above; once
the merge has published it, a second change names its digest in derive.PlaceholderImage
and its version in derive.PlaceholderVersion. When it has never been published, record the hash
above beside it.`, got, versionFile, record[2], record[1], record[1], versionFile)
	}
}

// sources is the SHA-256 of what the image is made of: every file of this directory
// that is not a test and not the record itself, by name and content, in name order.
func sources(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("os.ReadDir() error = %v", err)
	}
	sum := sha256.New()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == versionFile || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("os.ReadFile() error = %v", err)
		}
		fmt.Fprintf(sum, "%s %d\n", name, len(data))
		sum.Write(data)
	}

	return hex.EncodeToString(sum.Sum(nil))
}

// hashOf is the content policy's source for the page's one element of the name, as a
// browser computes it: the SHA-256 of what stands between its tags.
func hashOf(t *testing.T, element string) string {
	t.Helper()
	parts := regexp.MustCompile(`(?s)<` + element + `>(.*)</` + element + `>`).FindSubmatch(page)
	if parts == nil {
		t.Fatalf("the page has no <%s> element", element)
	}
	sum := sha256.Sum256(parts[1])

	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// freePort is a port nothing listens on, for the program to serve on.
func freePort(t *testing.T) string {
	t.Helper()
	var config net.ListenConfig
	listener, err := config.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("net.SplitHostPort() error = %v", err)
	}

	return port
}

// awaitServing waits until the address answers, as long as a program takes to start.
func awaitServing(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodHead, address, http.NoBody)
		if err != nil {
			t.Fatalf("http.NewRequestWithContext() error = %v", err)
		}
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			_ = response.Body.Close()

			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not answer: %v", address, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// get asks the address in one protocol alone, HTTP/1.1 or unencrypted HTTP/2 with prior
// knowledge (what Cloud Run speaks to a container port named h2c), and answers the
// protocol the answer came in, its status and its body.
func get(t *testing.T, protocol, address string) (spoken string, status int, body []byte) {
	t.Helper()
	protocols := new(http.Protocols)
	if protocol == "HTTP/2.0" {
		protocols.SetUnencryptedHTTP2(true)
	} else {
		protocols.SetHTTP1(true)
	}
	transport := &http.Transport{Protocols: protocols}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, http.NoBody)
	if err != nil {
		t.Fatalf("http.NewRequestWithContext() error = %v", err)
	}
	response, err := (&http.Client{Transport: transport, Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("over %s: %v", protocol, err)
	}
	defer response.Body.Close()
	body, err = io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("over %s: reading the answer: %v", protocol, err)
	}

	return response.Proto, response.StatusCode, body
}
