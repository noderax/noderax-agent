package logscan

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	DefaultMaxLines      = 500
	DefaultMaxBytes      = 65536
	DefaultBackfillLines = 200
	HardMaxLines         = 2000
	HardMaxBytes         = 262144
	HardBackfillLines    = 500
)

type Mode string

const (
	ModePreview Mode = "preview"
	ModeMonitor Mode = "monitor"
)

type SourceType string

const (
	SourceTypeFile    SourceType = "file"
	SourceTypeJournal SourceType = "journal"
)

type Request struct {
	Mode            Mode           `json:"mode"`
	SourcePresetID  string         `json:"sourcePresetId"`
	Limits          Limits         `json:"limits,omitempty"`
	Cursor          *Cursor        `json:"cursor,omitempty"`
	RunAsRoot       bool           `json:"runAsRoot,omitempty"`
	RootScope       string         `json:"rootScope,omitempty"`
	InternalContext map[string]any `json:"internalContext,omitempty"`
}

type Limits struct {
	MaxLines      int `json:"maxLines,omitempty"`
	MaxBytes      int `json:"maxBytes,omitempty"`
	BackfillLines int `json:"backfillLines,omitempty"`
}

type Cursor struct {
	JournalCursor     string `json:"journalCursor,omitempty"`
	FileInode         string `json:"fileInode,omitempty"`
	FileOffset        int64  `json:"fileOffset,omitempty"`
	LastReadAt        string `json:"lastReadAt,omitempty"`
	CursorResetReason string `json:"cursorResetReason,omitempty"`
}

type Entry struct {
	Timestamp  string `json:"timestamp,omitempty"`
	Message    string `json:"message"`
	Unit       string `json:"unit,omitempty"`
	Identifier string `json:"identifier,omitempty"`
}

type Result struct {
	SourcePresetID string     `json:"sourcePresetId"`
	SourceType     SourceType `json:"sourceType"`
	Entries        []Entry    `json:"entries"`
	Cursor         Cursor     `json:"cursor"`
	Truncated      bool       `json:"truncated"`
	BytesRead      int        `json:"bytesRead"`
	LinesRead      int        `json:"linesRead"`
	Warnings       []string   `json:"warnings"`
}

type Preset struct {
	ID              string
	Kind            SourceType
	Path            string
	Unit            string
	Identifier      string
	DefaultBackfill int
}

type normalizedLimits struct {
	maxLines      int
	maxBytes      int
	backfillLines int
}

var presetsByID = map[string]Preset{
	"syslog": {
		ID:              "syslog",
		Kind:            SourceTypeFile,
		Path:            "/var/log/syslog",
		Identifier:      "/var/log/syslog",
		DefaultBackfill: 200,
	},
	"auth.log": {
		ID:              "auth.log",
		Kind:            SourceTypeFile,
		Path:            "/var/log/auth.log",
		Identifier:      "/var/log/auth.log",
		DefaultBackfill: 200,
	},
	"kern.log": {
		ID:              "kern.log",
		Kind:            SourceTypeFile,
		Path:            "/var/log/kern.log",
		Identifier:      "/var/log/kern.log",
		DefaultBackfill: 200,
	},
	"noderax-agent": {
		ID:              "noderax-agent",
		Kind:            SourceTypeJournal,
		Unit:            "noderax-agent.service",
		Identifier:      "noderax-agent.service",
		DefaultBackfill: 200,
	},
}

func LoadRequest(path string) (Request, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return Request{}, fmt.Errorf("open log scan request: %w", err)
	}
	defer file.Close()

	var request Request
	if err := json.NewDecoder(file).Decode(&request); err != nil {
		return Request{}, fmt.Errorf("decode log scan request: %w", err)
	}

	return request, nil
}

func Run(ctx context.Context, request Request) (Result, error) {
	preset, ok := presetsByID[strings.TrimSpace(request.SourcePresetID)]
	if !ok {
		return Result{}, fmt.Errorf("unsupported log source preset %q", request.SourcePresetID)
	}

	mode := request.Mode
	if mode == "" {
		mode = ModePreview
	}
	if mode != ModePreview && mode != ModeMonitor {
		return Result{}, fmt.Errorf("unsupported log scan mode %q", request.Mode)
	}

	limits := normalizeLimits(request.Limits, preset.DefaultBackfill)
	switch preset.Kind {
	case SourceTypeFile:
		return scanFile(ctx, preset, mode, limits, request.Cursor)
	case SourceTypeJournal:
		return scanJournal(ctx, preset, mode, limits, request.Cursor)
	default:
		return Result{}, fmt.Errorf("unsupported source type %q", preset.Kind)
	}
}

