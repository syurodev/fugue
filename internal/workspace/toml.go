package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/syurodev/fugue/internal/secrets"
)

type rawPolicy struct {
	ApprovalMode   *string       `toml:"approval_mode,omitempty"`
	SandboxProfile *string       `toml:"sandbox_profile,omitempty"`
	ShellAsk       *[]string     `toml:"shell_ask,omitempty"`
	ShellAskAdd    *[]string     `toml:"shell_ask_add,omitempty"`
	ShellAllow     *[]string     `toml:"shell_allow,omitempty"`
	ShellAllowAdd  *[]string     `toml:"shell_allow_add,omitempty"`
	EnvTags        *[]EnvTagRule `toml:"env_tags,omitempty"`
}

type rawEnv struct {
	Keep             *[]string `toml:"keep,omitempty"`
	KeepAdd          *[]string `toml:"keep_add,omitempty"`
	OverrideAllow    *[]string `toml:"override_allow,omitempty"`
	OverrideAllowAdd *[]string `toml:"override_allow_add,omitempty"`
}

type rawRedact struct {
	AllowKeys     *[]string `toml:"allow_keys,omitempty"`
	AllowKeysAdd  *[]string `toml:"allow_keys_add,omitempty"`
	AllowPaths    *[]string `toml:"allow_paths,omitempty"`
	AllowPathsAdd *[]string `toml:"allow_paths_add,omitempty"`
}

type rawSession struct {
	ScratchDir *string `toml:"scratch_dir,omitempty"`
}

type rawHost struct {
	WorkspacesDir          *string `toml:"workspaces_dir,omitempty"`
	OutputRetentionTotal   *string `toml:"output_retention_total,omitempty"`
	SeenVersionsPerSession *string `toml:"seen_versions_per_session,omitempty"`
	SnapshotDisk           *string `toml:"snapshot_disk,omitempty"`
}

type tier2RawConfig struct {
	Policy   *rawPolicy               `toml:"policy,omitempty"`
	Env      *rawEnv                  `toml:"env,omitempty"`
	Redact   *rawRedact               `toml:"redact,omitempty"`
	Roles    *[]RoleConfig            `toml:"roles,omitempty"`
	Profiles map[string]ProfileConfig `toml:"profiles,omitempty"`
	Session  *rawSession              `toml:"session,omitempty"`
	Host     *rawHost                 `toml:"host,omitempty"`
}

type tier3RawConfig struct {
	Name     *string                  `toml:"name,omitempty"`
	Roots    *[]Root                  `toml:"roots,omitempty"`
	Policy   *rawPolicy               `toml:"policy,omitempty"`
	Env      *rawEnv                  `toml:"env,omitempty"`
	Redact   *rawRedact               `toml:"redact,omitempty"`
	Roles    *[]RoleConfig            `toml:"roles,omitempty"`
	Profiles map[string]ProfileConfig `toml:"profiles,omitempty"`
	Session  *rawSession              `toml:"session,omitempty"`
}

type tier4RawConfig struct {
	Model  *string `toml:"model,omitempty"`
	Effort *string `toml:"effort,omitempty"`
}

type tomlConfig struct {
	Name  string `toml:"name"`
	Roots []Root `toml:"roots,omitempty"`
}

func encodeConfigTOML(name string, roots []Root) ([]byte, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(tomlConfig{Name: name, Roots: roots}); err != nil {
		return nil, WrapError(ErrConfigInvalid, "encode config", err)
	}
	return buf.Bytes(), nil
}

func decodeConfigTOML(data []byte) (tomlConfig, error) {
	raw, err := decodeTier3TOML("fugue.toml", data, "")
	return tomlConfig{Name: derefStr(raw.Name), Roots: derefSlice(raw.Roots)}, err
}

func decodeTier2TOML(file string, data []byte, homeDir string) (tier2RawConfig, error) {
	var cfg tier2RawConfig
	return cfg, firstErr(decodeStrictTOML(file, data, &cfg), validateTier2(file, &cfg, homeDir))
}

