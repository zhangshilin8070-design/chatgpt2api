package service

import (
	"errors"
	"strings"
	"time"

	"chatgpt2api/internal/util"
)

// defaultBucketState 构造一个桶的初始状态对象，沿用现有 standard / subscription 嵌套形态。
func defaultBucketState(bucket string, defaults BillingDefaults) map[string]any {
	now := time.Now()
	period := defaultBucketSubscriptionPeriod(bucket, defaults)
	started, ends := billingPeriodBounds(period, now)
	return map[string]any{
		"billing_type": defaultBucketBillingType(bucket, defaults),
		"unlimited":    false,
		"standard": map[string]any{
			"balance":           max(0, defaultBucketStandardBalance(bucket, defaults)),
			"lifetime_consumed": 0,
		},
		"subscription": map[string]any{
			"quota_limit":             max(0, defaultBucketSubscriptionQuota(bucket, defaults)),
			"quota_used":              0,
			"manual_delta":            0,
			"quota_period":            period,
			"quota_period_started_at": started.Format(time.RFC3339),
			"quota_period_ends_at":    ends.Format(time.RFC3339),
		},
		"updated_at": util.NowISO(),
	}
}

// defaultBillingState 返回双桶用户的默认 state。
func defaultBillingState(userID string, defaults BillingDefaults) map[string]any {
	return map[string]any{
		"user_id":    userID,
		"unlimited":  false,
		"bucket_a":   defaultBucketState(util.ImageBucketA, defaults),
		"bucket_b":   defaultBucketState(util.ImageBucketB, defaults),
		"updated_at": util.NowISO(),
	}
}

// legacyBillingState 表示当前用户尚无任何持久化记录时的占位 state。
// 不沿用启动时的 defaults，避免新用户在调度参数变化后被错误初始化。
func legacyBillingState(userID string) map[string]any {
	return defaultBillingState(userID, nil)
}

// normalizeBillingState 把 state 整理为双桶规范形态，并返回是否发生改动。
// 旧字段（顶层 standard / subscription / billing_type）的迁移由迁移阶段处理，
// 此处只做默认补齐与字段去脏。
func normalizeBillingState(state map[string]any, userID string, defaults BillingDefaults) bool {
	changed := false
	if util.Clean(state["user_id"]) != userID {
		state["user_id"] = userID
		changed = true
	}
	if _, ok := state["unlimited"]; !ok {
		state["unlimited"] = false
		changed = true
	}
	for _, bucket := range []string{util.ImageBucketA, util.ImageBucketB} {
		if normalizeBucketState(state, bucket, defaults) {
			changed = true
		}
	}
	if util.Clean(state["updated_at"]) == "" {
		state["updated_at"] = util.NowISO()
		changed = true
	}
	return changed
}

// normalizeBucketState 把指定桶的状态整理为规范形态。返回是否发生改动。
func normalizeBucketState(state map[string]any, bucket string, defaults BillingDefaults) bool {
	if !isValidBillingBucket(bucket) {
		return false
	}
	bucketState, ok := state[bucket].(map[string]any)
	if !ok || bucketState == nil {
		state[bucket] = defaultBucketState(bucket, defaults)
		return true
	}
	changed := false
	billingType := normalizeBillingType(util.Clean(bucketState["billing_type"]))
	if billingType == "" {
		billingType = defaultBucketBillingType(bucket, defaults)
	}
	if bucketState["billing_type"] != billingType {
		bucketState["billing_type"] = billingType
		changed = true
	}
	if _, ok := bucketState["unlimited"]; !ok {
		bucketState["unlimited"] = false
		changed = true
	}
	if _, ok := bucketState["standard"].(map[string]any); !ok {
		bucketState["standard"] = map[string]any{
			"balance":           max(0, defaultBucketStandardBalance(bucket, defaults)),
			"lifetime_consumed": 0,
		}
		changed = true
	}
	standard := bucketStandardState(state, bucket)
	for key := range map[string]struct{}{"balance": {}, "lifetime_consumed": {}} {
		value := max(0, intField(standard, key))
		if standard[key] != value {
			standard[key] = value
			changed = true
		}
	}
	if _, ok := standard["balance_reserved"]; ok {
		delete(standard, "balance_reserved")
		changed = true
	}
	if _, ok := bucketState["subscription"].(map[string]any); !ok {
		period := defaultBucketSubscriptionPeriod(bucket, defaults)
		started, ends := billingPeriodBounds(period, time.Now())
		bucketState["subscription"] = map[string]any{
			"quota_limit":             max(0, defaultBucketSubscriptionQuota(bucket, defaults)),
			"quota_used":              0,
			"manual_delta":            0,
			"quota_period":            period,
			"quota_period_started_at": started.Format(time.RFC3339),
			"quota_period_ends_at":    ends.Format(time.RFC3339),
		}
		changed = true
	}
	subscription := bucketSubscriptionState(state, bucket)
	for key := range map[string]struct{}{"quota_limit": {}, "quota_used": {}} {
		value := max(0, intField(subscription, key))
		if subscription[key] != value {
			subscription[key] = value
			changed = true
		}
	}
	if manualDelta := intField(subscription, "manual_delta"); subscription["manual_delta"] != manualDelta {
		subscription["manual_delta"] = manualDelta
		changed = true
	}
	if _, ok := subscription["quota_reserved"]; ok {
		delete(subscription, "quota_reserved")
		changed = true
	}
	period := normalizeBillingPeriod(util.Clean(subscription["quota_period"]))
	if period == "" {
		period = defaultBucketSubscriptionPeriod(bucket, defaults)
	}
	if subscription["quota_period"] != period {
		subscription["quota_period"] = period
		changed = true
	}
	if parseBillingTime(util.Clean(subscription["quota_period_started_at"])).IsZero() ||
		parseBillingTime(util.Clean(subscription["quota_period_ends_at"])).IsZero() {
		resetSubscriptionPeriod(subscription, time.Now())
		changed = true
	}
	if util.Clean(bucketState["updated_at"]) == "" {
		bucketState["updated_at"] = util.NowISO()
		changed = true
	}
	return changed
}