func normalizeLimits(input Limits, defaultBackfill int) normalizedLimits {
	maxLines := clamp(input.MaxLines, 1, HardMaxLines, DefaultMaxLines)
	maxBytes := clamp(input.MaxBytes, 1, HardMaxBytes, DefaultMaxBytes)
	backfillDefault := defaultBackfill
	if backfillDefault <= 0 {
		backfillDefault = DefaultBackfillLines
	}
	backfillLines := clamp(input.BackfillLines, 1, HardBackfillLines, backfillDefault)

	return normalizedLimits{
		maxLines:      maxLines,
		maxBytes:      maxBytes,
		backfillLines: backfillLines,
	}
}

func clamp(value, minValue, maxValue, defaultValue int) int {
	if value == 0 {
		value = defaultValue
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func scanFile(
	ctx context.Context,
	preset Preset,
	mode Mode,
	limits normalizedLimits,
	cursor *Cursor,
) (Result, error) {
	stat, err := os.Stat(preset.Path)
	if err != nil {
		return Result{}, fmt.Errorf("stat %s: %w", preset.Path, err)
	}

	inode := fileInode(stat)
	size := stat.Size()
	now := time.Now().UTC().Format(time.RFC3339)
	result := Result{
		SourcePresetID: preset.ID,
		SourceType:     SourceTypeFile,
		Entries:        []Entry{},
		Cursor: Cursor{
			FileInode:  inode,
			FileOffset: size,
			LastReadAt: now,
		},
		Warnings: []string{},
	}

	if mode == ModePreview {
		entries, bytesRead, truncated, err := tailFileEntries(
			ctx,
			preset.Path,
			limits.backfillLines,
			limits.maxLines,
			limits.maxBytes,
			preset.Identifier,
		)
		if err != nil {
			return Result{}, err
		}
		result.Entries = entries
		result.BytesRead = bytesRead
		result.LinesRead = len(entries)
		result.Truncated = truncated
		return result, nil
	}

	if cursor == nil || strings.TrimSpace(cursor.FileInode) == "" {
		entries, bytesRead, truncated, err := tailFileEntries(
			ctx,
			preset.Path,
			limits.backfillLines,
			limits.maxLines,
			limits.maxBytes,
			preset.Identifier,
		)
		if err != nil {
			return Result{}, err
		}
		result.Entries = entries
		result.BytesRead = bytesRead
		result.LinesRead = len(entries)
		result.Truncated = truncated
		return result, nil
	}

	resetReason := ""
	if cursor.FileInode != inode {
		resetReason = "rotated"
	} else if cursor.FileOffset > size {
		resetReason = "truncated"
	}

	if resetReason != "" {
		entries, bytesRead, truncated, err := tailFileEntries(
			ctx,
			preset.Path,
			limits.backfillLines,
			limits.maxLines,
			limits.maxBytes,
			preset.Identifier,
		)
		if err != nil {
			return Result{}, err
		}
		result.Entries = entries
		result.BytesRead = bytesRead
		result.LinesRead = len(entries)
		result.Truncated = truncated
		result.Cursor.CursorResetReason = resetReason
		result.Warnings = append(result.Warnings, fmt.Sprintf("file cursor reset after log file %s", resetReason))
		return result, nil
	}

	entries, bytesRead, truncated, nextOffset, err := readFileFromOffset(
		ctx,
		preset.Path,
		cursor.FileOffset,
		limits.maxLines,
		limits.maxBytes,
		preset.Identifier,
	)
	if err != nil {
		return Result{}, err
	}

	result.Entries = entries
	result.BytesRead = bytesRead
	result.LinesRead = len(entries)
	result.Truncated = truncated
	result.Cursor.FileOffset = nextOffset
	return result, nil
}

func tailFileEntries(
	ctx context.Context,
	path string,
	backfillLines int,
	maxLines int,
	maxBytes int,
	identifier string,
) ([]Entry, int, bool, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, 0, false, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	type lineInfo struct {
		text string
		size int
	}

	lines := make([]lineInfo, 0, backfillLines)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil, 0, false, ctx.Err()
		default:
		}

		line := scanner.Text()
		size := len(scanner.Bytes()) + 1
		if len(lines) == backfillLines {
			copy(lines, lines[1:])
			lines[len(lines)-1] = lineInfo{text: line, size: size}
			continue
		}
		lines = append(lines, lineInfo{text: line, size: size})
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, false, fmt.Errorf("scan %s: %w", path, err)
	}

	start := 0
	if len(lines) > maxLines {
		start = len(lines) - maxLines
	}
	selected := lines[start:]

	for len(selected) > 0 {
		totalBytes := 0
		for _, line := range selected {
			totalBytes += line.size
		}
		if totalBytes <= maxBytes {
			entries := make([]Entry, 0, len(selected))
			for _, line := range selected {
				entries = append(entries, Entry{
					Message:    line.text,
					Identifier: identifier,
				})
			}
			truncated := len(selected) < len(lines)
			return entries, totalBytes, truncated, nil
		}
		selected = selected[1:]
	}

	return []Entry{}, 0, len(lines) > 0, nil
}

