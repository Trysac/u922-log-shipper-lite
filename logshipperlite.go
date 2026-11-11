package logshipperlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultBatchMaxRecords is the default maximum number of records per batch.
	DefaultBatchMaxRecords = 500
	// DefaultBatchMaxBytes is the default maximum byte size of an NDJSON batch body (1 MiB).
	DefaultBatchMaxBytes = 1 << 20
	// DefaultMaxPendingBatches is the default maximum number of unsent batches held in memory.
	DefaultMaxPendingBatches = 4
	// DefaultMaxLineBytes is the default maximum line length before truncation (64 KiB).
	DefaultMaxLineBytes = 65_536
	// DefaultPollInterval is the default sleep duration when Step does no work in Run.
	DefaultPollInterval = time.Second
	// DefaultMaxRetries is the default number of retry attempts per Step before returning retries_exhausted.
	DefaultMaxRetries = 10
	// MaxCheckpointBytes is the maximum size of a checkpoint file (10 MiB).
	MaxCheckpointBytes = 10 << 20
	// maxErrorSnippetLen bounds untrusted text included in error messages.
	maxErrorSnippetLen = 120
)

// Error represents a typed error returned by logshipperlite.
// Code is one of: "rejected", "retries_exhausted", "bad_checkpoint", "bad_config", "io".
type Error struct {
	Code    string
	Status  int
	Message string
	Err     error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("logshipperlite: ")
	b.WriteString(e.Code)
	if e.Status != 0 {
		b.WriteString(fmt.Sprintf(" (status %d)", e.Status))
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(truncateText(e.Err.Error(), maxErrorSnippetLen))
	}
	return b.String()
}

// Unwrap returns the underlying cause of the error, if any.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func truncateText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}

// Event describes an operational milestone emitted via Config.OnEvent.
// It intentionally never contains log line contents or secrets.
type Event struct {
	Kind    string
	File    string
	Records int
	Status  int
}

// Stats reports the current operational counters and offsets of a Shipper.
type Stats struct {
	PendingBatches int
	ReadOffset     int64
	AckedOffset    int64
	Sent           int
	Retries        int
}

// Config configures a Shipper instance.
type Config struct {
	Paths             []string
	CheckpointPath    string
	Endpoint          string
	Client            *http.Client
	Format            string
	BatchMaxRecords   int
	BatchMaxBytes     int
	MaxPendingBatches int
	MaxLineBytes      int
	PollInterval      time.Duration
	MaxRetries        int
	Sleep             func(ctx context.Context, d time.Duration) error
	OnEvent           func(Event)
}

type checkpointData struct {
	Version int                   `json:"version"`
	Files   []checkpointFileEntry `json:"files"`
}

type checkpointFileEntry struct {
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
	Offset      int64  `json:"offset"`
}

type batchProgress struct {
	path        string
	fingerprint string
	endOffset   int64
	isRotated   bool
}

type batch struct {
	body           []byte
	firstKey       string
	lastKey        string
	idempotencyKey string
	records        int
	firstFile      string
	progress       []batchProgress
}

type fileStream struct {
	file        *os.File
	fingerprint string
	readOffset  int64
}

type trackedFile struct {
	cfgPath         string
	baseName        string
	ackedFp         string
	ackedOffset     int64
	draining        *fileStream
	active          *fileStream
	stagedLineBytes []byte
	stagedKey       string
	stagedFp        string
	stagedEndOffset int64
	stagedRotated   bool
}

// Shipper tails configured log files, batches records, ships them over HTTP,
// and persists checkpoints.
type Shipper struct {
	cfg      Config
	files    []*trackedFile
	pending  []*batch
	building *batch
	sent     int
	retries  int
	rrIndex  int
}