// publicBillingState 输出对外可见的双桶视图。
func publicBillingState(state map[string]any) map[string]any {
	out := map[string]any{
		"user_id":    util.Clean(state["user_id"]),
		"unlimited":  util.ToBool(state["unlimited"]),
		"bucket_a":   publicBucketState(state, util.ImageBucketA),
		"bucket_b":   publicBucketState(state, util.ImageBucketB),
		"updated_at": state["updated_at"],
	}
	return out
}

// publicBucketState 输出单个桶的对外视图，结构与旧的单桶 publicBillingState 一致。
func publicBucketState(state map[string]any, bucket string) map[string]any {
	bucketState, _ := state[bucket].(map[string]any)
	if bucketState == nil {
		return nil
	}
	billingType := normalizeBillingType(util.Clean(bucketState["billing_type"]))
	unlimited := util.ToBool(bucketState["unlimited"])
	out := map[string]any{
		"type":         billingType,
		"unit":         BillingUnitImage,
		"unlimited":    unlimited,
		"available":    0,
		"standard":     nil,
		"subscription": nil,
		"updated_at":   bucketState["updated_at"],
	}
	switch billingType {
	case BillingTypeStandard:
		standard := copyBillingMap(bucketStandardState(state, bucket))
		available := availableStandardBalance(standard)
		out["available"] = available
		standard["available_balance"] = available
		out["standard"] = standard
	case BillingTypeSubscription:
		subscription := copyBillingMap(bucketSubscriptionState(state, bucket))
		available := availableSubscriptionQuota(subscription)
		out["available"] = available
		subscription["remaining_quota"] = available
		out["subscription"] = subscription
	}
	if unlimited {
		out["limit_state"] = "unlimited"
	} else if util.ToInt(out["available"], 0) > 0 {
		out["limit_state"] = "ok"
	} else {
		out["limit_state"] = "insufficient"
	}
	return out
}

// bucketStateForRead 返回（必要时初始化）指定桶的 state map。
func bucketStateForRead(state map[string]any, bucket string) map[string]any {
	bucketState, ok := state[bucket].(map[string]any)
	if !ok || bucketState == nil {
		bucketState = defaultBucketState(bucket, nil)
		state[bucket] = bucketState
	}
	return bucketState
}

func bucketStandardState(state map[string]any, bucket string) map[string]any {
	bucketState := bucketStateForRead(state, bucket)
	standard, ok := bucketState["standard"].(map[string]any)
	if !ok || standard == nil {
		standard = map[string]any{}
		bucketState["standard"] = standard
	}
	return standard
}

func bucketSubscriptionState(state map[string]any, bucket string) map[string]any {
	bucketState := bucketStateForRead(state, bucket)
	subscription, ok := bucketState["subscription"].(map[string]any)
	if !ok || subscription == nil {
		subscription = map[string]any{}
		bucketState["subscription"] = subscription
	}
	return subscription
}

func availableStandardBalance(standard map[string]any) int {
	return max(0, intField(standard, "balance"))
}

func availableSubscriptionQuota(subscription map[string]any) int {
	return max(0, intField(subscription, "quota_limit")+intField(subscription, "manual_delta")-intField(subscription, "quota_used"))
}

func setBucketStandardBalance(state map[string]any, bucket string, balance int) error {
	if balance < 0 {
		return errors.New("balance cannot be negative")
	}
	standard := bucketStandardState(state, bucket)
	standard["balance"] = balance
	return nil
}