func readFileFromOffset(
	ctx context.Context,
	path string,
	offset int64,
	maxLines int,
	maxBytes int,
	identifier string,
) ([]Entry, int, bool, int64, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, 0, false, offset, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, false, offset, fmt.Errorf("seek %s: %w", path, err)
	}

	reader := bufio.NewReader(file)
	entries := make([]Entry, 0, minInt(maxLines, 64))
	currentOffset := offset
	bytesRead := 0
	truncated := false

	for len(entries) < maxLines {
		select {
		case <-ctx.Done():
			return nil, 0, false, currentOffset, ctx.Err()
		default:
		}

		startOffset := currentOffset
		chunk, err := reader.ReadBytes('\n')
		if len(chunk) > 0 {
			lineSize := len(chunk)
			if bytesRead+lineSize > maxBytes {
				truncated = true
				break
			}

			currentOffset = startOffset + int64(lineSize)
			bytesRead += lineSize
			line := strings.TrimRight(string(chunk), "\r\n")
			entries = append(entries, Entry{
				Message:    line,
				Identifier: identifier,
			})
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, false, currentOffset, fmt.Errorf("read %s: %w", path, err)
		}
	}

	if len(entries) == maxLines {
		if _, err := reader.Peek(1); err == nil {
			truncated = true
		}
	}

	return entries, bytesRead, truncated, currentOffset, nil
}

func scanJournal(
	ctx context.Context,
	preset Preset,
	mode Mode,
	limits normalizedLimits,
	cursor *Cursor,
) (Result, error) {
	journalctlPath, err := exec.LookPath("journalctl")
	if err != nil {
		return Result{}, fmt.Errorf("journalctl is required for journal log scanning: %w", err)
	}

	args := []string{
		"--no-pager",
		"--output=json",
		"--utc",
		"--unit",
		preset.Unit,
	}

	if mode == ModeMonitor && cursor != nil && strings.TrimSpace(cursor.JournalCursor) != "" {
		args = append(args, "--after-cursor", cursor.JournalCursor)
	} else {
		args = append(args, "--lines", strconv.Itoa(limits.backfillLines))
	}

	cmd := exec.CommandContext(ctx, journalctlPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("prepare journalctl stdout: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start journalctl: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	entries := make([]Entry, 0, minInt(limits.maxLines, 64))
	warnings := make([]string, 0, 1)
	lastCursor := ""
	bytesRead := 0
	truncated := false

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return Result{}, ctx.Err()
		default:
		}

		rawLine := scanner.Bytes()
		lineSize := len(rawLine) + 1
		if len(entries) >= limits.maxLines || bytesRead+lineSize > limits.maxBytes {
			truncated = true
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			break
		}

		entry, journalCursor, err := parseJournalEntry(rawLine, preset)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}

		entries = append(entries, entry)
		lastCursor = journalCursor
		bytesRead += lineSize
	}

	if err := scanner.Err(); err != nil {
		return Result{}, fmt.Errorf("scan journalctl output: %w", err)
	}

	waitErr := cmd.Wait()
	if truncated && waitErr != nil {
		waitErr = nil
	}
	if waitErr != nil {
		stderrText := strings.TrimSpace(stderr.String())
		if stderrText != "" {
			return Result{}, fmt.Errorf("journalctl failed: %s", stderrText)
		}
		return Result{}, fmt.Errorf("journalctl failed: %w", waitErr)
	}

	return Result{
		SourcePresetID: preset.ID,
		SourceType:     SourceTypeJournal,
		Entries:        entries,
		Cursor: Cursor{
			JournalCursor: lastCursor,
			LastReadAt:    time.Now().UTC().Format(time.RFC3339),
		},
		Truncated: truncated,
		BytesRead: bytesRead,
		LinesRead: len(entries),
		Warnings:  warnings,
	}, nil
}

func parseJournalEntry(raw []byte, preset Preset) (Entry, string, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Entry{}, "", fmt.Errorf("journal entry parse failed: %w", err)
	}

	message, _ := payload["MESSAGE"].(string)
	if strings.TrimSpace(message) == "" {
		return Entry{}, "", fmt.Errorf("journal entry missing MESSAGE")
	}

	unit, _ := payload["_SYSTEMD_UNIT"].(string)
	if unit == "" {
		unit = preset.Unit
	}
	identifier, _ := payload["SYSLOG_IDENTIFIER"].(string)
	if identifier == "" {
		identifier = preset.Identifier
	}
	timestamp := parseJournalTimestamp(payload["__REALTIME_TIMESTAMP"])
	cursor, _ := payload["__CURSOR"].(string)

	return Entry{
		Timestamp:  timestamp,
		Message:    message,
		Unit:       unit,
		Identifier: identifier,
	}, cursor, nil
}

func parseJournalTimestamp(value any) string {
	raw, _ := value.(string)
	if raw == "" {
		return ""
	}

	micros, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return ""
	}

	return time.UnixMicro(micros).UTC().Format(time.RFC3339Nano)
}

func fileInode(info os.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}

	return strconv.FormatUint(stat.Ino, 10)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