func decodeTier3TOML(file string, data []byte, homeDir string) (tier3RawConfig, error) {
	var cfg tier3RawConfig
	return cfg, firstErr(decodeStrictTOML(file, data, &cfg), validateTier3(file, &cfg, homeDir))
}

func decodeTier4TOML(file string, data []byte) (tier4RawConfig, error) {
	var cfg tier4RawConfig
	return cfg, firstErr(decodeStrictTOML(file, data, &cfg), validateTier4(file, &cfg))
}

func decodeStrictTOML(file string, data []byte, dst any) error {
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil {
		return nil
	}
	var strictErr *toml.StrictMissingError
	if errors.As(err, &strictErr) && len(strictErr.Errors) > 0 {
		row, col := strictErr.Errors[0].Position()
		return &Error{Code: ErrConfigInvalid, File: file, Key: strings.Join(strictErr.Errors[0].Key(), "."), Line: row, Column: col, Message: "unknown configuration key"}
	}
	var decErr *toml.DecodeError
	if errors.As(err, &decErr) {
		row, col := decErr.Position()
		return &Error{Code: ErrConfigInvalid, File: file, Key: strings.Join(decErr.Key(), "."), Line: row, Column: col, Message: "invalid TOML syntax or type"}
	}
	return &Error{Code: ErrConfigInvalid, File: file, Message: "decode TOML configuration failed"}
}

func computeConfigHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func cfgErr(file, key, msg string) error {
	return &Error{Code: ErrConfigInvalid, File: file, Key: key, Message: msg}
}

