package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	artifactAMD64 = "noderax-agent-linux-amd64"
	artifactARM64 = "noderax-agent-linux-arm64"
)

type releaseManifest struct {
	Version     string                   `json:"version"`
	PublishedAt string                   `json:"publishedAt"`
	Commit      string                   `json:"commit"`
	Channel     string                   `json:"channel"`
	Notes       []releaseNotesSection    `json:"notes"`
	Artifacts   releaseManifestArtifacts `json:"artifacts"`
}

type releaseManifestArtifacts struct {
	AMD64 *releaseArtifact `json:"amd64,omitempty"`
	ARM64 *releaseArtifact `json:"arm64,omitempty"`
}

type releaseArtifact struct {
	BinaryURL string `json:"binaryUrl"`
	SHA256    string `json:"sha256"`
}

type releaseNotesSection struct {
	Title string   `json:"title"`
	Items []string `json:"items"`
}

type releaseCatalog struct {
	GeneratedAt string            `json:"generatedAt,omitempty"`
	Releases    []releaseManifest `json:"releases"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "release-metadata: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("expected a subcommand: bundle or catalog")
	}

	switch args[0] {
	case "bundle":
		return runBundle(args[1:])
	case "catalog":
		return runCatalog(args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func runBundle(args []string) error {
	fs := flag.NewFlagSet("bundle", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var version string
	var commit string
	var publishedAt string
	var artifactsDir string
	var downloadBaseURL string
	var manifestOutput string
	var releaseNotesOutput string
	var changelogPath string

	fs.StringVar(&version, "version", "", "tagged agent version")
	fs.StringVar(&commit, "commit", "", "git commit sha")
	fs.StringVar(&publishedAt, "published-at", "", "release timestamp in RFC3339")
	fs.StringVar(&artifactsDir, "artifacts-dir", "", "directory that contains built artifacts")
	fs.StringVar(&downloadBaseURL, "download-base-url", "", "public CDN base URL for the versioned release")
	fs.StringVar(&manifestOutput, "manifest-output", "", "output path for release-manifest.json")
	fs.StringVar(&releaseNotesOutput, "release-notes-output", "", "output path for markdown release notes")
	fs.StringVar(&changelogPath, "changelog", "CHANGELOG.md", "path to CHANGELOG.md")

	if err := fs.Parse(args); err != nil {
		return err
	}

	version = strings.TrimSpace(version)
	commit = strings.TrimSpace(commit)
	publishedAt = strings.TrimSpace(publishedAt)
	artifactsDir = strings.TrimSpace(artifactsDir)
	downloadBaseURL = strings.TrimRight(strings.TrimSpace(downloadBaseURL), "/")
	manifestOutput = strings.TrimSpace(manifestOutput)
	releaseNotesOutput = strings.TrimSpace(releaseNotesOutput)
	changelogPath = strings.TrimSpace(changelogPath)

	switch {
	case version == "":
		return errors.New("--version is required")
	case commit == "":
		return errors.New("--commit is required")
	case publishedAt == "":
		return errors.New("--published-at is required")
	case artifactsDir == "":
		return errors.New("--artifacts-dir is required")
	case downloadBaseURL == "":
		return errors.New("--download-base-url is required")
	case manifestOutput == "":
		return errors.New("--manifest-output is required")
	case releaseNotesOutput == "":
		return errors.New("--release-notes-output is required")
	}

	if _, err := time.Parse(time.RFC3339, publishedAt); err != nil {
		return fmt.Errorf("parse --published-at: %w", err)
	}

	notes, err := parseChangelogRelease(changelogPath, version)
	if err != nil {
		return err
	}

	checksums, err := loadChecksums(filepath.Join(artifactsDir, "SHA256SUMS"))
	if err != nil {
		return err
	}

	manifest := releaseManifest{
		Version:     version,
		PublishedAt: publishedAt,
		Commit:      commit,
		Channel:     "tag",
		Notes:       notes,
		Artifacts: releaseManifestArtifacts{
			AMD64: buildReleaseArtifact(downloadBaseURL, artifactAMD64, checksums),
			ARM64: buildReleaseArtifact(downloadBaseURL, artifactARM64, checksums),
		},
	}

	if manifest.Artifacts.AMD64 == nil || manifest.Artifacts.ARM64 == nil {
		return errors.New("release bundle is missing one or more required architecture artifacts")
	}

	if err := writeJSONFile(manifestOutput, manifest); err != nil {
		return err
	}

	releaseNotesMarkdown := renderReleaseNotesMarkdown(version, notes)
	if err := writeTextFile(releaseNotesOutput, releaseNotesMarkdown); err != nil {
		return err
	}

	return nil
}

func runCatalog(args []string) error {
	fs := flag.NewFlagSet("catalog", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var manifestPath string
	var existingPath string
	var outputPath string

	fs.StringVar(&manifestPath, "manifest", "", "path to release-manifest.json")
	fs.StringVar(&existingPath, "existing", "", "optional existing catalog.json")
	fs.StringVar(&outputPath, "output", "", "output path for catalog.json")

	if err := fs.Parse(args); err != nil {
		return err
	}

	manifestPath = strings.TrimSpace(manifestPath)
	existingPath = strings.TrimSpace(existingPath)
	outputPath = strings.TrimSpace(outputPath)

	switch {
	case manifestPath == "":
		return errors.New("--manifest is required")
	case outputPath == "":
		return errors.New("--output is required")
	}

	var manifest releaseManifest
	if err := readJSONFile(manifestPath, &manifest); err != nil {
		return err
	}

	existing, err := loadCatalog(existingPath)
	if err != nil {
		return err
	}

	merged := mergeCatalog(existing.Releases, manifest)
	catalog := releaseCatalog{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Releases:    merged,
	}

	return writeJSONFile(outputPath, catalog)
}

func parseChangelogRelease(path string, version string) ([]releaseNotesSection, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read changelog %s: %w", path, err)
	}

	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	sectionHeader := fmt.Sprintf("## [%s]", version)

	inRelease := false
	current := releaseNotesSection{}
	var sections []releaseNotesSection

	flushCurrent := func() {
		if strings.TrimSpace(current.Title) == "" || len(current.Items) == 0 {
			return
		}
		sections = append(sections, current)
		current = releaseNotesSection{}
	}

	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, " \t")
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "## [") {
			if inRelease {
				flushCurrent()
				break
			}
			inRelease = trimmed == sectionHeader || strings.HasPrefix(trimmed, sectionHeader+" ")
			continue
		}

		if !inRelease {
			continue
		}

		if strings.HasPrefix(trimmed, "### ") {
			flushCurrent()
			current = releaseNotesSection{
				Title: strings.TrimSpace(strings.TrimPrefix(trimmed, "### ")),
				Items: []string{},
			}
			continue
		}

		if item, ok := parseBulletLine(trimmed); ok {
			if strings.TrimSpace(current.Title) == "" {
				current.Title = "Notes"
			}
			current.Items = append(current.Items, item)
			continue
		}

		if trimmed == "" || len(current.Items) == 0 {
			continue
		}

		if strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") {
			lastIndex := len(current.Items) - 1
			current.Items[lastIndex] = strings.TrimSpace(
				current.Items[lastIndex] + " " + trimmed,
			)
		}
	}

	flushCurrent()

	if len(sections) == 0 {
		return nil, fmt.Errorf(
			"changelog %s does not contain a structured section for version %s",
			path,
			version,
		)
	}

	return sections, nil
}

func parseBulletLine(line string) (string, bool) {
	switch {
	case strings.HasPrefix(line, "- "):
		return strings.TrimSpace(strings.TrimPrefix(line, "- ")), true
	case strings.HasPrefix(line, "* "):
		return strings.TrimSpace(strings.TrimPrefix(line, "* ")), true
	default:
		return "", false
	}
}

func loadChecksums(path string) (map[string]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read checksums %s: %w", path, err)
	}

	checksums := make(map[string]string)
	for _, rawLine := range strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("invalid checksum line %q", rawLine)
		}

		filename := strings.TrimPrefix(fields[len(fields)-1], "*")
		checksums[filename] = fields[0]
	}

	return checksums, nil
}

func buildReleaseArtifact(
	downloadBaseURL string,
	filename string,
	checksums map[string]string,
) *releaseArtifact {
	sum := strings.TrimSpace(checksums[filename])
	if sum == "" {
		return nil
	}

	return &releaseArtifact{
		BinaryURL: downloadBaseURL + "/" + filename,
		SHA256:    sum,
	}
}

func renderReleaseNotesMarkdown(
	version string,
	sections []releaseNotesSection,
) string {
	var builder strings.Builder
	builder.WriteString("## Noderax Agent ")
	builder.WriteString(version)
	builder.WriteString("\n\n")

	for index, section := range sections {
		builder.WriteString("### ")
		builder.WriteString(section.Title)
		builder.WriteString("\n")
		for _, item := range section.Items {
			builder.WriteString("- ")
			builder.WriteString(item)
			builder.WriteString("\n")
		}
		if index < len(sections)-1 {
			builder.WriteString("\n")
		}
	}

	return builder.String()
}

func loadCatalog(path string) (releaseCatalog, error) {
	if strings.TrimSpace(path) == "" {
		return releaseCatalog{Releases: []releaseManifest{}}, nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return releaseCatalog{Releases: []releaseManifest{}}, nil
		}
		return releaseCatalog{}, fmt.Errorf("read existing catalog %s: %w", path, err)
	}

	trimmed := strings.TrimSpace(string(content))
	if trimmed == "" {
		return releaseCatalog{Releases: []releaseManifest{}}, nil
	}

	var wrapped releaseCatalog
	if err := json.Unmarshal(content, &wrapped); err == nil && wrapped.Releases != nil {
		return wrapped, nil
	}

	var releases []releaseManifest
	if err := json.Unmarshal(content, &releases); err == nil {
		return releaseCatalog{Releases: releases}, nil
	}

	return releaseCatalog{}, fmt.Errorf("existing catalog %s is not valid JSON", path)
}

func mergeCatalog(
	existing []releaseManifest,
	manifest releaseManifest,
) []releaseManifest {
	merged := make([]releaseManifest, 0, len(existing)+1)
	seen := map[string]struct{}{
		manifest.Version: {},
	}
	merged = append(merged, manifest)

	for _, release := range existing {
		if _, ok := seen[release.Version]; ok {
			continue
		}
		seen[release.Version] = struct{}{}
		merged = append(merged, release)
	}

	sort.SliceStable(merged, func(leftIndex, rightIndex int) bool {
		left := merged[leftIndex]
		right := merged[rightIndex]

		leftTime, leftErr := time.Parse(time.RFC3339, left.PublishedAt)
		rightTime, rightErr := time.Parse(time.RFC3339, right.PublishedAt)
		if leftErr == nil && rightErr == nil && !leftTime.Equal(rightTime) {
			return leftTime.After(rightTime)
		}
		if left.Version != right.Version {
			return left.Version > right.Version
		}
		return left.Commit > right.Commit
	})

	return merged
}

func writeJSONFile(path string, value any) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON for %s: %w", path, err)
	}
	content = append(content, '\n')
	return writeTextFile(path, string(content))
}

func writeTextFile(path string, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write file %s: %w", path, err)
	}
	return nil
}

func readJSONFile(path string, target any) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read JSON file %s: %w", path, err)
	}
	if err := json.Unmarshal(content, target); err != nil {
		return fmt.Errorf("decode JSON file %s: %w", path, err)
	}
	return nil
}
