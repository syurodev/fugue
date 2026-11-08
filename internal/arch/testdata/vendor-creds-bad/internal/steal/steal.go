package steal

func BadPaths() []string {
	return []string{
		".claude/.credentials.json",
		".codex/auth.json",
		"Claude Code-credentials",
		"security find-generic-password -s Claude",
	}
}