// New validates cfg, reads the existing checkpoint if present, and returns an
// initialized Shipper.
func New(cfg Config) (*Shipper, error) {
	if len(cfg.Paths) == 0 {
		return nil, &Error{Code: "bad_config", Message: "at least one path is required"}
	}
	seen := make(map[string]bool, len(cfg.Paths))
	for _, p := range cfg.Paths {
		if strings.TrimSpace(p) == "" {
			return nil, &Error{Code: "bad_config", Message: "path must not be empty"}
		}
		if seen[p] {
			return nil, &Error{Code: "bad_config", Message: "duplicate path configured"}
		}
		seen[p] = true
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, &Error{Code: "bad_config", Message: "endpoint is required"}
	}
	if cfg.Format == "" {
		cfg.Format = "raw"
	}
	if cfg.Format != "raw" && cfg.Format != "json" {
		return nil, &Error{Code: "bad_config", Message: "format must be 'raw' or 'json'"}
	}
	if cfg.BatchMaxRecords < 0 || cfg.BatchMaxBytes < 0 || cfg.MaxPendingBatches < 0 || cfg.MaxLineBytes < 0 || cfg.MaxRetries < 0 || cfg.PollInterval < 0 {
		return nil, &Error{Code: "bad_config", Message: "numeric limits must not be negative"}
	}
	if cfg.BatchMaxRecords == 0 {
		cfg.BatchMaxRecords = DefaultBatchMaxRecords
	}
	if cfg.BatchMaxBytes == 0 {
		cfg.BatchMaxBytes = DefaultBatchMaxBytes
	}
	if cfg.MaxPendingBatches == 0 {
		cfg.MaxPendingBatches = DefaultMaxPendingBatches
	}
	if cfg.MaxLineBytes == 0 {
		cfg.MaxLineBytes = DefaultMaxLineBytes
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = DefaultMaxRetries
	}
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	if cfg.Sleep == nil {
		cfg.Sleep = defaultSleep
	}

	ackedMap := make(map[string]checkpointFileEntry)
	if cfg.CheckpointPath != "" {
		entries, err := loadCheckpoint(cfg.CheckpointPath)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			ackedMap[e.Path] = e
		}
	}

	files := make([]*trackedFile, 0, len(cfg.Paths))
	for _, p := range cfg.Paths {
		tf := &trackedFile{
			cfgPath:  p,
			baseName: filepath.Base(p),
		}
		if entry, ok := ackedMap[p]; ok {
			tf.ackedFp = entry.Fingerprint
			tf.ackedOffset = entry.Offset
		}
		files = append(files, tf)
	}

	return &Shipper{
		cfg:   cfg,
		files: files,
	}, nil
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func loadCheckpoint(path string) ([]checkpointFileEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, &Error{Code: "io", Message: "open checkpoint", Err: err}
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, &Error{Code: "io", Message: "stat checkpoint", Err: err}
	}
	if info.Size() > MaxCheckpointBytes {
		return nil, &Error{Code: "bad_checkpoint", Message: "checkpoint file exceeds size limit"}
	}

	raw, err := io.ReadAll(io.LimitReader(f, MaxCheckpointBytes+1))
	if err != nil {
		return nil, &Error{Code: "io", Message: "read checkpoint", Err: err}
	}
	if len(raw) == 0 {
		return nil, &Error{Code: "bad_checkpoint", Message: "checkpoint file is empty"}
	}

	var cp checkpointData
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cp); err != nil {
		return nil, &Error{Code: "bad_checkpoint", Message: "malformed checkpoint JSON", Err: err}
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, &Error{Code: "bad_checkpoint", Message: "trailing data in checkpoint JSON"}
	}
	if cp.Version != 1 {
		return nil, &Error{Code: "bad_checkpoint", Message: fmt.Sprintf("unsupported checkpoint version %d", cp.Version)}
	}
	for _, entry := range cp.Files {
		if entry.Path == "" || entry.Offset < 0 {
			return nil, &Error{Code: "bad_checkpoint", Message: "invalid file entry in checkpoint"}
		}
	}
	return cp.Files, nil
}

func (s *Shipper) saveCheckpoint() error {
	if s.cfg.CheckpointPath == "" {
		return nil
	}
	cp := checkpointData{
		Version: 1,
		Files:   make([]checkpointFileEntry, 0, len(s.files)),
	}
	for _, tf := range s.files {
		cp.Files = append(cp.Files, checkpointFileEntry{
			Path:        tf.cfgPath,
			Fingerprint: tf.ackedFp,
			Offset:      tf.ackedOffset,
		})
	}
	data, err := json.Marshal(cp)
	if err != nil {
		return &Error{Code: "io", Message: "marshal checkpoint", Err: err}
	}

	dir := filepath.Dir(s.cfg.CheckpointPath)
	tmp, err := os.CreateTemp(dir, ".checkpoint-*.tmp")
	if err != nil {
		return &Error{Code: "io", Message: "create temp checkpoint", Err: err}
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return &Error{Code: "io", Message: "write temp checkpoint", Err: err}
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return &Error{Code: "io", Message: "fsync temp checkpoint", Err: err}
	}
	if err := tmp.Close(); err != nil {
		return &Error{Code: "io", Message: "close temp checkpoint", Err: err}
	}
	if err := os.Rename(tmpName, s.cfg.CheckpointPath); err != nil {
		return &Error{Code: "io", Message: "rename checkpoint", Err: err}
	}
	cleanup = false
	return nil
}

