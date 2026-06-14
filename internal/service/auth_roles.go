package service

import (
	"sort"
	"strings"

	"chatgpt2api/internal/util"
)

func normalizeManagedRoles(raw any) []ManagedRole {
	items := util.AsMapSlice(raw)
	if obj, ok := raw.(map[string]any); ok {
		items = util.AsMapSlice(obj["items"])
	}
	roles := make([]ManagedRole, 0, len(items)+1)
	for _, item := range items {
		role := normalizeManagedRole(item)
		if role.ID == "" {
			continue
		}
		roles = append(roles, role)
	}
	roles = mergeDefaultManagedRole(roles)
	sortManagedRoles(roles)
	return roles
}

func normalizeManagedRole(raw map[string]any) ManagedRole {
	id := util.Clean(raw["id"])
	name := util.Clean(raw["name"])
	if id == "" || name == "" {
		return ManagedRole{}
	}
	return ManagedRole{
		ID:             id,
		Name:           name,
		Description:    util.Clean(raw["description"]),
		Builtin:        util.ToBool(raw["builtin"]) && id == DefaultManagedRoleID,
		MenuPaths:      NormalizeMenuPermissions(util.AsStringSlice(raw["menu_paths"])),
		APIPermissions: NormalizeAPIPermissions(util.AsStringSlice(raw["api_permissions"])),
		CreatedAt:      util.Clean(raw["created_at"]),
		UpdatedAt:      util.Clean(raw["updated_at"]),
	}
}

func mergeDefaultManagedRole(roles []ManagedRole) []ManagedRole {
	defaultRole := defaultManagedRole()
	out := make([]ManagedRole, 0, len(roles)+1)
	seenDefault := false
	seen := map[string]struct{}{}
	for _, role := range roles {
		if _, ok := seen[role.ID]; ok {
			continue
		}
		seen[role.ID] = struct{}{}
		if role.ID == DefaultManagedRoleID {
			role.Builtin = true
			if role.Name == "" {
				role.Name = defaultRole.Name
			}
			if role.Description == "" {
				role.Description = defaultRole.Description
			}
			out = append(out, role)
			seenDefault = true
			continue
		}
		role.Builtin = false
		out = append(out, role)
	}
	if !seenDefault {
		out = append(out, defaultRole)
	}
	return out
}

func defaultManagedRole() ManagedRole {
	permissions := DefaultPermissionSetForRole(AuthRoleUser)
	return ManagedRole{
		ID:             DefaultManagedRoleID,
		Name:           "普通用户",
		Description:    "默认用户角色，适合基础创作和个人令牌管理。",
		Builtin:        true,
		MenuPaths:      permissions.MenuPaths,
		APIPermissions: permissions.APIPermissions,
	}
}

func sortManagedRoles(roles []ManagedRole) {
	sort.SliceStable(roles, func(i, j int) bool {
		if roles[i].Builtin != roles[j].Builtin {
			return roles[i].Builtin
		}
		if roles[i].Name != roles[j].Name {
			return roles[i].Name < roles[j].Name
		}
		return roles[i].ID < roles[j].ID
	})
}

func managedRoleByIDLocked(roles []ManagedRole, id string) (ManagedRole, bool) {
	id = util.Clean(id)
	if id == "" {
		id = DefaultManagedRoleID
	}
	for _, role := range roles {
		if role.ID == id {
			return role, true
		}
	}
	return ManagedRole{}, false
}

func managedRoleName(roles []ManagedRole, id string) string {
	if role, ok := managedRoleByIDLocked(roles, id); ok {
		return role.Name
	}
	return defaultManagedRole().Name
}

func managedRoleNameExistsLocked(roles []ManagedRole, exceptID, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, role := range roles {
		if role.ID != exceptID && strings.EqualFold(role.Name, name) {
			return true
		}
	}
	return false
}

func applyManagedRoleToAuthItem(item map[string]any, role ManagedRole) {
	if util.Clean(item["role"]) != AuthRoleUser || role.ID == "" {
		return
	}
	item["role_id"] = role.ID
	item["role_name"] = role.Name
	applyPermissionSet(item, role.PermissionSet())
}

func managedRoleUserCountsLocked(items []map[string]any, accounts []PasswordAccount) map[string]int {
	seenUsers := map[string]struct{}{}
	counts := map[string]int{}
	for _, account := range accounts {
		if account.Role != AuthRoleUser || account.ID == "" {
			continue
		}
		key := account.ID + "\x00" + account.ManagedRoleID()
		if _, ok := seenUsers[key]; ok {
			continue
		}
		seenUsers[key] = struct{}{}
		counts[account.ManagedRoleID()]++
	}
	for _, item := range items {
		userID := managedAuthUserID(item)
		if userID == "" {
			continue
		}
		key := userID + "\x00" + managedAuthRoleID(item)
		if _, ok := seenUsers[key]; ok {
			continue
		}
		seenUsers[key] = struct{}{}
		counts[managedAuthRoleID(item)]++
	}
	return counts
}

func publicManagedRolesLocked(roles []ManagedRole, items []map[string]any, accounts []PasswordAccount) []map[string]any {
	counts := managedRoleUserCountsLocked(items, accounts)
	out := make([]map[string]any, 0, len(roles))
	for _, role := range roles {
		out = append(out, publicManagedRole(role, counts[role.ID]))
	}
	return out
}

func publicManagedRole(role ManagedRole, userCount int) map[string]any {
	return map[string]any{
		"id":              role.ID,
		"name":            role.Name,
		"description":     role.Description,
		"builtin":         role.Builtin,
		"user_count":      userCount,
		"created_at":      role.CreatedAt,
		"updated_at":      role.UpdatedAt,
		"menu_paths":      append([]string(nil), role.PermissionSet().MenuPaths...),
		"api_permissions": append([]string(nil), role.PermissionSet().APIPermissions...),
	}
}

func storedManagedRole(role ManagedRole) map[string]any {
	return map[string]any{
		"id":              role.ID,
		"name":            role.Name,
		"description":     role.Description,
		"builtin":         role.Builtin,
		"created_at":      role.CreatedAt,
		"updated_at":      role.UpdatedAt,
		"menu_paths":      append([]string(nil), role.PermissionSet().MenuPaths...),
		"api_permissions": append([]string(nil), role.PermissionSet().APIPermissions...),
	}
}