func resetSubscriptionPeriod(subscription map[string]any, now time.Time) {
	period := normalizeBillingPeriod(util.Clean(subscription["quota_period"]))
	if period == "" {
		period = BillingPeriodMonthly
	}
	started, ends := billingPeriodBounds(period, now)
	subscription["quota_used"] = 0
	subscription["manual_delta"] = 0
	subscription["quota_period"] = period
	subscription["quota_period_started_at"] = started.Format(time.RFC3339)
	subscription["quota_period_ends_at"] = ends.Format(time.RFC3339)
}

func billingPeriodBounds(period string, now time.Time) (time.Time, time.Time) {
	loc := now.Location()
	switch normalizeBillingPeriod(period) {
	case BillingPeriodDaily:
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		return start, start.AddDate(0, 0, 1)
	case BillingPeriodWeekly:
		weekdayOffset := (int(now.Weekday()) + 6) % 7
		day := now.AddDate(0, 0, -weekdayOffset)
		start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
		return start, start.AddDate(0, 0, 7)
	default:
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		return start, start.AddDate(0, 1, 0)
	}
}

func parseBillingTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t
		}
	}
	return time.Time{}
}

func billingUserID(identity Identity) string {
	if owner := util.Clean(identity.OwnerID); owner != "" {
		return owner
	}
	return util.Clean(identity.ID)
}

func normalizeBillingType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case BillingTypeSubscription:
		return BillingTypeSubscription
	case "", BillingTypeStandard:
		return BillingTypeStandard
	default:
		return ""
	}
}

func normalizeBillingPeriod(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case BillingPeriodDaily, BillingPeriodWeekly, BillingPeriodMonthly:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func isValidBillingBucket(bucket string) bool {
	switch strings.TrimSpace(bucket) {
	case util.ImageBucketA, util.ImageBucketB:
		return true
	default:
		return false
	}
}

func defaultBucketBillingType(bucket string, defaults BillingDefaults) string {
	if defaults == nil {
		return BillingTypeStandard
	}
	var raw string
	switch bucket {
	case util.ImageBucketA:
		raw = defaults.DefaultBucketABillingType()
	case util.ImageBucketB:
		raw = defaults.DefaultBucketBBillingType()
	}
	if value := normalizeBillingType(raw); value != "" {
		return value
	}
	return BillingTypeStandard
}

func defaultBucketSubscriptionPeriod(bucket string, defaults BillingDefaults) string {
	if defaults == nil {
		return BillingPeriodMonthly
	}
	var raw string
	switch bucket {
	case util.ImageBucketA:
		raw = defaults.DefaultBucketASubscriptionPeriod()
	case util.ImageBucketB:
		raw = defaults.DefaultBucketBSubscriptionPeriod()
	}
	if value := normalizeBillingPeriod(raw); value != "" {
		return value
	}
	return BillingPeriodMonthly
}

func defaultBucketStandardBalance(bucket string, defaults BillingDefaults) int {
	if defaults == nil {
		return 0
	}
	switch bucket {
	case util.ImageBucketA:
		return defaults.DefaultBucketAStandardBalance()
	case util.ImageBucketB:
		return defaults.DefaultBucketBStandardBalance()
	}
	return 0
}

func defaultBucketSubscriptionQuota(bucket string, defaults BillingDefaults) int {
	if defaults == nil {
		return 0
	}
	switch bucket {
	case util.ImageBucketA:
		return defaults.DefaultBucketASubscriptionQuota()
	case util.ImageBucketB:
		return defaults.DefaultBucketBSubscriptionQuota()
	}
	return 0
}

func intField(item map[string]any, key string) int {
	return util.ToInt(item[key], 0)
}

func firstIntValue(item map[string]any, keys ...string) int {
	for _, key := range keys {
		if value, ok := item[key]; ok {
			return util.ToInt(value, 0)
		}
	}
	return 0
}

func adjustmentAmount(item map[string]any) int {
	return firstIntValue(item, "amount", "balance", "quota_limit")
}

func billingAdjustmentNeedsPositiveAmount(adjustmentType string) bool {
	switch adjustmentType {
	case "increase_balance", "decrease_balance", "increase_quota", "decrease_quota":
		return true
	default:
		return false
	}
}

func isSupportedBillingAdjustmentType(adjustmentType string) bool {
	switch adjustmentType {
	case "set_unlimited",
		"switch_to_standard",
		"switch_to_subscription",
		"set_balance",
		"increase_balance",
		"decrease_balance",
		"set_quota_limit",
		"set_quota_period",
		"reset_quota",
		"clear_quota_used",
		"increase_quota",
		"decrease_quota":
		return true
	default:
		return false
	}
}

func uniqueBillingUserIDs(userIDs []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		out = append(out, userID)
	}
	return out
}

func copyBillingMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		if child, ok := value.(map[string]any); ok {
			out[key] = copyBillingMap(child)
		} else {
			out[key] = value
		}
	}
	return out
}