// Close closes any open file handles held by the Shipper.
func (s *Shipper) Close() error {
	var firstErr error
	for _, tf := range s.files {
		if tf.draining != nil && tf.draining.file != nil {
			if err := tf.draining.file.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
			tf.draining.file = nil
		}
		if tf.active != nil && tf.active.file != nil {
			if err := tf.active.file.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
			tf.active.file = nil
		}
	}
	return firstErr
}

// Stats returns a snapshot of the Shipper's current queue size, offsets, and counters.
func (s *Shipper) Stats() Stats {
	pendingCount := len(s.pending)
	if s.building != nil && s.building.records > 0 {
		pendingCount++
	}
	var readOffset, ackedOffset int64
	for _, tf := range s.files {
		ackedOffset += tf.ackedOffset
		if tf.active != nil {
			readOffset += tf.active.readOffset
		} else if tf.draining != nil {
			readOffset += tf.draining.readOffset
		} else {
			readOffset += tf.ackedOffset
		}
	}
	return Stats{
		PendingBatches: pendingCount,
		ReadOffset:     readOffset,
		AckedOffset:    ackedOffset,
		Sent:           s.sent,
		Retries:        s.retries,
	}
}

// Step executes one synchronous cycle: it reads available complete log lines
// into batches while the pending queue has room (< MaxPendingBatches), and then
// attempts to send the oldest pending batch once (with up to MaxRetries retries
// on transient failure).
func (s *Shipper) Step(ctx context.Context) error {
	_, err := s.stepInternal(ctx)
	return err
}

// Run loops Step until ctx is canceled. When a Step reads no new records and
// has no pending batches to send, Run sleeps for PollInterval using Config.Sleep.
func (s *Shipper) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		didWork, err := s.stepInternal(ctx)
		if err != nil {
			return err
		}
		if !didWork {
			if err := s.cfg.Sleep(ctx, s.cfg.PollInterval); err != nil {
				return err
			}
		}
	}
}

func (s *Shipper) stepInternal(ctx context.Context) (bool, error) {
	readAny, err := s.fillPendingBatches()
	if err != nil {
		return readAny, err
	}
	if len(s.pending) == 0 {
		return readAny, nil
	}
	err = s.sendOldestBatch(ctx)
	return true, err
}

func (s *Shipper) emit(ev Event) {
	if s.cfg.OnEvent != nil {
		s.cfg.OnEvent(ev)
	}
}

func (s *Shipper) fillPendingBatches() (bool, error) {
	readAny := false
	for len(s.pending) < s.cfg.MaxPendingBatches {
		madeProgress := false
		for i := 0; i < len(s.files); i++ {
			if len(s.pending) >= s.cfg.MaxPendingBatches {
				break
			}
			idx := (s.rrIndex + i) % len(s.files)
			tf := s.files[idx]
			added, err := s.readFromTrackedFile(tf)
			if err != nil {
				return readAny, err
			}
			if added {
				madeProgress = true
				readAny = true
			}
		}
		if !madeProgress {
			break
		}
	}

	if len(s.pending) < s.cfg.MaxPendingBatches && s.building != nil && s.building.records > 0 {
		s.sealBuildingBatch()
	}
	return readAny, nil
}

func (s *Shipper) sealBuildingBatch() {
	if s.building == nil || s.building.records == 0 {
		return
	}
	sum := sha256.Sum256([]byte(s.building.firstKey + "|" + s.building.lastKey))
	s.building.idempotencyKey = hex.EncodeToString(sum[:])
	s.pending = append(s.pending, s.building)
	s.building = nil
}