func errIf(cond bool, file, key, msg string) error {
	if cond {
		return cfgErr(file, key, msg)
	}
	return nil
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func derefSlice[T any](p *[]T) []T {
	if p == nil {
		return nil
	}
	return *p
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ValidateConfig strictly decodes and validates rawTOML for the given configuration tier.
func ValidateConfig(tier Tier, rawTOML []byte) error {
	switch tier {
	case TierGlobal:
		_, err := decodeTier2TOML("config.toml", rawTOML, "")
		return err
	case TierWorkspace:
		_, err := decodeTier3TOML("fugue.toml", rawTOML, "")
		return err
	case TierSession:
		_, err := decodeTier4TOML("session", rawTOML)
		return err
	case TierDefault:
		return NewError(ErrConfigInvalid, "default tier is embedded and cannot be decoded from TOML")
	default:
		return NewError(ErrConfigInvalid, "unsupported configuration tier")
	}
}

func validateTier2(file string, cfg *tier2RawConfig, homeDir string) error {
	return firstErr(validateCommon(file, cfg.Policy, cfg.Env, cfg.Redact, cfg.Roles, cfg.Profiles, cfg.Session, homeDir), validateHost(file, cfg.Host, homeDir))
}

func validateTier3(file string, cfg *tier3RawConfig, homeDir string) error {
	if err := checkNoSecret(file, "name", derefStr(cfg.Name)); err != nil {
		return err
	}
	for _, r := range derefSlice(cfg.Roots) {
		if err := firstErr(checkNoSecret(file, "roots.name", r.Name), checkNoSecret(file, "roots.path", r.Path)); err != nil {
			return err
		}
	}
	return validateCommon(file, cfg.Policy, cfg.Env, cfg.Redact, cfg.Roles, cfg.Profiles, cfg.Session, homeDir)
}

func validateCommon(
	file string, pol *rawPolicy, env *rawEnv, red *rawRedact,
	roles *[]RoleConfig, profiles map[string]ProfileConfig, sess *rawSession, homeDir string,
) error {
	var scratch *string
	if sess != nil {
		scratch = sess.ScratchDir
	}
	var redErr error
	if red != nil {
		redErr = firstErr(validateAddPair(file, "redact.allow_keys", red.AllowKeys, red.AllowKeysAdd, false), validateAddPair(file, "redact.allow_paths", red.AllowPaths, red.AllowPathsAdd, true))
	}
	return firstErr(
		validatePolicy(file, pol), validateEnv(file, env), redErr,
		validateRoles(file, roles), validateProfiles(file, profiles), validatePathField(file, "session.scratch_dir", scratch, homeDir),
	)
}

func validateTier4(file string, cfg *tier4RawConfig) error {
	return firstErr(
		errIf(cfg.Model != nil && strings.TrimSpace(*cfg.Model) == "", file, "model", "session model override cannot be empty"),
		errIf(cfg.Effort != nil && !isValidEffort(*cfg.Effort), file, "effort", "effort must be low, medium, high, xhigh, or max"),
		checkNoSecret(file, "model", derefStr(cfg.Model)),
	)
}

func validatePolicy(file string, pol *rawPolicy) error {
	if pol == nil {
		return nil
	}
	return firstErr(
		errIf(pol.ApprovalMode != nil && !slices.Contains([]string{"plan", "ask", "auto", "full"}, *pol.ApprovalMode), file, keyPolicyApprovalMode, "approval_mode must be plan, ask, auto, or full"),
		errIf(pol.SandboxProfile != nil && !slices.Contains([]string{"read-only", "workspace-write", "full-access"}, *pol.SandboxProfile), file, "policy.sandbox_profile", "sandbox_profile must be read-only, workspace-write, or full-access"),
		validateAddPair(file, "policy.shell_ask", pol.ShellAsk, pol.ShellAskAdd, false),
		validateAddPair(file, "policy.shell_allow", pol.ShellAllow, pol.ShellAllowAdd, false),
		validateEnvTags(file, pol.EnvTags),
	)
}

func validateEnvTags(file string, tags *[]EnvTagRule) error {
	for _, rule := range derefSlice(tags) {
		if err := firstErr(
			errIf(strings.TrimSpace(rule.Match) == "" || !isValidGlob(rule.Match), file, "policy.env_tags.match", "env_tags.match must be a valid non-empty glob pattern"),
			errIf(ValidateRootName(rule.Tag) != nil, file, "policy.env_tags.tag", "env_tags.tag is invalid"),
			checkNoSecret(file, "policy.env_tags.match", rule.Match),
			checkNoSecret(file, "policy.env_tags.tag", rule.Tag),
		); err != nil {
			return err
		}
	}
	return nil
}

func validateEnv(file string, env *rawEnv) error {
	if env == nil {
		return nil
	}
	chk := func(f, k, name string) error {
		return errIf(!isValidEnvVarName(name) || isDenylistedEnvVar(name), f, k, "invalid or denylisted J-04 environment variable name in "+k)
	}
	return firstErr(validatePair(file, "env.keep", env.Keep, env.KeepAdd, chk), validatePair(file, "env.override_allow", env.OverrideAllow, env.OverrideAllowAdd, chk))
}

func validateAddPair(file, baseKey string, base, add *[]string, checkGlob bool) error {
	return validatePair(file, baseKey, base, add, func(f, k, item string) error {
		return firstErr(errIf(strings.TrimSpace(item) == "" || (checkGlob && !isValidGlob(item)), f, k, k+" contains empty or invalid entry"), checkNoSecret(f, k, item))
	})
}

func validatePair(file, baseKey string, base, add *[]string, itemFn func(string, string, string) error) error {
	addKey := baseKey + "_add"
	return firstErr(errIf(base != nil && add != nil, file, addKey, "cannot specify both "+baseKey+" and "+addKey+" in the same tier"), validateList(file, baseKey, base, itemFn), validateList(file, addKey, add, itemFn))
}

func validateList(file, key string, list *[]string, itemFn func(string, string, string) error) error {
	for _, item := range derefSlice(list) {
		if err := itemFn(file, key, item); err != nil {
			return err
		}
	}
	return nil
}

func validateRoles(file string, roles *[]RoleConfig) error {
	items := derefSlice(roles)
	seen := make(map[string]struct{}, len(items))
	for i := range items {
		role := &items[i]
		_, dup := seen[role.Name]
		if err := firstErr(
			errIf(ValidateRootName(role.Name) != nil, file, "roles.name", "invalid role name"),
			errIf(dup, file, "roles.name", "duplicate role name "+role.Name),
			errIf(!slices.Contains([]string{"claude", "codex", "agy"}, role.Vendor), file, "roles.vendor", "role vendor must be claude, codex, or agy"),
			errIf(role.Effort != "" && !isValidEffort(role.Effort), file, "roles.effort", "role effort must be low, medium, high, xhigh, or max"),
			errIf(role.Permission != "read" && role.Permission != "write", file, "roles.permission", "role permission must be read or write"),
			checkNoSecret(file, "roles.description", role.Description),
			checkNoSecret(file, "roles.model", role.Model),
			validateAddPair(file, "roles.checks", &role.Checks, nil, false),
		); err != nil {
			return err
		}
		seen[role.Name] = struct{}{}
	}
	return nil
}

func validateProfiles(file string, profiles map[string]ProfileConfig) error {
	for name, prof := range profiles {
		prefix := "profiles." + name
		if err := firstErr(
			errIf(ValidateRootName(name) != nil, file, prefix, "invalid profile name"),
			errIf(prof.Vendor != "" && !slices.Contains([]string{"claude", "codex", "agy"}, prof.Vendor), file, prefix+".vendor", "profile vendor must be claude, codex, or agy"),
			errIf(prof.Effort != "" && !isValidEffort(prof.Effort), file, prefix+".effort", "profile effort must be low, medium, high, xhigh, or max"),
			checkNoSecret(file, prefix+".model", prof.Model),
		); err != nil {
			return err
		}
	}
	return nil
}

func validateHost(file string, host *rawHost, homeDir string) error {
	if host == nil {
		return nil
	}
	return firstErr(
		validatePathField(file, "host.workspaces_dir", host.WorkspacesDir, homeDir),
		validateByteSizeField(file, "host.output_retention_total", host.OutputRetentionTotal),
		validateByteSizeField(file, "host.seen_versions_per_session", host.SeenVersionsPerSession),
		validateByteSizeField(file, "host.snapshot_disk", host.SnapshotDisk),
	)
}

func validatePathField(file, key string, val *string, homeDir string) error {
	if val == nil {
		return nil
	}
	trimmed := expandHome(strings.TrimSpace(*val), homeDir)
	return firstErr(checkNoSecret(file, key, *val), errIf(trimmed == "", file, key, key+" cannot be empty"), errIf(!strings.HasPrefix(trimmed, "~") && (!filepath.IsAbs(trimmed) || filepath.Clean(trimmed) == "/"), file, key, key+" must be a non-root absolute path"))
}

func validateByteSizeField(file, key string, val *string) error {
	return firstErr(checkNoSecret(file, key, derefStr(val)), errIf(val != nil && !isValidByteSize(*val), file, key, "invalid byte size for "+key))
}

func checkNoSecret(file, key, val string) error {
	return errIf(val != "" && (len(secrets.Find(val)) > 0 || len(secrets.Find(key+" = "+val)) > 0), file, key, "secret detected in configuration key "+key)
}

func isValidEffort(effort string) bool {
	return slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, effort)
}

func isValidGlob(pattern string) bool {
	_, err := path.Match(pattern, "sample")
	return err == nil
}

func isValidEnvVarName(name string) bool {
	for i := range len(name) {
		ch := name[i]
		isUpper, isDigit := ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9'
		if (i == 0 && !isUpper && ch != '_') || (!isUpper && !isDigit && ch != '_') {
			return false
		}
	}
	return name != ""
}

func isDenylistedEnvVar(name string) bool {
	u := strings.ToUpper(name)
	return strings.HasPrefix(u, "AWS_") || strings.HasSuffix(u, "_KEY") ||
		slices.ContainsFunc([]string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "PRIVATE"}, func(s string) bool { return strings.Contains(u, s) })
}

func isValidByteSize(raw string) bool {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) != 2 {
		return false
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	return err == nil && n > 0 && slices.Contains([]string{"B", "KB", "MB", "GB", "KiB", "MiB", "GiB"}, fields[1])
}
