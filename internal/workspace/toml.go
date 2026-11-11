package workspace

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

type rawConfig struct {
	Name  string
	Roots []Root
}

func formatMinimalTOML(name string, roots []Root) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "name = %q\n", name)
	for _, r := range roots {
		fmt.Fprintf(&sb, "\n[[roots]]\nname = %q\npath = %q\naccess = %q\n", r.Name, r.Path, r.Access)
	}
	return sb.String()
}

func parseMinimalTOML(data []byte) (rawConfig, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var cfg rawConfig
	var curRoot *Root
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "[[roots]]" {
			if curRoot != nil {
				cfg.Roots = append(cfg.Roots, *curRoot)
			}
			curRoot = &Root{}
			continue
		}
		parseKeyValue(line, &cfg, curRoot)
	}
	if curRoot != nil {
		cfg.Roots = append(cfg.Roots, *curRoot)
	}
	if err := scanner.Err(); err != nil {
		return rawConfig{}, WrapError(ErrRootInvalid, "parse config file", err)
	}
	return cfg, nil
}

func parseKeyValue(line string, cfg *rawConfig, curRoot *Root) {
	k, v, ok := strings.Cut(line, "=")
	if !ok {
		return
	}
	key, val := strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"`)
	if curRoot == nil {
		if key == "name" {
			cfg.Name = val
		}
		return
	}
	switch key {
	case "name":
		curRoot.Name = val
	case "path":
		curRoot.Path = val
	case "access":
		curRoot.Access = RootAccess(val)
	}
}

func computeConfigHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
