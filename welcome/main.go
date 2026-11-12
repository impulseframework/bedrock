// Command welcome is the image a site runs on between its application's registration and
// its first release: the stack creates every Cloud Run service with it (placement.json,
// placeholderImage), and the pipeline's first deploy takes its place. It answers every
// request, at every path, with one page that says the site is set up and its
// application has not had a release yet.
//
// It serves on the port Cloud Run gives it (PORT), on one listener that speaks HTTP/1.1
// and unencrypted HTTP/2: the stack names the container port h2c for a site whose main
// package imports the framework's server, and leaves it an HTTP/1 port for any other, so
// the image that stands in for both has to speak both. It reads nothing else. The
// arguments are not read either: a site's service in a multi-site application is
// created with the site's binary as its container's arguments, which this entrypoint
// lets pass.
//
// The image is published once for each version (VERSION, and the workflow
// .github/workflows/welcome-image.yml): a tag is never moved, so a change to what the
// image is made of takes the next version, which the test here holds.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// page is the one page the image serves: a whole document with its style and its script
// written into it, which asks for nothing else.
//
//go:embed page.html
var page []byte

const (
	// defaultPort is the port served on when PORT is not set, Cloud Run's own default.
	defaultPort = "8080"
	// headerTimeout bounds how long a client may take over a request's headers, and
	// idleTimeout how long a kept connection may sit unused.
	headerTimeout = 10 * time.Second
	idleTimeout   = 2 * time.Minute
	// shutdownTimeout is how long the requests in flight get once Cloud Run asks the
	// instance to stop.
	shutdownTimeout = 10 * time.Second
)

func main() {
	os.Exit(run())
}

// run serves until the instance is told to stop (SIGTERM, as Cloud Run sends it, or an
// interrupt at a terminal) and answers the exit code.
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := listenAndServe(ctx, os.Getenv("PORT")); err != nil {
		fmt.Fprintln(os.Stderr, "welcome:", err)

		return 1
	}

	return 0
}

// listenAndServe serves the page on port, or on the default port when none is named.
func listenAndServe(ctx context.Context, port string) error {
	if port == "" {
		port = defaultPort
	}
	handler, err := newHandler(page)
	if err != nil {
		return err
	}
	var config net.ListenConfig
	listener, err := config.Listen(ctx, "tcp", net.JoinHostPort("", port))
	if err != nil {
		return fmt.Errorf("listening on port %s: %w", port, err)
	}

	return serve(ctx, listener, handler)
}

// serve answers on the listener in HTTP/1.1 and in unencrypted HTTP/2 until ctx is
// done, then lets the requests in flight finish.
func serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{
		Handler:           handler,
		Protocols:         protocols,
		ReadHeaderTimeout: headerTimeout,
		IdleTimeout:       idleTimeout,
	}
	failed := make(chan error, 1)
	go func() {
		failed <- server.Serve(listener)
	}()
	select {
	case err := <-failed:
		return fmt.Errorf("serving: %w", err)
	case <-ctx.Done():
	}
	// The context that ended is the one that says stop; the requests in flight get their
	// own time to finish.
	stopping, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(stopping); err != nil {
		return fmt.Errorf("stopping: %w", err)
	}

	return nil
}

// newHandler answers every request with the page: any path, any method, 200. The page
// is not to be indexed or kept (the application takes its place at the same address),
// and its content policy admits the one style and the one script written into it, by
// their hashes, an icon written into it, and nothing from anywhere else.
func newHandler(page []byte) (http.Handler, error) {
	policy, err := contentPolicy(page)
	if err != nil {
		return nil, err
	}
	length := strconv.Itoa(len(page))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Type", "text/html; charset=utf-8")
		header.Set("Content-Length", length)
		header.Set("Content-Security-Policy", policy)
		header.Set("Cache-Control", "no-store")
		header.Set("X-Robots-Tag", "noindex")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "no-referrer")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		// A client that went away is the only way this write fails, and there is
		// nobody left to tell.
		_, _ = w.Write(page)
	}), nil
}

// contentPolicy is the Content-Security-Policy the page is served under: nothing is
// loaded from anywhere, the style and the script are the ones in the page (their
// SHA-256, so an edit of either changes the policy with it), and the icon is the one
// written into its link.
func contentPolicy(page []byte) (string, error) {
	style, err := inlineHash(page, "style")
	if err != nil {
		return "", err
	}
	script, err := inlineHash(page, "script")
	if err != nil {
		return "", err
	}

	return "default-src 'none'; style-src " + style + "; script-src " + script +
		"; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'", nil
}

// inlineHash is the policy's source for the page's one element of the name, style or
// script: the SHA-256 of what stands between its tags, byte for byte, as a browser
// hashes it. A page with none of them, or with more than one, is refused: the policy
// names exactly one of each.
func inlineHash(page []byte, element string) (string, error) {
	open, shut := []byte("<"+element+">"), []byte("</"+element+">")
	if bytes.Count(page, open) != 1 || bytes.Count(page, shut) != 1 {
		return "", errors.New("the page holds " + strconv.Itoa(bytes.Count(page, open)) + " <" + element + "> element(s) without attributes, and its content policy names exactly one")
	}
	start := bytes.Index(page, open) + len(open)
	end := bytes.Index(page, shut)
	if end < start {
		return "", errors.New("the page closes its <" + element + "> element before it opens it")
	}
	sum := sha256.Sum256(page[start:end])

	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'", nil
}
