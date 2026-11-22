package vendor

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// DefaultAccountID is the identifier of each vendor's primary home account.
const DefaultAccountID = "default"

const maxAccountIDLen = 64

// MultiAccountTermsWarning returns the vendor terms-of-service warning shown
// when the user opts into multiple accounts per vendor (orchestration §8).
func MultiAccountTermsWarning() string {
	return "Using multiple accounts per vendor is opt-in: Anthropic prohibits account sharing and assumes ordinary, individual usage, and OpenAI may limit paid accounts per device. Only use accounts you personally own; every account switch is logged."
}

// ValidateAccountID checks that an extra vendor account ID is a safe single
// directory segment without path traversal, separators, or reserved names.
func ValidateAccountID(id string) error {
	if id == "" || len(id) > maxAccountIDLen {
		return errcode.New(errcode.InvalidArgument, "account ID must be 1..64 characters")
	}
	if id == DefaultAccountID {
		return errcode.New(errcode.InvalidArgument, "account ID \"default\" is reserved for the primary account")
	}
	if strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
		return errcode.New(errcode.InvalidArgument, "account ID must not contain path separators or '..'")
	}
	for i := range len(id) {
		if !isValidAccountIDChar(i, id[i]) {
			return errcode.New(errcode.InvalidArgument, "account ID must start with an ASCII letter or digit and contain only letters, digits, '.', '_' or '-'")
		}
	}
	return nil
}

func isValidAccountIDChar(pos int, c byte) bool {
	if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
		return true
	}
	return pos > 0 && (c == '.' || c == '_' || c == '-')
}

// PrepareExtraHome creates an isolated vendor home directory under
// <dataDir>/vendor-homes/<vendor>/<accountID> (0700) and symlinks shared
// non-credential settings, plugins, and skills from primaryHomeDir when present.
func PrepareExtraHome(dataDir, vendorName, accountID, primaryHomeDir string) (AccountHome, error) {
	if dataDir == "" || !filepath.IsAbs(dataDir) {
		return AccountHome{}, errcode.New(errcode.InvalidArgument, "data directory must be an absolute path")
	}
	if !isKnownVendorName(vendorName) {
		return AccountHome{}, errcode.New(errcode.InvalidArgument, "unknown vendor for extra account home")
	}
	if err := ValidateAccountID(accountID); err != nil {
		return AccountHome{}, err
	}
	cleanData := filepath.Clean(dataDir)
	extraDir, err := ensureSafeSubdir(cleanData, "vendor-homes", vendorName, accountID)
	if err != nil {
		return AccountHome{}, err
	}
	if primaryHomeDir != "" {
		if linkErr := linkSharedVendorItems(vendorName, filepath.Clean(primaryHomeDir), extraDir); linkErr != nil {
			return AccountHome{}, linkErr
		}
	}
	return AccountHome{ConfigDir: extraDir}, nil
}

func isKnownVendorName(v string) bool {
	return v == "claude" || v == "codex" || v == "agy"
}

func ensureSafeSubdir(base string, parts ...string) (string, error) {
	cur := base
	for _, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", errcode.New(errcode.PermissionDenied, "vendor home path component must not be a symlink")
			}
			if !info.IsDir() {
				return "", errcode.New(errcode.FailedPrecondition, "vendor home path component is not a directory")
			}
			continue
		}
		if !os.IsNotExist(err) {
			return "", errcode.Wrap(errcode.Internal, "inspect vendor home directory", err)
		}
		if mkErr := os.Mkdir(cur, 0o700); mkErr != nil {
			return "", errcode.Wrap(errcode.Internal, "create vendor home directory", mkErr)
		}
	}
	return cur, nil
}

func sharedVendorItems(vendorName string) []string {
	switch vendorName {
	case "claude":
		return []string{"settings.json", "settings.local.json", "plugins", "skills", "commands", "CLAUDE.md"}
	case "codex":
		return []string{"config.toml", "instructions.md", "skills", "rules"}
	case "agy":
		return []string{
			filepath.Join(".gemini", "settings.json"),
			filepath.Join(".gemini", "GEMINI.md"),
			filepath.Join(".gemini", "antigravity", "skills"),
		}
	default:
		return nil
	}
}

func linkSharedVendorItems(vendorName, primaryDir, extraDir string) error {
	info, statErr := os.Stat(primaryDir)
	if statErr == nil && info.IsDir() {
		for _, rel := range sharedVendorItems(vendorName) {
			if err := linkOneSharedItem(primaryDir, extraDir, rel); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasPath(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func linkOneSharedItem(primaryDir, extraDir, rel string) error {
	src := filepath.Join(primaryDir, rel)
	dst := filepath.Join(extraDir, rel)
	if !hasPath(src) || hasPath(dst) {
		return nil
	}
	if dir := filepath.Dir(rel); dir != "." {
		if _, mkErr := ensureSafeSubdir(extraDir, strings.Split(dir, string(filepath.Separator))...); mkErr != nil {
			return mkErr
		}
	}
	if symErr := os.Symlink(src, dst); symErr != nil && !os.IsExist(symErr) {
		return errcode.Wrap(errcode.Internal, "symlink shared vendor settings", symErr)
	}
	return nil
}
