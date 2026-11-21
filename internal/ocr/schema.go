package ocr

import (
	"encoding/json"
	"errors"
	"path"
	"slices"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// ErrUnsupportedSchema indicates that OCR emitted a schema_version outside
// Fugue's supported version, which §10.1 treats as "no OCR".
var ErrUnsupportedSchema = errors.New("unsupported ocr schema_version")

// PreviewOutput is the JSON payload emitted by `ocr delegate preview --format json`.
type PreviewOutput struct {
	SchemaVersion   string         `json:"schema_version"`
	Mode            string         `json:"mode"`
	Repository      string         `json:"repository,omitempty"`
	From            string         `json:"from,omitempty"`
	To              string         `json:"to,omitempty"`
	MergeBase       string         `json:"merge_base,omitempty"`
	TotalFiles      int            `json:"total_files"`
	ReviewableCount int            `json:"reviewable_count"`
	ExcludedCount   int            `json:"excluded_count"`
	ReviewableFiles []PreviewFile  `json:"reviewable_files"`
	ExcludedFiles   []ExcludedFile `json:"excluded_files"`
	Bundles         []BundleSpec   `json:"bundles,omitempty"`
}

// PreviewFile describes one reviewable file selected by `ocr delegate preview`.
type PreviewFile struct {
	Path      string `json:"path"`
	Status    string `json:"status,omitempty"`
	Additions int    `json:"additions,omitempty"`
	Deletions int    `json:"deletions,omitempty"`
	Bundle    string `json:"bundle,omitempty"`
}

// ExcludedFile describes one file excluded by `ocr delegate preview` and its reason.
type ExcludedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// BundleSpec groups reviewable files into a single reviewer assignment.
type BundleSpec struct {
	Name  string   `json:"name,omitempty"`
	Files []string `json:"files"`
}

// RuleOutput is the JSON payload emitted by `ocr delegate rule --format json`.
type RuleOutput struct {
	SchemaVersion string      `json:"schema_version"`
	Groups        []RuleGroup `json:"groups"`
}

// RuleGroup maps a set of file paths to the rules that apply to them.
type RuleGroup struct {
	Files []string   `json:"files"`
	Rules []RuleItem `json:"rules"`
}

// RuleItem is one deterministic review rule resolved by OCR.
type RuleItem struct {
	ID          string `json:"id"`
	Severity    string `json:"severity,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description"`
}

// ParsePreview decodes `ocr delegate preview --format json` output and checks schema_version.
func ParsePreview(raw []byte) (PreviewOutput, error) {
	var out PreviewOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return PreviewOutput{}, errcode.Wrap(errcode.InvalidArgument, "decode ocr preview output", err)
	}
	if strings.TrimSpace(out.SchemaVersion) != SupportedSchemaVersion {
		return PreviewOutput{}, ErrUnsupportedSchema
	}
	return out, nil
}

// ParseRule decodes `ocr delegate rule --format json` output and checks schema_version.
func ParseRule(raw []byte) (RuleOutput, error) {
	var out RuleOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return RuleOutput{}, errcode.Wrap(errcode.InvalidArgument, "decode ocr rule output", err)
	}
	if strings.TrimSpace(out.SchemaVersion) != SupportedSchemaVersion {
		return RuleOutput{}, ErrUnsupportedSchema
	}
	return out, nil
}

// BuildBundles returns deterministic file bundles from a parsed preview.
// When preview.Bundles is empty, files are grouped by PreviewFile.Bundle or by directory.
func BuildBundles(p *PreviewOutput) []BundleSpec {
	if p == nil {
		return nil
	}
	if len(p.Bundles) > 0 {
		out := make([]BundleSpec, 0, len(p.Bundles))
		for _, b := range p.Bundles {
			if len(b.Files) == 0 {
				continue
			}
			files := slices.Clone(b.Files)
			slices.Sort(files)
			out = append(out, BundleSpec{Name: b.Name, Files: files})
		}
		return out
	}
	byKey := make(map[string][]string)
	for _, f := range p.ReviewableFiles {
		if strings.TrimSpace(f.Path) == "" {
			continue
		}
		key := strings.TrimSpace(f.Bundle)
		if key == "" {
			key = path.Dir(f.Path)
		}
		byKey[key] = append(byKey[key], f.Path)
	}
	return sortedBundlesFromMap(byKey)
}

// DirectoryBundles groups changed file paths by their parent directory for the no-OCR fallback.
func DirectoryBundles(files []string) []BundleSpec {
	byDir := make(map[string][]string)
	for _, f := range files {
		clean := strings.TrimSpace(f)
		if clean == "" {
			continue
		}
		dir := path.Dir(clean)
		byDir[dir] = append(byDir[dir], clean)
	}
	return sortedBundlesFromMap(byDir)
}

func sortedBundlesFromMap(byKey map[string][]string) []BundleSpec {
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]BundleSpec, 0, len(keys))
	for _, k := range keys {
		files := slices.Clone(byKey[k])
		slices.Sort(files)
		out = append(out, BundleSpec{Name: k, Files: files})
	}
	return out
}
