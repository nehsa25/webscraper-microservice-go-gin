//go:build e2e

// Package e2e_test builds the real binary, runs it as a separate process, and
// drives it only over HTTP.
//
// This tier answers "does the artifact we ship actually work", including the
// parts no in-process test can reach: environment parsing, the wiring in
// main(), the listen address, and shutdown on SIGTERM.
package e2e_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const samplePage = `<!doctype html>
<html><body>
  <header>ignore me</header>
  <div id="main"><p>The interesting part.</p></div>
</body></html>`

type service struct {
	baseURL string
	cmd     *exec.Cmd
	logs    *strings.Builder
}

func repoRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("locating repo root: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func buildBinary(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "webscraper")

	// Build the artifact rather than using `go run`, which differs in signal
	// handling and exit codes — two of the things this tier exists to check.
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	cmd.Dir = repoRoot(t)

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building: %v\n%s", err, out)
	}
	return binary
}

func freePort(t *testing.T) int {
	t.Helper()

	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer func() { _ = listener.Close() }()

	return listener.Addr().(*net.TCPAddr).Port
}

func startService(t *testing.T, binary string, env map[string]string) *service {
	t.Helper()

	port := freePort(t)
	logs := &strings.Builder{}

	// context.Background(), not t.Context(): the test context is cancelled
	// when the test ends, which would kill the process before Cleanup can stop
	// it gracefully — and graceful shutdown is one of the things under test.
	cmd := exec.CommandContext(context.Background(), binary)
	cmd.Env = append(os.Environ(), fmt.Sprintf("ADDR=127.0.0.1:%d", port))
	for k, v := range env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Stdout = logs
	cmd.Stderr = logs

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the service: %v", err)
	}

	svc := &service{baseURL: fmt.Sprintf("http://127.0.0.1:%d", port), cmd: cmd, logs: logs}

	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_, _ = cmd.Process.Wait()
		if t.Failed() {
			t.Logf("service output:\n%s", logs.String())
		}
	})

	waitForHTTP(t, svc.baseURL+"/health", 15*time.Second, logs)
	return svc
}

// waitForHTTP polls instead of sleeping. A fixed sleep is either too short
// (flaky) or too long (slow), and wrong on another machine either way.
func waitForHTTP(t *testing.T, url string, timeout time.Duration, logs *strings.Builder) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: time.Second}
	var lastErr error

	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("%s never became ready within %s (last: %v)\nservice output:\n%s",
		url, timeout, lastErr, logs.String())
}

func get(t *testing.T, url string) (int, string, http.Header) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp.StatusCode, string(body), resp.Header
}

func pageServer(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, samplePage)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The default configuration must refuse a loopback URL. This is the single
// most important assertion in the file: it runs against the REAL binary with
// the REAL default settings, so it proves the guard is on in production —
// which no in-process test can, because they all construct the guard directly.
func TestShippedDefaultsRefuseInternalAddresses(t *testing.T) {
	binary := buildBinary(t)

	svc := startService(t, binary, map[string]string{})

	for _, target := range []string{
		"http://127.0.0.1:8080/",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5/",
		"http://[::1]/",
	} {
		t.Run(target, func(t *testing.T) {
			status, body, _ := get(t, svc.baseURL+"/scraper?url="+target)

			if status != http.StatusForbidden {
				t.Errorf("status = %d for %s, want 403 (body: %s)", status, target, body)
			}
		})
	}
}

func TestShippedDefaultsRefuseNonHTTPSchemes(t *testing.T) {
	binary := buildBinary(t)

	svc := startService(t, binary, map[string]string{})

	for _, target := range []string{"file:///etc/passwd", "gopher://example.com/", "ftp://example.com/"} {
		t.Run(target, func(t *testing.T) {
			status, body, _ := get(t, svc.baseURL+"/scraper?url="+target)

			if status != http.StatusBadRequest {
				t.Errorf("status = %d for %s, want 400 (body: %s)", status, target, body)
			}
		})
	}
}

// ALLOW_PRIVATE_HOSTS is the documented development escape hatch. This test
// exercises the happy path through the real binary, and doubles as proof that
// the switch works — a security switch nobody has tested is a switch that
// might not do anything.
func TestScrapesAPageWhenPrivateHostsAreAllowed(t *testing.T) {
	binary := buildBinary(t)
	page := pageServer(t)

	svc := startService(t, binary, map[string]string{"ALLOW_PRIVATE_HOSTS": "true"})

	status, body, header := get(t, svc.baseURL+"/scraper?url="+page.URL+"/")

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", status, body)
	}
	if !strings.Contains(body, "The interesting part.") {
		t.Errorf("body did not contain the main content: %s", body)
	}
	if strings.Contains(body, "ignore me") {
		t.Errorf("body leaked content from outside #main: %s", body)
	}
	if got := header.Get("X-Matched-Selector"); got != "#main" {
		t.Errorf("X-Matched-Selector = %q, want #main", got)
	}
}

func TestMissingURLIsA400(t *testing.T) {
	binary := buildBinary(t)

	svc := startService(t, binary, map[string]string{})

	if status, body, _ := get(t, svc.baseURL+"/scraper"); status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", status, body)
	}
}

func TestKubernetesProbesAnswer(t *testing.T) {
	binary := buildBinary(t)

	svc := startService(t, binary, map[string]string{})

	for _, path := range []string{"/health", "/ready"} {
		t.Run(path, func(t *testing.T) {
			if status, body, _ := get(t, svc.baseURL+path); status != http.StatusOK {
				t.Errorf("status = %d, want 200 (body: %s)", status, body)
			}
		})
	}
}

func TestServiceRefusesToStartWithABadTimeout(t *testing.T) {
	binary := buildBinary(t)

	cmd := exec.CommandContext(t.Context(), binary)
	cmd.Env = append(os.Environ(), "ADDR=127.0.0.1:0", "REQUEST_TIMEOUT_SECONDS=soon")

	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("the service started with an invalid timeout; it should exit non-zero")
	}
	if !strings.Contains(string(out), "REQUEST_TIMEOUT_SECONDS") {
		t.Errorf("output did not explain the problem:\n%s", out)
	}
}

// Graceful shutdown is invisible to every other tier. If it breaks, rolling
// deploys drop in-flight requests and nobody notices until a customer does.
func TestServiceShutsDownGracefullyOnSIGTERM(t *testing.T) {
	binary := buildBinary(t)

	svc := startService(t, binary, map[string]string{})

	if err := svc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- svc.cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exited with %v, want a clean exit\n%s", err, svc.logs)
		}
		if !strings.Contains(svc.logs.String(), "stopped cleanly") {
			t.Errorf("no clean-stop log:\n%s", svc.logs)
		}
	case <-time.After(10 * time.Second):
		_ = svc.cmd.Process.Kill()
		t.Fatal("the service ignored SIGTERM for 10s")
	}
}

// Turning the guard off is a deliberate, loud decision. An operator reading the
// logs must be able to see that this instance can reach internal addresses.
func TestAllowPrivateHostsIsLoggedAsAWarning(t *testing.T) {
	binary := buildBinary(t)

	svc := startService(t, binary, map[string]string{"ALLOW_PRIVATE_HOSTS": "true"})

	if !strings.Contains(svc.logs.String(), "WARNING") {
		t.Errorf("no warning logged when the SSRF guard is disabled:\n%s", svc.logs)
	}
}