func (s *Shipper) readFromTrackedFile(tf *trackedFile) (bool, error) {
	addedAny := false
	for len(s.pending) < s.cfg.MaxPendingBatches {
		if tf.stagedLineBytes == nil {
			hasLine, err := s.stageNextLine(tf)
			if err != nil {
				return addedAny, err
			}
			if !hasLine {
				return addedAny, nil
			}
		}

		recLen := len(tf.stagedLineBytes)
		if s.building != nil && s.building.records > 0 {
			if s.building.records+1 > s.cfg.BatchMaxRecords || len(s.building.body)+recLen > s.cfg.BatchMaxBytes {
				s.sealBuildingBatch()
				if len(s.pending) >= s.cfg.MaxPendingBatches {
					return addedAny, nil
				}
			}
		}

		if s.building == nil {
			s.building = &batch{
				firstKey:  tf.stagedKey,
				firstFile: tf.baseName,
			}
		}
		s.building.body = append(s.building.body, tf.stagedLineBytes...)
		s.building.lastKey = tf.stagedKey
		s.building.records++
		updateBatchProgress(s.building, batchProgress{
			path:        tf.cfgPath,
			fingerprint: tf.stagedFp,
			endOffset:   tf.stagedEndOffset,
			isRotated:   tf.stagedRotated,
		})

		tf.stagedLineBytes = nil
		tf.stagedKey = ""
		tf.stagedFp = ""
		tf.stagedEndOffset = 0
		tf.stagedRotated = false
		addedAny = true

		if s.building.records >= s.cfg.BatchMaxRecords || len(s.building.body) >= s.cfg.BatchMaxBytes {
			s.sealBuildingBatch()
		}
	}
	return addedAny, nil
}

func updateBatchProgress(b *batch, p batchProgress) {
	for i := range b.progress {
		if b.progress[i].path == p.path && b.progress[i].fingerprint == p.fingerprint && b.progress[i].isRotated == p.isRotated {
			b.progress[i].endOffset = p.endOffset
			return
		}
	}
	b.progress = append(b.progress, p)
}

func (s *Shipper) stageNextLine(tf *trackedFile) (bool, error) {
	for {
		if tf.draining != nil {
			recBytes, key, endOffset, ok, err := s.readRecordFromStream(tf.baseName, tf.draining)
			if err != nil {
				return false, err
			}
			if ok {
				tf.stagedLineBytes = recBytes
				tf.stagedKey = key
				tf.stagedFp = tf.draining.fingerprint
				tf.stagedEndOffset = endOffset
				tf.stagedRotated = true
				return true, nil
			}
			_ = tf.draining.file.Close()
			tf.draining = nil
		}

		if err := s.syncActiveStream(tf); err != nil {
			return false, err
		}
		if tf.draining != nil {
			continue
		}
		if tf.active == nil {
			return false, nil
		}

		recBytes, key, endOffset, ok, err := s.readRecordFromStream(tf.baseName, tf.active)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
		tf.stagedLineBytes = recBytes
		tf.stagedKey = key
		tf.stagedFp = tf.active.fingerprint
		tf.stagedEndOffset = endOffset
		tf.stagedRotated = false
		return true, nil
	}
}

func (s *Shipper) syncActiveStream(tf *trackedFile) error {
	f, err := openLogFile(tf.cfgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if tf.active != nil {
				tf.draining = tf.active
				tf.active = nil
				s.emit(Event{Kind: "rotate", File: tf.baseName})
			}
			return nil
		}
		return &Error{Code: "io", Message: "open log file", Err: err}
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return &Error{Code: "io", Message: "stat log file", Err: err}
	}
	fp, hasFirstLine, err := readFirstLineFingerprint(f)
	if err != nil {
		_ = f.Close()
		return err
	}

	if tf.active == nil {
		if !hasFirstLine {
			_ = f.Close()
			return nil
		}
		var startOffset int64
		if tf.ackedFp != "" && tf.ackedFp == fp && info.Size() >= tf.ackedOffset {
			startOffset = tf.ackedOffset
		}
		tf.active = &fileStream{
			file:        f,
			fingerprint: fp,
			readOffset:  startOffset,
		}
		return nil
	}

	activeInfo, err := tf.active.file.Stat()
	if err != nil {
		_ = f.Close()
		return &Error{Code: "io", Message: "stat active log file", Err: err}
	}

	sameFile := os.SameFile(info, activeInfo)
	if !sameFile {
		// Path now points to a replaced file: drain the previous open handle to EOF first.
		tf.draining = tf.active
		tf.active = nil
		s.emit(Event{Kind: "rotate", File: tf.baseName})
		if hasFirstLine {
			tf.active = &fileStream{
				file:        f,
				fingerprint: fp,
				readOffset:  0,
			}
		} else {
			_ = f.Close()
		}
		return nil
	}

	// Same underlying file: check for copy-truncate (size shrunk or first-line fingerprint changed).
	if info.Size() < tf.active.readOffset || (hasFirstLine && fp != tf.active.fingerprint) {
		_ = tf.active.file.Close()
		tf.active = nil
		s.emit(Event{Kind: "rotate", File: tf.baseName})
		if hasFirstLine {
			tf.active = &fileStream{
				file:        f,
				fingerprint: fp,
				readOffset:  0,
			}
		} else {
			_ = f.Close()
		}
		return nil
	}

	_ = f.Close()
	return nil
}

