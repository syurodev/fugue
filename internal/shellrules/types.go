package shellrules

// Limits governing shell command parsing to prevent unbounded resource consumption.
const (
	// MaxCommandLength is the maximum allowed byte length of a shell command string.
	MaxCommandLength = 4096

	// MaxNestingDepth is the maximum nesting depth of subshells, groups, and command substitutions.
	MaxNestingDepth = 16

	// MaxSimpleCommands is the maximum number of simple commands extracted from a command string.
	MaxSimpleCommands = 128
)

// Matched rule identifiers for static shell evaluation (interaction §3.3).
const (
	RuleRemotePush         = "remote_push"
	RuleDestructiveOutside = "destructive_outside"
	RuleDownloadPipe       = "download_pipe"
	RuleDeployInfra        = "deploy_infra"
	RuleEnvTag             = "env_tag"
	RuleWorkspaceAsk       = "workspace_ask"
	RuleUnparsable         = "unparsable"
)

// Risk represents the highest impact an operation can cause (interaction §3.1).
type Risk string

// The supported risk levels (interaction §3.1).
const (
	RiskRead        Risk = "read"
	RiskWrite       Risk = "write"
	RiskDestructive Risk = "destructive"
	RiskExternal    Risk = "external"
)

// Command represents a single resolved simple command extracted from a compound shell command.
type Command struct {
	Raw  string
	Name string
	Base string
	Args []string
	Env  []string
	// Redirects holds the static targets of output redirections (>, >>, >|, &>, N>).
	Redirects []string
	PipedTo   bool
}

// TagPolicy reports whether an environment tag requires confirmation (interaction §3.4).
type TagPolicy interface {
	RequiresConfirmation(name string) bool
}

// Options provides context for static shell rule inspection.
type Options struct {
	WorkspaceRoots []string
	ShellAsk       []string
	ShellAllow     []string
	Target         string
	EnvTag         string
	TagPolicy      TagPolicy
}

// Verdict is the outcome of static shell rule inspection (interaction §3.3).
type Verdict struct {
	Ask         bool
	Risk        Risk
	Unparsable  bool
	MatchedRule string
	Commands    []Command
}
