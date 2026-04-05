package logscan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunFileMonitorTracksOffsetAcrossAppends(t *testing.T) {
	t.Parallel()

	logPath := filepath.Join(t.TempDir(), "auth.log")
	if err := os.WriteFile(logPath, []byte("line1\nline2\nline3\n"), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	original := presetsByID["auth.log"]
	presetsByID["auth.log"] = Preset{
		ID:              original.ID,
		Kind:            original.Kind,
		Path:            logPath,
		Identifier:      original.Identifier,
		DefaultBackfill: 2,
	}
	defer func() {
		presetsByID["auth.log"] = original
	}()

	first, err := Run(context.Background(), Request{
		Mode:           ModeMonitor,
		SourcePresetID: "auth.log",
		Limits: Limits{
			BackfillLines: 2,
			MaxLines:      10,
			MaxBytes:      4096,
		},
	})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if got := collectMessages(first.Entries); got != "line2|line3" {
		t.Fatalf("unexpected initial entries: %s", got)
	}

	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open append file: %v", err)
	}
	if _, err := file.WriteString("line4\nline5\n"); err != nil {
		file.Close()
		t.Fatalf("append log lines: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close append file: %v", err)
	}

	second, err := Run(context.Background(), Request{
		Mode:           ModeMonitor,
		SourcePresetID: "auth.log",
		Limits: Limits{
			BackfillLines: 2,
			MaxLines:      10,
			MaxBytes:      4096,
		},
		Cursor: &first.Cursor,
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := collectMessages(second.Entries); got != "line4|line5" {
		t.Fatalf("unexpected appended entries: %s", got)
	}
	if second.Cursor.FileOffset <= first.Cursor.FileOffset {
		t.Fatalf("cursor did not advance: first=%d second=%d", first.Cursor.FileOffset, second.Cursor.FileOffset)
	}
}

func TestRunFileMonitorDetectsRotationAndReplaysTail(t *testing.T) {
	t.Parallel()

	logPath := filepath.Join(t.TempDir(), "kern.log")
	if err := os.WriteFile(logPath, []byte("a1\na2\na3\n"), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	original := presetsByID["kern.log"]
	presetsByID["kern.log"] = Preset{
		ID:              original.ID,
		Kind:            original.Kind,
		Path:            logPath,
		Identifier:      original.Identifier,
		DefaultBackfill: 2,
	}
	defer func() {
		presetsByID["kern.log"] = original
	}()

	first, err := Run(context.Background(), Request{
		Mode:           ModeMonitor,
		SourcePresetID: "kern.log",
		Limits: Limits{
			BackfillLines: 2,
			MaxLines:      10,
			MaxBytes:      4096,
		},
	})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	if err := os.Rename(logPath, logPath+".1"); err != nil {
		t.Fatalf("rotate old file: %v", err)
	}
	if err := os.WriteFile(logPath, []byte("b1\nb2\nb3\n"), 0o600); err != nil {
		t.Fatalf("write rotated replacement: %v", err)
	}

	second, err := Run(context.Background(), Request{
		Mode:           ModeMonitor,
		SourcePresetID: "kern.log",
		Limits: Limits{
			BackfillLines: 2,
			MaxLines:      10,
			MaxBytes:      4096,
		},
		Cursor: &first.Cursor,
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := collectMessages(second.Entries); got != "b2|b3" {
		t.Fatalf("unexpected rotated tail entries: %s", got)
	}
	if second.Cursor.CursorResetReason != "rotated" {
		t.Fatalf("expected rotated cursor reset, got %q", second.Cursor.CursorResetReason)
	}
	if len(second.Warnings) == 0 || !strings.Contains(second.Warnings[0], "rotated") {
		t.Fatalf("expected rotated warning, got %v", second.Warnings)
	}
}

func TestRunFileMonitorDetectsTruncationAndReplaysTail(t *testing.T) {
	t.Parallel()

	logPath := filepath.Join(t.TempDir(), "syslog")
	if err := os.WriteFile(logPath, []byte("c1\nc2\nc3\n"), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	original := presetsByID["syslog"]
	presetsByID["syslog"] = Preset{
		ID:              original.ID,
		Kind:            original.Kind,
		Path:            logPath,
		Identifier:      original.Identifier,
		DefaultBackfill: 2,
	}
	defer func() {
		presetsByID["syslog"] = original
	}()

	first, err := Run(context.Background(), Request{
		Mode:           ModeMonitor,
		SourcePresetID: "syslog",
		Limits: Limits{
			BackfillLines: 2,
			MaxLines:      10,
			MaxBytes:      4096,
		},
	})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	if err := os.WriteFile(logPath, []byte("d1\nd2\n"), 0o600); err != nil {
		t.Fatalf("truncate log file: %v", err)
	}

	second, err := Run(context.Background(), Request{
		Mode:           ModeMonitor,
		SourcePresetID: "syslog",
		Limits: Limits{
			BackfillLines: 2,
			MaxLines:      10,
			MaxBytes:      4096,
		},
		Cursor: &Cursor{
			FileInode:  first.Cursor.FileInode,
			FileOffset: first.Cursor.FileOffset + 2048,
		},
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := collectMessages(second.Entries); got != "d1|d2" {
		t.Fatalf("unexpected truncated tail entries: %s", got)
	}
	if second.Cursor.CursorResetReason != "truncated" {
		t.Fatalf("expected truncated cursor reset, got %q", second.Cursor.CursorResetReason)
	}
	if len(second.Warnings) == 0 || !strings.Contains(second.Warnings[0], "truncated") {
		t.Fatalf("expected truncated warning, got %v", second.Warnings)
	}
}

func collectMessages(entries []Entry) string {
	values := make([]string, 0, len(entries))
	for _, entry := range entries {
		values = append(values, fmt.Sprintf("%s", entry.Message))
	}
	return strings.Join(values, "|")
}
