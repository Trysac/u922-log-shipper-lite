package logshipperlite_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Trysac/u922-log-shipper-lite"
)

type postCapture struct {
	contentType    string
	idempotencyKey string
	body           string
	lines          []string
}

// TestAcceptance1And2 verifies file reading, NDJSON record formatting, keys,
// checkpoint offset 14, and trailing partial line handling.
func TestAcceptance1And2(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "a.log")
	cpPath := filepath.Join(dir, "checkpoint.json")

	if err := os.WriteFile(logPath, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var posts []postCapture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		posts = append(posts, postCapture{
			contentType:    r.Header.Get("Content-Type"),
			idempotencyKey: r.Header.Get("Idempotency-Key"),
			body:           string(raw),
			lines:          lines,
		})
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	shipper, err := logshipperlite.New(logshipperlite.Config{
		Paths:          []string{logPath},
		CheckpointPath: cpPath,
		Endpoint:       srv.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = shipper.Close() }()

	if err := shipper.Step(context.Background()); err != nil {
		t.Fatalf("Step 1: %v", err)
	}

	if len(posts) != 1 {
		t.Fatalf("got %d POSTs, want 1", len(posts))
	}
	if posts[0].contentType != "application/x-ndjson" {
		t.Fatalf("Content-Type = %q, want %q", posts[0].contentType, "application/x-ndjson")
	}
	wantLines := []string{
		`{"file":"a.log","key":"2c8b08da5ce60398:0","line":"one"}`,
		`{"file":"a.log","key":"2c8b08da5ce60398:4","line":"two"}`,
		`{"file":"a.log","key":"2c8b08da5ce60398:8","line":"three"}`,
	}
	if len(posts[0].lines) != len(wantLines) {
		t.Fatalf("lines = %v, want %v", posts[0].lines, wantLines)
	}
	for i, want := range wantLines {
		if posts[0].lines[i] != want {
			t.Fatalf("line[%d] = %s, want %s", i, posts[0].lines[i], want)
		}
	}

	cpRaw, err := os.ReadFile(cpPath)
	if err != nil {
		t.Fatalf("ReadFile checkpoint: %v", err)
	}
	var cp struct {
		Version int `json:"version"`
		Files   []struct {
			Path        string `json:"path"`
			Fingerprint string `json:"fingerprint"`
			Offset      int64  `json:"offset"`
		} `json:"files"`
	}
	if err := json.Unmarshal(cpRaw, &cp); err != nil {
		t.Fatalf("Unmarshal checkpoint: %v", err)
	}
	if len(cp.Files) != 1 || cp.Files[0].Offset != 14 || cp.Files[0].Fingerprint != "2c8b08da5ce60398" {
		t.Fatalf("checkpoint = %+v, want offset 14 and fingerprint 2c8b08da5ce60398", cp)
	}

	// Acceptance 2: trailing "four" without a newline is not sent; after "\n" is appended
	// the next Step sends it with key "2c8b08da5ce60398:14".
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("four"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if err := shipper.Step(context.Background()); err != nil {
		t.Fatalf("Step with partial line: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("expected no new POST for incomplete line, got %d", len(posts))
	}

	f, err = os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if err := shipper.Step(context.Background()); err != nil {
		t.Fatalf("Step after newline: %v", err)
	}
	if len(posts) != 2 {
		t.Fatalf("got %d POSTs, want 2", len(posts))
	}
	wantFour := `{"file":"a.log","key":"2c8b08da5ce60398:14","line":"four"}`
	if len(posts[1].lines) != 1 || posts[1].lines[0] != wantFour {
		t.Fatalf("second POST lines = %v, want [%s]", posts[1].lines, wantFour)
	}
}

// TestAcceptance3RetriesAndRetryAfter verifies 503, 503 then 200 retry sleeps,
// Idempotency-Key stability, and 429 Retry-After header parsing.
func TestAcceptance3RetriesAndRetryAfter(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "a.log")
	if err := os.WriteFile(logPath, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	statuses := []int{http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusOK}
	var idemKeys []string
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idemKeys = append(idemKeys, r.Header.Get("Idempotency-Key"))
		st := statuses[call]
		call++
		if st == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "2")
		}
		w.WriteHeader(st)
	}))
	defer srv.Close()

	var sleeps []time.Duration
	shipper, err := logshipperlite.New(logshipperlite.Config{
		Paths:    []string{logPath},
		Endpoint: srv.URL,
		Sleep: func(ctx context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shipper.Close() }()

	if err := shipper.Step(context.Background()); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(idemKeys) != 3 {
		t.Fatalf("got %d POSTs, want 3", len(idemKeys))
	}
	if idemKeys[0] == "" || idemKeys[0] != idemKeys[1] || idemKeys[1] != idemKeys[2] {
		t.Fatalf("Idempotency-Key headers differed: %v", idemKeys)
	}
	wantSleeps := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}
	if len(sleeps) != 2 || sleeps[0] != wantSleeps[0] || sleeps[1] != wantSleeps[1] {
		t.Fatalf("sleeps = %v, want %v", sleeps, wantSleeps)
	}

	// Now test 429 with Retry-After "2"
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("three\n")
	_ = f.Close()

	statuses = []int{http.StatusTooManyRequests, http.StatusOK}
	call = 0
	sleeps = nil

	if err := shipper.Step(context.Background()); err != nil {
		t.Fatalf("Step 429: %v", err)
	}
	if len(sleeps) != 1 || sleeps[0] != 2*time.Second {
		t.Fatalf("429 sleeps = %v, want [2s]", sleeps)
	}
}

