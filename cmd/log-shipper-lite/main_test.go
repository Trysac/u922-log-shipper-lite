package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRunCLI(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"-invalid-flag"}, &stdout, &stderr); code != 2 {
		t.Fatalf("Run(-invalid-flag) = %d, want 2", code)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{}, &stdout, &stderr); code != 2 {
		t.Fatalf("Run() = %d, want 2", code)
	}

	dir := t.TempDir()
	logFile := filepath.Join(dir, "a.log")
	if err := os.WriteFile(logFile, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cpFile := filepath.Join(dir, "cp.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	stdout.Reset()
	stderr.Reset()
	code := Run([]string{"-once", "-endpoint", srv.URL, "-checkpoint", cpFile, logFile}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run(-once) = %d, stderr=%s", code, stderr.String())
	}

	// Test rejected returns exit code 1
	rejSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer rejSrv.Close()

	logFile2 := filepath.Join(dir, "b.log")
	if err := os.WriteFile(logFile2, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"-once", "-endpoint", rejSrv.URL, logFile2}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("Run(-once rejected) = %d, want 1", code)
	}
}
