package httpapi

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"chatgpt2api/internal/service"
	"chatgpt2api/internal/util"
)

type managedUsersQuery struct {
	Page       int
	PageSize   int
	Search     string
	Provider   string
	Status     string
	SortBy     string
	SortOrder  string
	Total      int
	TotalPages int
}

func (a *App) managedUsersResponse(r *http.Request) (map[string]any, error) {
	query, err := parseManagedUsersQuery(r)
	if err != nil {
		return nil, err
	}
	items := filterManagedUsers(a.auth.ListUsers(), query)
	a.prepareManagedUsersSortValues(items, query.SortBy)
	sortManagedUsers(items, query)
	query.Total = len(items)
	query.TotalPages = managedUsersTotalPages(query.Total, query.PageSize)
	if query.Page > query.TotalPages {
		query.Page = query.TotalPages
	}
	start := (query.Page - 1) * query.PageSize
	if start > query.Total {
		start = query.Total
	}
	end := start + query.PageSize
	if end > query.Total {
		end = query.Total
	}
	pageItems := items[start:end]
	a.attachManagedUserUsage(pageItems)
	return map[string]any{
		"items":       pageItems,
		"total":       query.Total,
		"page":        query.Page,
		"page_size":   query.PageSize,
		"sort_by":     query.SortBy,
		"sort_order":  query.SortOrder,
		"total_pages": query.TotalPages,
	}, nil
}

func (a *App) managedUser(id string) map[string]any {
	item := findManagedUser(a.auth.ListUsers(), id)
	if item == nil {
		return nil
	}
	a.attachManagedUserUsage([]map[string]any{item})
	return item
}

func (a *App) attachManagedUserUsage(items []map[string]any) {
	userIDs := managedUserIDs(items)
	if len(userIDs) == 0 {
		return
	}
	a.attachManagedUserUsageStats(items, userIDs)
	a.attachManagedUserBillingStates(items, userIDs)
}

func managedUserIDs(items []map[string]any) []string {
	userIDs := make([]string, 0, len(items))
	for _, item := range items {
		if userID := util.Clean(item["id"]); userID != "" {
			userIDs = append(userIDs, userID)
		}
	}
	return userIDs
}

func (a *App) attachManagedUserUsageStats(items []map[string]any, userIDs []string) {
	stats := a.logs.UserUsageStatsForUsers(14, userIDs)
	for _, item := range items {
		userID := util.Clean(item["id"])
		usage := stats[userID]
		if usage == nil {
			usage = service.ZeroUserUsageStats(14)
		}
		for key, value := range usage {
			item[key] = value
		}
	}
}

func (a *App) attachManagedUserBillingStates(items []map[string]any, userIDs []string) {
	billingStates := a.billing.GetMany(userIDs)
	for _, item := range items {
		userID := util.Clean(item["id"])
		item["billing"] = billingStates[userID]
	}
}

func (a *App) prepareManagedUsersSortValues(items []map[string]any, sortBy string) {
	if len(items) == 0 {
		return
	}
	switch sortBy {
	case "call_count", "quota_used", "failure_count":
		a.attachManagedUserUsageStats(items, managedUserIDs(items))
	case "billing_available":
		a.attachManagedUserBillingStates(items, managedUserIDs(items))
	}
}

func (a *App) bulkBillingTargetUserIDs(body map[string]any) ([]string, error) {
	scope := strings.ToLower(strings.TrimSpace(util.Clean(body["scope"])))
	if scope == "" {
		scope = "users"
	}
	users := a.auth.ListUsers()
	switch scope {
	case "users":
		rawIDs := util.AsStringSlice(body["user_ids"])
		if len(rawIDs) == 0 {
			rawIDs = util.AsStringSlice(body["ids"])
		}
		return existingManagedUserIDs(users, rawIDs)
	case "role":
		roleID := util.Clean(body["role_id"])
		if roleID == "" {
			return nil, fmt.Errorf("role id is required")
		}
		if !a.auth.RoleExists(roleID) {
			return nil, fmt.Errorf("role not found")
		}
		return managedUserIDsByRole(users, roleID)
	default:
		return nil, fmt.Errorf("unsupported billing target scope: %s", scope)
	}
}

func existingManagedUserIDs(items []map[string]any, requested []string) ([]string, error) {
	available := map[string]struct{}{}
	for _, item := range items {
		if id := util.Clean(item["id"]); id != "" {
			available[id] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(requested))
	for _, id := range requested {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		if _, ok := available[id]; !ok {
			return nil, fmt.Errorf("user not found: %s", id)
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("user ids are required")
	}
	return out, nil
}

func managedUserIDsByRole(items []map[string]any, roleID string) ([]string, error) {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if util.Clean(item["role_id"]) != roleID {
			continue
		}
		if id := util.Clean(item["id"]); id != "" {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("role has no users")
	}
	return out, nil
}

func publicBulkBillingAdjustmentResults(results []service.BillingBulkAdjustmentResult) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for _, result := range results {
		item := map[string]any{
			"user_id": result.UserID,
			"billing": result.Billing,
		}
		if result.Adjustment != nil {
			item["adjustment"] = result.Adjustment
		}
		if result.Error != "" {
			item["error"] = result.Error
		}
		out = append(out, item)
	}
	return out
}

func bulkBillingAdjustmentSummary(results []service.BillingBulkAdjustmentResult) map[string]any {
	succeeded := 0
	failed := 0
	for _, result := range results {
		if result.Error != "" {
			failed++
			continue
		}
		succeeded++
	}
	return map[string]any{
		"total":     len(results),
		"succeeded": succeeded,
		"failed":    failed,
	}
}

func parseManagedUsersQuery(r *http.Request) (managedUsersQuery, error) {
	values := r.URL.Query()
	page, err := parseManagedUsersPage(values.Get("page"))
	if err != nil {
		return managedUsersQuery{}, err
	}
	pageSize, err := parseManagedUsersPageSize(values.Get("page_size"))
	if err != nil {
		return managedUsersQuery{}, err
	}
	sortBy, err := parseManagedUsersSortBy(values.Get("sort_by"))
	if err != nil {
		return managedUsersQuery{}, err
	}
	sortOrder, err := parseManagedUsersSortOrder(values.Get("sort_order"))
	if err != nil {
		return managedUsersQuery{}, err
	}
	return managedUsersQuery{
		Page:      page,
		PageSize:  pageSize,
		Search:    strings.TrimSpace(values.Get("search")),
		Provider:  strings.TrimSpace(values.Get("provider")),
		Status:    strings.TrimSpace(values.Get("status")),
		SortBy:    sortBy,
		SortOrder: sortOrder,
	}, nil
}

func parseManagedUsersPage(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 1, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("page 参数无效")
	}
	return value, nil
}