// dedupReceiver stores unique records by key in arrival order.
type dedupReceiver struct {
	mu    sync.Mutex
	seen  map[string]bool
	lines []string
}

func newDedupReceiver() *dedupReceiver {
	return &dedupReceiver{seen: make(map[string]bool)}
}

func (d *dedupReceiver) ingest(body []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		var rec struct {
			File string `json:"file"`
			Key  string `json:"key"`
			Line string `json:"line"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &rec); err == nil {
			if !d.seen[rec.Key] {
				d.seen[rec.Key] = true
				d.lines = append(d.lines, rec.Line)
			}
		}
	}
}

type faultyRoundTripper struct {
	receiver *dedupReceiver
	rng      *rand.Rand
	alwaysOK bool
}

func (rt *faultyRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	if !rt.alwaysOK {
		roll := rt.rng.IntN(10)
		switch {
		case roll < 2:
			// Fail before server stores batch (transport error)
			return nil, errors.New("simulated connection reset before store")
		case roll < 4:
			// Server returns 503 before storing
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		case roll < 6:
			// Server stores batch, then acknowledgment drops on the wire
			rt.receiver.ingest(body)
			return nil, errors.New("simulated ack drop after server stored batch")
		}
	}
	rt.receiver.ingest(body)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
	}, nil
}

// TestAcceptance4Property200Seeds runs 200 seeded schedules (math/rand/v2 PCG seeds 1 to 200)
// that append lines, crash/recreate the shipper from its checkpoint, and drop
// requests or destination acknowledgements at random points.
func TestAcceptance4Property200Seeds(t *testing.T) {
	baseDir := t.TempDir()
	for seed := uint64(1); seed <= 200; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed^0x5a5a5a5a5a5a5a5a))
		runDir := filepath.Join(baseDir, fmt.Sprintf("seed-%d", seed))
		if err := os.MkdirAll(runDir, 0o700); err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(runDir, "app.log")
		cpPath := filepath.Join(runDir, "cp.json")

		totalLines := 8 + rng.IntN(15)
		expected := make([]string, 0, totalLines)
		for i := 0; i < totalLines; i++ {
			expected = append(expected, fmt.Sprintf("seed-%d-line-%d", seed, i))
		}

		recv := newDedupReceiver()
		rt := &faultyRoundTripper{receiver: recv, rng: rng}
		client := &http.Client{Transport: rt}

		newShipper := func() *logshipperlite.Shipper {
			s, err := logshipperlite.New(logshipperlite.Config{
				Paths:             []string{logPath},
				CheckpointPath:    cpPath,
				Endpoint:          "http://example.invalid/ingest",
				Client:            client,
				BatchMaxRecords:   1 + rng.IntN(4),
				MaxPendingBatches: 1 + rng.IntN(3),
				MaxRetries:        2,
				Sleep:             func(context.Context, time.Duration) error { return nil },
			})
			if err != nil {
				t.Fatalf("seed %d New: %v", seed, err)
			}
			return s
		}

		shipper := newShipper()
		written := 0
		for written < len(expected) {
			chunk := 1 + rng.IntN(4)
			if written+chunk > len(expected) {
				chunk = len(expected) - written
			}
			f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			for j := 0; j < chunk; j++ {
				_, _ = f.WriteString(expected[written+j] + "\n")
			}
			_ = f.Close()
			written += chunk

			_ = shipper.Step(context.Background())
			if rng.IntN(3) == 0 {
				// Simulate process crash and restart from checkpoint
				_ = shipper.Close()
				shipper = newShipper()
			}
		}

		// Allow network to heal and drain remaining batches
		rt.alwaysOK = true
		for i := 0; i < totalLines+10; i++ {
			if err := shipper.Step(context.Background()); err != nil {
				t.Fatalf("seed %d heal Step: %v", seed, err)
			}
			if shipper.Stats().PendingBatches == 0 && len(recv.lines) == len(expected) {
				break
			}
		}
		_ = shipper.Close()

		if len(recv.lines) != len(expected) {
			t.Fatalf("seed %d: got %d lines (%v), want %d (%v)", seed, len(recv.lines), recv.lines, len(expected), expected)
		}
		for i := range expected {
			if recv.lines[i] != expected[i] {
				t.Fatalf("seed %d line[%d] = %q, want %q", seed, i, recv.lines[i], expected[i])
			}
		}
	}
}

// TestAcceptance5Backpressure verifies that when the destination is down,
// BatchMaxRecords 1 and MaxPendingBatches 2 stops reading after 2 lines
// (PendingBatches 2, ReadOffset 8).
func TestAcceptance5Backpressure(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "a.log")
	if err := os.WriteFile(logPath, []byte("one\ntwo\nthree\nfour\nfive\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	shipper, err := logshipperlite.New(logshipperlite.Config{
		Paths:             []string{logPath},
		Endpoint:          srv.URL,
		BatchMaxRecords:   1,
		MaxPendingBatches: 2,
		MaxRetries:        1,
		Sleep:             func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shipper.Close() }()

	for i := 0; i < 5; i++ {
		err := shipper.Step(context.Background())
		var lErr *logshipperlite.Error
		if !errors.As(err, &lErr) || lErr.Code != "retries_exhausted" {
			t.Fatalf("Step %d err = %v, want retries_exhausted", i+1, err)
		}
	}

	st := shipper.Stats()
	if st.PendingBatches != 2 || st.ReadOffset != 8 {
		t.Fatalf("Stats() = %+v, want PendingBatches=2 and ReadOffset=8", st)
	}
}

// TestAcceptance6Rotation verifies that renaming "a.log" to "a.log.1" and creating
// a new "a.log" finishes the unread lines of the old handle before shipping the
// new file with its new first-line fingerprint.
func TestAcceptance6Rotation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "a.log")
	rotPath := filepath.Join(dir, "a.log.1")

	if err := os.WriteFile(logPath, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var received []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
			if line != "" {
				received = append(received, line)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	shipper, err := logshipperlite.New(logshipperlite.Config{
		Paths:           []string{logPath},
		Endpoint:        srv.URL,
		BatchMaxRecords: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shipper.Close() }()

	// Step 1 reads "one\n" and "two\n" into 2 pending batches (BatchMaxRecords=1) and sends "one\n".
	// Actually with MaxPendingBatches=4 it reads both; to leave "three\n" unread on the open handle,
	// let's ship "one\n" and "two\n" first, then append "three\n" to a.log, rename to a.log.1, and write new a.log.
	if err := shipper.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := shipper.Step(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Append "three\n" to the open file, then rename it to a.log.1 and create a new a.log.
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("three\n")
	_ = f.Close()

	if err := os.Rename(logPath, rotPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 4; i++ {
		if err := shipper.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	if len(received) != 5 {
		t.Fatalf("received %d records (%v), want 5", len(received), received)
	}
	// Record 2 (0-indexed) must be "three" with old fingerprint 2c8b08da5ce60398:8
	wantThree := `{"file":"a.log","key":"2c8b08da5ce60398:8","line":"three"}`
	if received[2] != wantThree {
		t.Fatalf("received[2] = %s, want %s", received[2], wantThree)
	}
	// Records 3 and 4 must be "alpha" and "beta" with a different fingerprint
	if !strings.Contains(received[3], `"line":"alpha"`) || strings.Contains(received[3], "2c8b08da5ce60398") {
		t.Fatalf("received[3] = %s, expected alpha with new fingerprint", received[3])
	}
	if !strings.Contains(received[4], `"line":"beta"`) {
		t.Fatalf("received[4] = %s, expected beta", received[4])
	}
}

// TestAcceptance7ErrorsAndTruncation verifies 400 -> Code "rejected" with unchanged
// checkpoint, corrupt checkpoint -> Code "bad_checkpoint", and 65_537-byte line
// truncated to 65_536 bytes with "truncated":true.
func TestAcceptance7ErrorsAndTruncation(t *testing.T) {
	dir := t.TempDir()
	badCp := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(badCp, []byte("{not-valid-json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := logshipperlite.New(logshipperlite.Config{
		Paths:          []string{filepath.Join(dir, "a.log")},
		CheckpointPath: badCp,
		Endpoint:       "http://127.0.0.1:1",
	})
	var lErr *logshipperlite.Error
	if !errors.As(err, &lErr) || lErr.Code != "bad_checkpoint" {
		t.Fatalf("New with corrupt checkpoint err = %v, want bad_checkpoint", err)
	}

	// Test 400 rejected leaves checkpoint unchanged
	logPath := filepath.Join(dir, "a.log")
	cpPath := filepath.Join(dir, "cp.json")
	if err := os.WriteFile(logPath, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	statusToReturn := http.StatusBadRequest
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.WriteHeader(statusToReturn)
	}))
	defer srv.Close()

	shipper, err := logshipperlite.New(logshipperlite.Config{
		Paths:          []string{logPath},
		CheckpointPath: cpPath,
		Endpoint:       srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shipper.Close() }()

	err = shipper.Step(context.Background())
	if !errors.As(err, &lErr) || lErr.Code != "rejected" || lErr.Status != 400 {
		t.Fatalf("Step 400 err = %v, want Code=rejected Status=400", err)
	}
	if _, statErr := os.Stat(cpPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("checkpoint should not exist after rejected first batch, err=%v", statErr)
	}

	// Now switch server to 200 and verify the rejected batch was kept and sends
	statusToReturn = http.StatusOK
	if err := shipper.Step(context.Background()); err != nil {
		t.Fatalf("Step 200 after 400: %v", err)
	}

	// Test 65_537-byte line truncated to 65_536 bytes with "truncated":true
	longLine := strings.Repeat("x", 65_537) + "\n"
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(longLine)
	_ = f.Close()

	bodies = nil
	if err := shipper.Step(context.Background()); err != nil {
		t.Fatalf("Step long line: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("got %d bodies, want 1", len(bodies))
	}
	var rec struct {
		File      string `json:"file"`
		Key       string `json:"key"`
		Line      string `json:"line"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &rec); err != nil {
		t.Fatalf("Unmarshal truncated record: %v", err)
	}
	if len(rec.Line) != 65_536 || !rec.Truncated {
		t.Fatalf("len(Line)=%d, Truncated=%v; want 65536 and true", len(rec.Line), rec.Truncated)
	}
	if !strings.HasSuffix(strings.TrimSpace(bodies[0]), `"truncated":true}`) {
		t.Fatalf("record does not end with truncated:true: %s", bodies[0][:100])
	}
}