func readFirstLineFingerprint(f *os.File) (string, bool, error) {
	h := sha256.New()
	buf := make([]byte, 4096)
	var offset int64
	for {
		n, err := f.ReadAt(buf, offset)
		if n > 0 {
			if idx := bytes.IndexByte(buf[:n], '\n'); idx >= 0 {
				h.Write(buf[:idx+1])
				sum := h.Sum(nil)
				return hex.EncodeToString(sum)[:16], true, nil
			}
			h.Write(buf[:n])
			offset += int64(n)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", false, nil
			}
			return "", false, &Error{Code: "io", Message: "read first line", Err: err}
		}
	}
}

func (s *Shipper) readRecordFromStream(baseName string, stream *fileStream) ([]byte, string, int64, bool, error) {
	startOffset := stream.readOffset
	curOffset := startOffset
	maxBytes := s.cfg.MaxLineBytes
	var lineBuf []byte
	truncated := false
	var buf [4096]byte

	for {
		n, err := stream.file.ReadAt(buf[:], curOffset)
		if n > 0 {
			chunk := buf[:n]
			if nlIdx := bytes.IndexByte(chunk, '\n'); nlIdx >= 0 {
				beforeNL := chunk[:nlIdx]
				if !truncated {
					rem := maxBytes - len(lineBuf)
					if len(beforeNL) > rem {
						lineBuf = append(lineBuf, beforeNL[:rem]...)
						truncated = true
					} else {
						lineBuf = append(lineBuf, beforeNL...)
					}
				}
				endOffset := curOffset + int64(nlIdx) + 1
				stream.readOffset = endOffset
				key := fmt.Sprintf("%s:%d", stream.fingerprint, startOffset)
				recBytes, encErr := encodeRecord(baseName, key, lineBuf, truncated, s.cfg.Format)
				if encErr != nil {
					return nil, "", 0, false, encErr
				}
				return recBytes, key, endOffset, true, nil
			}

			if !truncated {
				rem := maxBytes - len(lineBuf)
				if len(chunk) > rem {
					lineBuf = append(lineBuf, chunk[:rem]...)
					truncated = true
				} else {
					lineBuf = append(lineBuf, chunk...)
				}
			}
			curOffset += int64(n)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, "", 0, false, nil
			}
			return nil, "", 0, false, &Error{Code: "io", Message: "read log line", Err: err}
		}
	}
}

func encodeRecord(file, key string, rawLine []byte, truncated bool, format string) ([]byte, error) {
	validLine := strings.ToValidUTF8(string(rawLine), "\uFFFD")

	var buf bytes.Buffer
	buf.WriteString(`{"file":`)
	appendJSONString(&buf, file)
	buf.WriteString(`,"key":`)
	appendJSONString(&buf, key)
	buf.WriteString(`,"line":`)
	appendJSONString(&buf, validLine)

	if format == "json" {
		var parsed map[string]any
		dec := json.NewDecoder(strings.NewReader(validLine))
		dec.UseNumber()
		err := dec.Decode(&parsed)
		var trailing any
		if err == nil && parsed != nil && dec.Decode(&trailing) == io.EOF {
			rawObj, mErr := json.Marshal(parsed)
			if mErr != nil {
				return nil, &Error{Code: "io", Message: "marshal parsed json", Err: mErr}
			}
			buf.WriteString(`,"json":`)
			buf.Write(rawObj)
		} else {
			buf.WriteString(`,"parse_error":true`)
		}
	}

	if truncated {
		buf.WriteString(`,"truncated":true`)
	}
	buf.WriteString("}\n")
	return buf.Bytes(), nil
}

func appendJSONString(buf *bytes.Buffer, s string) {
	var tmp bytes.Buffer
	enc := json.NewEncoder(&tmp)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	b := tmp.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	buf.Write(b)
}

