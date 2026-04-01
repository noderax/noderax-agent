package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseChangelogRelease(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	changelogPath := filepath.Join(tempDir, "CHANGELOG.md")
	content := `# Changelog

## [Unreleased]

### Added
- Future work

## [1.2.0] - 2026-04-01

### Added
- Fleet rollout orchestration from the Updates center.
- Detached agent updater with checksum verification
  and heartbeat confirmation before rollout completion.

### Fixed
- Official release metadata is now sourced from the changelog.

## [1.1.0] - 2026-03-01

### Added
- Earlier release.
`
	if err := os.WriteFile(changelogPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write changelog: %v", err)
	}

	sections, err := parseChangelogRelease(changelogPath, "1.2.0")
	if err != nil {
		t.Fatalf("parse changelog: %v", err)
	}

	if len(sections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(sections))
	}
	if sections[0].Title != "Added" {
		t.Fatalf("expected first section title Added, got %q", sections[0].Title)
	}
	if len(sections[0].Items) != 2 {
		t.Fatalf("expected 2 Added items, got %d", len(sections[0].Items))
	}
	if sections[0].Items[1] != "Detached agent updater with checksum verification and heartbeat confirmation before rollout completion." {
		t.Fatalf("unexpected continuation merge result: %q", sections[0].Items[1])
	}
	if sections[1].Title != "Fixed" || len(sections[1].Items) != 1 {
		t.Fatalf("unexpected Fixed section: %#v", sections[1])
	}
}

func TestMergeCatalogReplacesAndSortsByPublishedAt(t *testing.T) {
	t.Parallel()

	merged := mergeCatalog(
		[]releaseManifest{
			{
				Version:     "1.0.0",
				PublishedAt: "2026-03-01T00:00:00Z",
				Commit:      "older",
				Channel:     "tag",
			},
			{
				Version:     "1.1.0",
				PublishedAt: "2026-03-20T00:00:00Z",
				Commit:      "stale",
				Channel:     "tag",
			},
		},
		releaseManifest{
			Version:     "1.1.0",
			PublishedAt: "2026-04-01T00:00:00Z",
			Commit:      "fresh",
			Channel:     "tag",
		},
	)

	if len(merged) != 2 {
		t.Fatalf("expected 2 merged releases, got %d", len(merged))
	}
	if merged[0].Version != "1.1.0" || merged[0].Commit != "fresh" {
		t.Fatalf("expected refreshed 1.1.0 first, got %#v", merged[0])
	}
	if merged[1].Version != "1.0.0" {
		t.Fatalf("expected 1.0.0 second, got %#v", merged[1])
	}
}

func TestLoadChecksums(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	checksumPath := filepath.Join(tempDir, "SHA256SUMS")
	content := "abc123  noderax-agent-linux-amd64\nxyz789 *noderax-agent-linux-arm64\n"
	if err := os.WriteFile(checksumPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write checksums: %v", err)
	}

	checksums, err := loadChecksums(checksumPath)
	if err != nil {
		t.Fatalf("load checksums: %v", err)
	}

	if checksums[artifactAMD64] != "abc123" {
		t.Fatalf("unexpected amd64 checksum: %q", checksums[artifactAMD64])
	}
	if checksums[artifactARM64] != "xyz789" {
		t.Fatalf("unexpected arm64 checksum: %q", checksums[artifactARM64])
	}
}