func parseManagedUsersPageSize(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 20, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("page_size 参数无效")
	}
	return normalizedManagedUsersPageSize(value), nil
}

func normalizedManagedUsersPageSize(value int) int {
	if value <= 0 {
		return 20
	}
	if value > 100 {
		return 100
	}
	return value
}

func managedUsersTotalPages(total, pageSize int) int {
	if pageSize <= 0 {
		pageSize = 20
	}
	if total <= 0 {
		return 1
	}
	return (total + pageSize - 1) / pageSize
}

func parseManagedUsersSortBy(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "id", nil
	}
	switch value {
	case "id", "name", "username", "provider", "enabled", "role_id", "role_name", "billing_available", "call_count", "quota_used", "failure_count", "created_at", "last_used_at", "updated_at":
		return value, nil
	default:
		return "", fmt.Errorf("sort_by 参数无效")
	}
}

func parseManagedUsersSortOrder(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "desc", nil
	}
	switch value {
	case "asc", "desc":
		return value, nil
	default:
		return "", fmt.Errorf("sort_order 参数无效")
	}
}

func filterManagedUsers(items []map[string]any, query managedUsersQuery) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	search := strings.ToLower(strings.TrimSpace(query.Search))
	provider := strings.TrimSpace(query.Provider)
	status := strings.TrimSpace(query.Status)
	for _, item := range items {
		if provider != "" && provider != "all" && util.Clean(item["provider"]) != provider {
			continue
		}
		if status == "enabled" && !util.ToBool(item["enabled"]) {
			continue
		}
		if status == "disabled" && util.ToBool(item["enabled"]) {
			continue
		}
		if search != "" && !strings.Contains(managedUserSearchText(item), search) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func sortManagedUsers(items []map[string]any, query managedUsersQuery) {
	desc := query.SortOrder == "desc"
	sort.SliceStable(items, func(i, j int) bool {
		cmp := compareManagedUsers(items[i], items[j], query.SortBy)
		if cmp == 0 {
			cmp = strings.Compare(util.Clean(items[i]["id"]), util.Clean(items[j]["id"]))
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
}

func compareManagedUsers(left, right map[string]any, sortBy string) int {
	switch sortBy {
	case "enabled":
		return compareManagedUserInts(managedUserSortBool(left, sortBy), managedUserSortBool(right, sortBy))
	case "billing_available", "call_count", "quota_used", "failure_count":
		return compareManagedUserInts(managedUserSortInt(left, sortBy), managedUserSortInt(right, sortBy))
	default:
		return strings.Compare(strings.ToLower(managedUserSortString(left, sortBy)), strings.ToLower(managedUserSortString(right, sortBy)))
	}
}

func managedUserSortString(item map[string]any, sortBy string) string {
	switch sortBy {
	case "name":
		return util.Clean(item["name"])
	case "username":
		return util.Clean(item["username"])
	case "provider":
		return util.Clean(item["provider"])
	case "role_id":
		return util.Clean(item["role_id"])
	case "role_name":
		return util.Clean(item["role_name"])
	case "created_at":
		return util.Clean(item["created_at"])
	case "last_used_at":
		return util.Clean(item["last_used_at"])
	case "updated_at":
		return util.Clean(item["updated_at"])
	default:
		return util.Clean(item["id"])
	}
}

func managedUserSortBool(item map[string]any, sortBy string) int {
	if sortBy == "enabled" && util.ToBool(item["enabled"]) {
		return 1
	}
	return 0
}

func managedUserSortInt(item map[string]any, sortBy string) int {
	if sortBy == "billing_available" {
		// 双桶视图下「可用额度」按两桶之和排序：
		// bucket_a.available + bucket_b.available。
		billing := util.StringMap(item["billing"])
		bucketA := util.StringMap(billing[util.ImageBucketA])
		bucketB := util.StringMap(billing[util.ImageBucketB])
		return util.ToInt(bucketA["available"], 0) + util.ToInt(bucketB["available"], 0)
	}
	return util.ToInt(item[sortBy], 0)
}

func compareManagedUserInts(left, right int) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func managedUserSearchText(item map[string]any) string {
	parts := []string{
		util.Clean(item["id"]),
		util.Clean(item["username"]),
		util.Clean(item["name"]),
		util.Clean(item["role_id"]),
		util.Clean(item["role_name"]),
		util.Clean(item["owner_id"]),
		util.Clean(item["owner_name"]),
		util.Clean(item["provider"]),
		util.Clean(item["linuxdo_level"]),
		util.Clean(item["session_id"]),
		util.Clean(item["session_name"]),
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func findManagedUser(items []map[string]any, id string) map[string]any {
	for _, item := range items {
		if item["id"] == id {
			return item
		}
	}
	return nil
}
