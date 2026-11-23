package team

// Role is the part of a workspace catalog entry used by the proposal editor.
type Role struct {
	Name, Vendor, Model string
}

func readRoles(values map[string]any) []Role {
	entries, ok := values["Roles"].([]any)
	if !ok {
		return nil
	}
	roles := make([]Role, 0, len(entries))
	for _, entry := range entries {
		data, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name := roleValue(data, "Name")
		if name != "" {
			roles = append(roles, Role{Name: name, Vendor: roleValue(data, "Vendor"), Model: roleValue(data, "Model")})
		}
	}
	return roles
}

func roleValue(fields map[string]any, key string) string {
	if value, ok := fields[key].(string); ok {
		return value
	}
	return ""
}