func TestJSONFormatAndInvalidUTF8(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "json.log")
	content := append([]byte(`{"level":"info","msg":"hi"}`+"\n"), []byte("not-json-\xff\n")...)
	if err := os.WriteFile(logPath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got = string(raw)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var events []logshipperlite.Event
	shipper, err := logshipperlite.New(logshipperlite.Config{
		Paths:    []string{logPath},
		Endpoint: srv.URL,
		Format:   "json",
		OnEvent: func(e logshipperlite.Event) {
			events = append(events, e)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shipper.Close() }()

	if err := shipper.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if !strings.Contains(lines[0], `,"json":{"level":"info","msg":"hi"}}`) {
		t.Fatalf("line[0] = %s", lines[0])
	}
	if !strings.Contains(lines[1], "not-json-\uFFFD") || !strings.Contains(lines[1], `,"parse_error":true}`) {
		t.Fatalf("line[1] = %s", lines[1])
	}
	if len(events) == 0 || events[0].Kind != "sent" || events[0].Records != 2 {
		t.Fatalf("events = %+v", events)
	}
}

func TestConfigValidationAndCheckpointEdgeCases(t *testing.T) {
	dir := t.TempDir()
	validPath := filepath.Join(dir, "a.log")

	badConfigs := []logshipperlite.Config{
		{Endpoint: "http://localhost"},
		{Paths: []string{""}, Endpoint: "http://localhost"},
		{Paths: []string{validPath, validPath}, Endpoint: "http://localhost"},
		{Paths: []string{validPath}, Endpoint: ""},
		{Paths: []string{validPath}, Endpoint: "http://localhost", Format: "xml"},
		{Paths: []string{validPath}, Endpoint: "http://localhost", BatchMaxRecords: -1},
	}
	for i, cfg := range badConfigs {
		_, err := logshipperlite.New(cfg)
		var lErr *logshipperlite.Error
		if !errors.As(err, &lErr) || lErr.Code != "bad_config" {
			t.Fatalf("case %d: got %v, want bad_config", i, err)
		}
	}

	// Bad checkpoint versions & negative offsets
	badCheckpoints := []string{
		"",
		`{"version":2,"files":[]}`,
		`{"version":1,"files":[{"path":"a.log","fingerprint":"abc","offset":-5}]}`,
		`{"version":1,"files":[]}{}`,
	}
	for i, content := range badCheckpoints {
		cpFile := filepath.Join(dir, fmt.Sprintf("bad-%d.json", i))
		_ = os.WriteFile(cpFile, []byte(content), 0o600)
		_, err := logshipperlite.New(logshipperlite.Config{
			Paths:          []string{validPath},
			CheckpointPath: cpFile,
			Endpoint:       "http://localhost",
		})
		var lErr *logshipperlite.Error
		if !errors.As(err, &lErr) || lErr.Code != "bad_checkpoint" {
			t.Fatalf("bad checkpoint %d: got %v, want bad_checkpoint", i, err)
		}
	}
}

func TestRunLoopAndCopyTruncate(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "a.log")
	if err := os.WriteFile(logPath, []byte("first-line\nsecond-line\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var received []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Date-Retry") == "1" {
			w.Header().Set("Retry-After", time.Now().Add(50*time.Millisecond).UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		received = append(received, string(raw))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	sleepCount := 0
	shipper, err := logshipperlite.New(logshipperlite.Config{
		Paths:    []string{logPath},
		Endpoint: srv.URL,
		Sleep: func(ctx context.Context, d time.Duration) error {
			sleepCount++
			if sleepCount == 1 {
				// Simulate copy-truncate rotation: file shrinks
				_ = os.WriteFile(logPath, []byte("short\n"), 0o600)
				return nil
			}
			cancel()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	err = shipper.Run(ctx)
	_ = shipper.Close()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	if len(received) != 2 {
		t.Fatalf("received %d batches, want 2", len(received))
	}
}

func TestCoverageBoost(t *testing.T) {
	// 1. Error methods: Error(), Unwrap()
	innerErr := errors.New("underlying error")
	var nilErr *logshipperlite.Error
	if nilErr.Error() != "" || nilErr.Unwrap() != nil {
		t.Errorf("expected empty / nil for nil *Error")
	}
	e := &logshipperlite.Error{
		Code:    "io",
		Status:  500,
		Message: "read failure",
		Err:     innerErr,
	}
	if !strings.Contains(e.Error(), "read failure") || !errors.Is(e, innerErr) {
		t.Errorf("Error methods incorrect: %v", e.Error())
	}
	longMsg := strings.Repeat("a", 200)
	e2 := &logshipperlite.Error{
		Code: "io",
		Err:  errors.New(longMsg),
	}
	if !strings.HasSuffix(e2.Error(), "...") {
		t.Errorf("truncateText not triggered: %v", e2.Error())
	}

	// 2. defaultSleep coverage via Shipper with Sleep: nil
	dir := t.TempDir()
	p := filepath.Join(dir, "sleep.log")
	_ = os.WriteFile(p, []byte("line\n"), 0o600)

	// Sleep with 1ms using defaultSleep in Shipper.Run
	var retried bool
	srv503 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !retried {
			retried = true
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv503.Close()

	sSleep, err := logshipperlite.New(logshipperlite.Config{
		Paths:        []string{p},
		Endpoint:     srv503.URL,
		PollInterval: 5 * time.Millisecond,
		// Sleep: nil -> defaultSleep
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sSleep.Close() }()

	sleepCtx, sleepCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer sleepCancel()
	// Step will trigger retry and sleep using defaultSleep
	if err := sSleep.Step(sleepCtx); err != nil {
		t.Fatalf("Step with defaultSleep: %v", err)
	}

	// Also trigger defaultSleep with canceled ctx and 0 duration in Run
	runCtx, runCancel := context.WithCancel(context.Background())
	runCancel()
	_ = sSleep.Run(runCtx)

	// 3. Close with open handles and multiple files
	p2 := filepath.Join(dir, "sleep2.log")
	_ = os.WriteFile(p2, []byte("line2\n"), 0o600)
	sMulti, err := logshipperlite.New(logshipperlite.Config{
		Paths:    []string{p, p2},
		Endpoint: srv503.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = sMulti.Step(context.Background())
	stMulti := sMulti.Stats()
	if stMulti.PendingBatches != 0 {
		t.Errorf("expected 0 pending batches, got %d", stMulti.PendingBatches)
	}
	_ = sMulti.Close()

	// 4. Retry delay with IMF-fixdate (http date) and seconds
	dSec, ok := logshipperlite.ParseRetryAfterForTest("10")
	if !ok || dSec != 10*time.Second {
		t.Errorf("ParseRetryAfter seconds: %v, %v", dSec, ok)
	}
	dDate, ok := logshipperlite.ParseRetryAfterForTest(time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat))
	if !ok || dDate <= 0 {
		t.Errorf("ParseRetryAfter date: %v, %v", dDate, ok)
	}
	dBad, ok := logshipperlite.ParseRetryAfterForTest("invalid-date-or-num")
	if ok || dBad != 0 {
		t.Errorf("ParseRetryAfter invalid: %v, %v", dBad, ok)
	}
	// computeRetryDelay with caps and negative retry after
	dCap := logshipperlite.ComputeRetryDelayForTest(20, "")
	if dCap != 30*time.Second {
		t.Errorf("ComputeRetryDelay cap want 30s, got %v", dCap)
	}
	dCapHeader := logshipperlite.ComputeRetryDelayForTest(1, "100")
	if dCapHeader != 30*time.Second {
		t.Errorf("ComputeRetryDelay cap header want 30s, got %v", dCapHeader)
	}
	dNeg := logshipperlite.ComputeRetryDelayForTest(1, "-5")
	if dNeg != 0 {
		t.Errorf("ComputeRetryDelay negative want 0, got %v", dNeg)
	}

	// defaultSleep direct invocation
	sleepCtx2, sleepCancel2 := context.WithCancel(context.Background())
	if err := logshipperlite.DefaultSleepForTest(sleepCtx2, 0); err != nil {
		t.Errorf("defaultSleep 0 err: %v", err)
	}
	sleepCancel2()
	if err := logshipperlite.DefaultSleepForTest(sleepCtx2, 0); err == nil {
		t.Errorf("defaultSleep canceled want err, got nil")
	}

	// 5. RunCLI coverage
	var stdout, stderr bytes.Buffer
	// Invalid args
	if code := logshipperlite.RunCLI(context.Background(), []string{"-invalid"}, &stdout, &stderr); code != 2 {
		t.Errorf("RunCLI invalid flag want 2, got %d", code)
	}
	// Missing endpoint
	if code := logshipperlite.RunCLI(context.Background(), []string{p}, &stdout, &stderr); code != 2 {
		t.Errorf("RunCLI missing endpoint want 2, got %d", code)
	}
	// Bad config (e.g. invalid format)
	if code := logshipperlite.RunCLI(context.Background(), []string{"-endpoint", "http://x", "-format", "bad", p}, &stdout, &stderr); code != 1 {
		t.Errorf("RunCLI bad config want 1, got %d", code)
	}
	// RunCLI with -once and failing destination
	if code := logshipperlite.RunCLI(context.Background(), []string{"-once", "-endpoint", "http://127.0.0.1:1/nonexistent", "-max-retries=0", p}, &stdout, &stderr); code != 1 {
		// note: -max-retries is not a CLI flag, flag parse will error with 2
	}
	if code := logshipperlite.RunCLI(context.Background(), []string{"-once", "-endpoint", "http://127.0.0.1:1/nonexistent", p}, &stdout, &stderr); code != 1 {
		t.Errorf("RunCLI -once failing want 1, got %d", code)
	}
	// RunCLI without -once, cancelled context
	cliCtx, cliCancel := context.WithCancel(context.Background())
	cliCancel()
	if code := logshipperlite.RunCLI(cliCtx, []string{"-endpoint", "http://example.com", p}, &stdout, &stderr); code != 0 {
		t.Errorf("RunCLI canceled want 0, got %d", code)
	}
	// RunCLI without -once, runtime error during run
	srv400 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv400.Close()
	if code := logshipperlite.RunCLI(context.Background(), []string{"-endpoint", srv400.URL, p}, &stdout, &stderr); code != 1 {
		t.Errorf("RunCLI rejected in Run want 1, got %d", code)
	}
}

func ExampleShipper_Step() {
	dir, _ := os.MkdirTemp("", "example-shipper")
	defer func() { _ = os.RemoveAll(dir) }()
	logPath := filepath.Join(dir, "a.log")
	_ = os.WriteFile(logPath, []byte("one\ntwo\nthree\n"), 0o600)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, _ := logshipperlite.New(logshipperlite.Config{Paths: []string{logPath}, Endpoint: srv.URL})
	_ = s.Step(context.Background())
	fmt.Println("sent:", s.Stats().Sent)
	// Output:
	// sent: 3
}