func (s *Shipper) sendOldestBatch(ctx context.Context) error {
	b := s.pending[0]
	attempt := 0

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Endpoint, bytes.NewReader(b.body))
		if err != nil {
			return &Error{Code: "bad_config", Message: "create HTTP request", Err: err}
		}
		req.Header.Set("Content-Type", "application/x-ndjson")
		req.Header.Set("Idempotency-Key", b.idempotencyKey)

		resp, err := s.cfg.Client.Do(req)
		var status int
		var retryAfterHeader string
		if err == nil {
			status = resp.StatusCode
			retryAfterHeader = resp.Header.Get("Retry-After")
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
		}

		if err == nil && status >= 200 && status < 300 {
			s.pending = s.pending[1:]
			s.sent += b.records
			for _, p := range b.progress {
				if p.isRotated {
					continue
				}
				for _, tf := range s.files {
					if tf.cfgPath == p.path {
						tf.ackedFp = p.fingerprint
						tf.ackedOffset = p.endOffset
					}
				}
			}
			if cpErr := s.saveCheckpoint(); cpErr != nil {
				return cpErr
			}
			s.emit(Event{Kind: "sent", File: b.firstFile, Records: b.records, Status: status})
			return nil
		}

		if err == nil && status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests {
			s.emit(Event{Kind: "rejected", File: b.firstFile, Records: b.records, Status: status})
			return &Error{Code: "rejected", Status: status, Message: fmt.Sprintf("destination rejected batch with HTTP %d", status)}
		}

		// Transient error: transport failure, 5xx, 408, 429, or unexpected status.
		if attempt >= s.cfg.MaxRetries {
			s.emit(Event{Kind: "retries_exhausted", File: b.firstFile, Records: b.records, Status: status})
			return &Error{Code: "retries_exhausted", Status: status, Message: "maximum retries exhausted", Err: err}
		}

		attempt++
		s.retries++
		s.emit(Event{Kind: "retry", File: b.firstFile, Records: b.records, Status: status})
		delay := computeRetryDelay(attempt, retryAfterHeader)
		if sleepErr := s.cfg.Sleep(ctx, delay); sleepErr != nil {
			return sleepErr
		}
	}
}

func computeRetryDelay(attempt int, retryAfter string) time.Duration {
	const maxDelay = 30 * time.Second
	if retryAfter != "" {
		if d, ok := parseRetryAfter(retryAfter); ok {
			if d < 0 {
				return 0
			}
			if d > maxDelay {
				return maxDelay
			}
			return d
		}
	}

	shift := attempt - 1
	if shift > 10 {
		shift = 10
	}
	d := (100 * time.Millisecond) * time.Duration(1<<shift)
	if d > maxDelay {
		return maxDelay
	}
	return d
}

func parseRetryAfter(val string) (time.Duration, bool) {
	val = strings.TrimSpace(val)
	if secs, err := strconv.ParseInt(val, 10, 64); err == nil {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(val); err == nil {
		return time.Until(t), true
	}
	return 0, false
}

// RunCLI executes the command-line interface for log-shipper-lite.
// It returns 0 on clean exit, 1 on runtime/rejected error, and 2 on CLI usage error.
func RunCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("log-shipper-lite", flag.ContinueOnError)
	fs.SetOutput(stderr)
	endpoint := fs.String("endpoint", "", "HTTP destination endpoint URL")
	checkpoint := fs.String("checkpoint", "", "path to checkpoint JSON file")
	format := fs.String("format", "raw", "record format: raw or json")
	once := fs.Bool("once", false, "run a single Step and exit")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	paths := fs.Args()
	if *endpoint == "" || len(paths) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: log-shipper-lite -endpoint URL [-checkpoint PATH] [-format raw|json] FILE...")
		return 2
	}

	shipper, err := New(Config{
		Paths:          paths,
		CheckpointPath: *checkpoint,
		Endpoint:       *endpoint,
		Format:         *format,
	})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err.Error())
		return 1
	}
	defer func() { _ = shipper.Close() }()

	if *once {
		if err := shipper.Step(ctx); err != nil {
			_, _ = fmt.Fprintln(stderr, err.Error())
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "sent=%d\n", shipper.Stats().Sent)
		return 0
	}

	err = shipper.Run(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintln(stderr, err.Error())
		return 1
	}
	return 0
}
