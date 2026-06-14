package service

import (
	"errors"
	"strings"
	"testing"

	"chatgpt2api/internal/storage"
	"chatgpt2api/internal/util"
)

// testBillingDefaults 是 BillingDefaults 的测试桩，按桶 A / 桶 B 分别返回默认值。
//
// 字段命名上，桶 A 的字段沿用历史名称（billingType / standardBalance / ...），
// 便于既有测试沿用单桶视角的语义；桶 B 字段则以 bucketB 前缀显式区分。
type testBillingDefaults struct {
	billingType        string
	standardBalance    int
	subscriptionQuota  int
	subscriptionPeriod string

	bucketBBillingType        string
	bucketBStandardBalance    int
	bucketBSubscriptionQuota  int
	bucketBSubscriptionPeriod string
}

func (d testBillingDefaults) DefaultBucketABillingType() string        { return d.billingType }
func (d testBillingDefaults) DefaultBucketAStandardBalance() int       { return d.standardBalance }
func (d testBillingDefaults) DefaultBucketASubscriptionQuota() int     { return d.subscriptionQuota }
func (d testBillingDefaults) DefaultBucketASubscriptionPeriod() string { return d.subscriptionPeriod }

func (d testBillingDefaults) DefaultBucketBBillingType() string        { return d.bucketBBillingType }
func (d testBillingDefaults) DefaultBucketBStandardBalance() int       { return d.bucketBStandardBalance }
func (d testBillingDefaults) DefaultBucketBSubscriptionQuota() int     { return d.bucketBSubscriptionQuota }
func (d testBillingDefaults) DefaultBucketBSubscriptionPeriod() string { return d.bucketBSubscriptionPeriod }

// newTestBillingService 构造一个空的 BillingService 测试夹具，并初始化
// 两个常用测试用户 alice / bob 的双桶默认状态。
func newTestBillingService(t *testing.T, defaults testBillingDefaults) *BillingService {
	t.Helper()
	backend := newTestStorageBackend(t)
	svc := NewBillingService(backend, defaults, NewLogService(backend))
	svc.InitializeUserDefaults("alice")
	svc.InitializeUserDefaults("bob")
	return svc
}

// newTestBillingServiceAt 构造一个不预初始化用户的 BillingService 夹具，
// 用于持久化层、迁移路径等需要观察从零状态出发的用例。
func newTestBillingServiceAt(t *testing.T, defaults testBillingDefaults) *BillingService {
	t.Helper()
	backend := newTestStorageBackend(t)
	return NewBillingService(backend, defaults, NewLogService(backend))
}

func billingTestUser(id string) Identity {
	return Identity{ID: id, Name: id, Role: AuthRoleUser, CredentialID: "cred-" + id}
}

// 双桶单元测试由 task 2.6 引入（TestBillingDualBucket* 系列）。
// 旧的单桶断言无法直接迁移到双桶 API，已随接口重构一并删除。

// bucketAvailable 读取 publicBillingState 输出中指定桶的 `available` 字段。
// 桶级 available 在 standard / subscription 两种类型下都已统一暴露在该字段。
func bucketAvailable(t *testing.T, billing map[string]any, bucket string) int {
	t.Helper()
	if billing == nil {
		t.Fatalf("billing state is nil")
	}
	bucketView, ok := billing[bucket].(map[string]any)
	if !ok || bucketView == nil {
		t.Fatalf("bucket %s missing in billing state: %#v", bucket, billing)
	}
	value, ok := bucketView["available"].(int)
	if !ok {
		t.Fatalf("bucket %s available is not int: %#v", bucket, bucketView["available"])
	}
	return value
}

// assertBucketAvailable 断言指定桶的剩余余额。
func assertBucketAvailable(t *testing.T, svc *BillingService, userID, bucket string, expected int) {
	t.Helper()
	got := bucketAvailable(t, svc.Get(userID), bucket)
	if got != expected {
		t.Fatalf("bucket %s available for %s = %d, want %d", bucket, userID, got, expected)
	}
}

// dualBucketStandardDefaults 返回两桶都为 standard 类型、初始余额可独立设置的默认值。
func dualBucketStandardDefaults(bucketABalance, bucketBBalance int) testBillingDefaults {
	return testBillingDefaults{
		billingType:               BillingTypeStandard,
		standardBalance:           bucketABalance,
		subscriptionQuota:         0,
		subscriptionPeriod:        BillingPeriodMonthly,
		bucketBBillingType:        BillingTypeStandard,
		bucketBStandardBalance:    bucketBBalance,
		bucketBSubscriptionQuota:  0,
		bucketBSubscriptionPeriod: BillingPeriodMonthly,
	}
}

// TestBillingDualBucketChargeIsolation 验证两桶扣费互不影响：一桶扣费不会
// 影响另一桶余额；当扣费失败时两桶状态保持一致。
//
// _Requirements: 2.2, 2.3, 2.4
func TestBillingDualBucketChargeIsolation(t *testing.T) {
	svc := newTestBillingService(t, dualBucketStandardDefaults(100, 50))
	identity := billingTestUser("alice")

	// 桶 A 扣 30 → 桶 A 余额 70，桶 B 不变。
	if _, err := svc.ChargeUserID(identity.ID, 30, BillingReference{Bucket: util.ImageBucketA}); err != nil {
		t.Fatalf("charge bucket_a: %v", err)
	}
	assertBucketAvailable(t, svc, identity.ID, util.ImageBucketA, 70)
	assertBucketAvailable(t, svc, identity.ID, util.ImageBucketB, 50)

	// 桶 B 扣 40 → 桶 B 余额 10，桶 A 仍为 70。
	if _, err := svc.ChargeUserID(identity.ID, 40, BillingReference{Bucket: util.ImageBucketB}); err != nil {
		t.Fatalf("charge bucket_b: %v", err)
	}
	assertBucketAvailable(t, svc, identity.ID, util.ImageBucketA, 70)
	assertBucketAvailable(t, svc, identity.ID, util.ImageBucketB, 10)

	// 桶 A 扣 80 应失败（余额不足），状态不应回流到桶 B。
	_, err := svc.ChargeUserID(identity.ID, 80, BillingReference{Bucket: util.ImageBucketA})
	if err == nil {
		t.Fatalf("charge bucket_a 80 expected BillingLimitError, got nil")
	}
	var limitErr BillingLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("expected BillingLimitError, got %T: %v", err, err)
	}
	if limitErr.Bucket != util.ImageBucketA {
		t.Fatalf("limit error bucket = %q, want %q", limitErr.Bucket, util.ImageBucketA)
	}
	assertBucketAvailable(t, svc, identity.ID, util.ImageBucketA, 70)
	assertBucketAvailable(t, svc, identity.ID, util.ImageBucketB, 10)
}

// TestBillingDualBucketChargeKeyDoesNotCrossBuckets 验证 charge_key 是
// 桶级幂等键：相同 key 在不同桶下应分别记账，互不抵消；refund 也按桶查找。
//
// _Requirements: 2.2, 2.4, 2.9
func TestBillingDualBucketChargeKeyDoesNotCrossBuckets(t *testing.T) {
	svc := newTestBillingService(t, dualBucketStandardDefaults(100, 100))
	user := "alice"

	// 桶 A 扣 30，charge_key=k1。
	res, err := svc.ChargeUserID(user, 30, BillingReference{Bucket: util.ImageBucketA, ChargeKey: "k1"})
	if err != nil {
		t.Fatalf("charge bucket_a k1: %v", err)
	}
	if !res.Charged || res.AlreadyCharged {
		t.Fatalf("bucket_a k1 first charge result = %#v", res)
	}
	assertBucketAvailable(t, svc, user, util.ImageBucketA, 70)
	assertBucketAvailable(t, svc, user, util.ImageBucketB, 100)

	// 桶 B 用同 charge_key=k1 扣 30：必须独立记账（不被桶 A 的幂等命中）。
	res, err = svc.ChargeUserID(user, 30, BillingReference{Bucket: util.ImageBucketB, ChargeKey: "k1"})
	if err != nil {
		t.Fatalf("charge bucket_b k1: %v", err)
	}
	if !res.Charged || res.AlreadyCharged {
		t.Fatalf("bucket_b k1 charge with same key should be applied: %#v", res)
	}
	assertBucketAvailable(t, svc, user, util.ImageBucketA, 70)
	assertBucketAvailable(t, svc, user, util.ImageBucketB, 70)

	// 重复扣桶 A 的 k1：应识别为已扣费的幂等命中，不再变更余额。
	res, err = svc.ChargeUserID(user, 30, BillingReference{Bucket: util.ImageBucketA, ChargeKey: "k1"})
	if err != nil {
		t.Fatalf("charge bucket_a k1 idempotent: %v", err)
	}
	if !res.AlreadyCharged || res.Charged {
		t.Fatalf("bucket_a k1 second charge should be idempotent: %#v", res)
	}
	assertBucketAvailable(t, svc, user, util.ImageBucketA, 70)
	assertBucketAvailable(t, svc, user, util.ImageBucketB, 70)

	// 退款桶 A 的 k1：refundForKey=k1 + 唯一的 refund chargeKey=k1-refund。
	refund, err := svc.RefundUserID(user, 30, BillingReference{
		Bucket:       util.ImageBucketA,
		ChargeKey:    "k1-refund",
		RefundForKey: "k1",
	})
	if err != nil {
		t.Fatalf("refund bucket_a k1: %v", err)
	}
	if !refund.Refunded || refund.AlreadyRefunded {
		t.Fatalf("bucket_a k1 refund result = %#v", refund)
	}
	assertBucketAvailable(t, svc, user, util.ImageBucketA, 100)
	assertBucketAvailable(t, svc, user, util.ImageBucketB, 70)

	// 对桶 A 不存在的 charge_key=k2 退款：应为 no-op（refundable 计算返回 0）。
	refund, err = svc.RefundUserID(user, 30, BillingReference{
		Bucket:       util.ImageBucketA,
		ChargeKey:    "k2-refund",
		RefundForKey: "k2",
	})
	if err != nil {
		t.Fatalf("refund bucket_a unknown key: %v", err)
	}
	if refund.Refunded {
		t.Fatalf("refund for unknown key should be no-op: %#v", refund)
	}
	assertBucketAvailable(t, svc, user, util.ImageBucketA, 100)
	assertBucketAvailable(t, svc, user, util.ImageBucketB, 70)
}

// TestBillingDualBucketLimitErrorCarriesBucketSuffix 验证 BillingLimitError 在
// standard / subscription 两种 billing_type 下都把 bucket 后缀写入 Code，
// 并在错误对象上保留 Bucket 字段。
//
// _Requirements: 2.7
func TestBillingDualBucketLimitErrorCarriesBucketSuffix(t *testing.T) {
	svc := newTestBillingService(t, dualBucketStandardDefaults(0, 0))
	identity := billingTestUser("alice")

	// 桶 A standard 余额 0 → user_balance_insufficient_bucket_a。
	err := svc.CheckAvailable(identity, 1, util.ImageBucketA)
	if err == nil {
		t.Fatalf("CheckAvailable bucket_a expected BillingLimitError, got nil")
	}
	var limitErr BillingLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("expected BillingLimitError, got %T: %v", err, err)
	}
	if limitErr.Bucket != util.ImageBucketA {
		t.Fatalf("standard limit bucket = %q, want %q", limitErr.Bucket, util.ImageBucketA)
	}
	if limitErr.Code != "user_balance_insufficient_bucket_a" {
		t.Fatalf("standard limit code = %q, want user_balance_insufficient_bucket_a", limitErr.Code)
	}

	// 把桶 B 切换为 subscription 类型且配额 0。
	if _, err := svc.ApplyAdjustment("alice", billingTestUser("admin"), map[string]any{
		"type":         "switch_to_subscription",
		"bucket":       util.ImageBucketB,
		"quota_limit":  0,
		"quota_period": BillingPeriodMonthly,
	}); err != nil {
		t.Fatalf("switch_to_subscription bucket_b: %v", err)
	}

	// 桶 B subscription 配额 0 → user_quota_exceeded_bucket_b。
	err = svc.CheckAvailable(identity, 1, util.ImageBucketB)
	if err == nil {
		t.Fatalf("CheckAvailable bucket_b expected BillingLimitError, got nil")
	}
	limitErr = BillingLimitError{}
	if !errors.As(err, &limitErr) {
		t.Fatalf("expected BillingLimitError, got %T: %v", err, err)
	}
	if limitErr.Bucket != util.ImageBucketB {
		t.Fatalf("subscription limit bucket = %q, want %q", limitErr.Bucket, util.ImageBucketB)
	}
	if limitErr.Code != "user_quota_exceeded_bucket_b" {
		t.Fatalf("subscription limit code = %q, want user_quota_exceeded_bucket_b", limitErr.Code)
	}
}

// TestBillingDualBucketAdjustmentRequiresBucket 验证 ApplyAdjustment 必须
// 在 body 中显式声明 bucket，且只接受 bucket_a / bucket_b。
//
// _Requirements: 2.2
func TestBillingDualBucketAdjustmentRequiresBucket(t *testing.T) {
	svc := newTestBillingService(t, dualBucketStandardDefaults(10, 10))
	operator := billingTestUser("admin")

	cases := []struct {
		name string
		body map[string]any
	}{
		{
			name: "missing bucket",
			body: map[string]any{
				"type":   "increase_balance",
				"amount": 5,
			},
		},
		{
			name: "unknown bucket",
			body: map[string]any{
				"type":   "increase_balance",
				"amount": 5,
				"bucket": "bucket_c",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.ApplyAdjustment("alice", operator, tc.body)
			if err == nil {
				t.Fatalf("ApplyAdjustment expected error, got nil")
			}
			if !strings.Contains(err.Error(), "unsupported billing bucket") {
				t.Fatalf("ApplyAdjustment error = %q, want contain `unsupported billing bucket`", err.Error())
			}
		})
	}

	// 合法 bucket 应当成功并写入对应桶。
	out, err := svc.ApplyAdjustment("alice", operator, map[string]any{
		"type":   "increase_balance",
		"amount": 5,
		"bucket": util.ImageBucketA,
	})
	if err != nil {
		t.Fatalf("ApplyAdjustment with valid bucket: %v", err)
	}
	if out == nil {
		t.Fatalf("ApplyAdjustment output is nil")
	}
	assertBucketAvailable(t, svc, "alice", util.ImageBucketA, 15)
	assertBucketAvailable(t, svc, "alice", util.ImageBucketB, 10)
}

// TestBillingDualBucketCheckAvailableValidatesBucket 验证 CheckAvailable 对
// 非法 bucket 取值（空串、未知值）返回 `unsupported billing bucket` 错误，
// 而非静默通过。
//
// _Requirements: 2.2, 2.3
func TestBillingDualBucketCheckAvailableValidatesBucket(t *testing.T) {
	svc := newTestBillingService(t, dualBucketStandardDefaults(10, 10))
	identity := billingTestUser("alice")

	cases := []struct {
		name   string
		bucket string
	}{
		{name: "unknown bucket", bucket: "bucket_c"},
		{name: "empty bucket", bucket: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.CheckAvailable(identity, 1, tc.bucket)
			if err == nil {
				t.Fatalf("CheckAvailable expected error for bucket=%q, got nil", tc.bucket)
			}
			if !strings.Contains(err.Error(), "unsupported billing bucket") {
				t.Fatalf("CheckAvailable error = %q, want contain `unsupported billing bucket`", err.Error())
			}
		})
	}
}

// seedBillingDocumentForTest 在 BillingService 构造之前把一份原始 billing
// 文档直接写入 storage backend，用于驱动 loadLocked 阶段的迁移路径。
func seedBillingDocumentForTest(t *testing.T, backend storage.Backend, doc map[string]any) {
	t.Helper()
	store := jsonDocumentStoreFromBackend(backend)
	if store == nil {
		t.Fatalf("backend does not implement JSONDocumentBackend")
	}
	if err := saveStoredJSON(store, billingDocumentName, doc); err != nil {
		t.Fatalf("seed billing document: %v", err)
	}
}

// migrationDualBucketDefaults 给迁移测试准备一组带显式 bucket_b 默认值的
// BillingDefaults，便于断言「迁移后 bucket_b 使用桶 B 默认值」。
func migrationDualBucketDefaults() testBillingDefaults {
	return testBillingDefaults{
		billingType:               BillingTypeStandard,
		standardBalance:           1000,
		subscriptionQuota:         0,
		subscriptionPeriod:        BillingPeriodMonthly,
		bucketBBillingType:        BillingTypeStandard,
		bucketBStandardBalance:    99,
		bucketBSubscriptionQuota:  0,
		bucketBSubscriptionPeriod: BillingPeriodMonthly,
	}
}

// nestedMap 在嵌套 map[string]any 中按 path 取子 map，缺失时调用 t.Fatalf。
func nestedMap(t *testing.T, parent map[string]any, path ...string) map[string]any {
	t.Helper()
	current := parent
	for i, key := range path {
		child, ok := current[key].(map[string]any)
		if !ok || child == nil {
			t.Fatalf("missing nested map at path %v (index %d, key %q): %#v", path, i, key, parent)
		}
		current = child
	}
	return current
}

// countConflictLogs 返回 backend 中「legacy_billing_state_conflict」条目数。
func countConflictLogs(t *testing.T, backend storage.Backend) int {
	t.Helper()
	logs := NewLogService(backend)
	return len(logs.Search(LogQuery{Summary: "legacy_billing_state_conflict", Limit: 50}))
}

// TestBillingMigrationLegacyToDual 验证 v1 单桶 state 在 loadLocked 阶段被
// 一次性迁移到 v2 双桶 state：旧字段并入 bucket_a，bucket_b 由 DefaultBucketB*
// 初始化，顶层旧字段删除，并通过持久化使二次构造保持等价。
//
// _Requirements: 10.1, 10.2
func TestBillingMigrationLegacyToDual(t *testing.T) {
	backend := newTestStorageBackend(t)
	seedBillingDocumentForTest(t, backend, map[string]any{
		"states": map[string]any{
			"u1": map[string]any{
				"user_id":      "u1",
				"billing_type": BillingTypeStandard,
				"unlimited":    false,
				"standard": map[string]any{
					"balance":           42,
					"lifetime_consumed": 7,
				},
				"subscription": map[string]any{
					"quota_limit":  100,
					"quota_used":   5,
					"quota_period": BillingPeriodMonthly,
				},
				"updated_at": "2025-01-01T00:00:00Z",
			},
		},
	})

	defaults := migrationDualBucketDefaults()
	svc := NewBillingService(backend, defaults, NewLogService(backend))

	state, ok := svc.states["u1"]
	if !ok || state == nil {
		t.Fatalf("state for u1 missing after migration")
	}
	for _, key := range []string{"billing_type", "standard", "subscription"} {
		if _, exists := state[key]; exists {
			t.Fatalf("legacy top-level key %q should be removed: %#v", key, state)
		}
	}

	bucketA := nestedMap(t, state, util.ImageBucketA)
	if got := util.Clean(bucketA["billing_type"]); got != BillingTypeStandard {
		t.Fatalf("bucket_a.billing_type = %q, want %q", got, BillingTypeStandard)
	}
	standardA := nestedMap(t, bucketA, "standard")
	if got := intField(standardA, "balance"); got != 42 {
		t.Fatalf("bucket_a.standard.balance = %d, want 42", got)
	}
	if got := intField(standardA, "lifetime_consumed"); got != 7 {
		t.Fatalf("bucket_a.standard.lifetime_consumed = %d, want 7", got)
	}
	subscriptionA := nestedMap(t, bucketA, "subscription")
	if got := intField(subscriptionA, "quota_limit"); got != 100 {
		t.Fatalf("bucket_a.subscription.quota_limit = %d, want 100", got)
	}

	bucketB := nestedMap(t, state, util.ImageBucketB)
	standardB := nestedMap(t, bucketB, "standard")
	if got := intField(standardB, "balance"); got != 99 {
		t.Fatalf("bucket_b.standard.balance = %d, want 99 (DefaultBucketBStandardBalance)", got)
	}

	public := svc.Get("u1")
	if _, ok := public[util.ImageBucketA].(map[string]any); !ok {
		t.Fatalf("Get(u1) missing bucket_a in dual-bucket view: %#v", public)
	}
	if _, ok := public[util.ImageBucketB].(map[string]any); !ok {
		t.Fatalf("Get(u1) missing bucket_b in dual-bucket view: %#v", public)
	}

	// 二次构造同一 backend 上的 BillingService：迁移已落盘，新实例不应再次重置桶 B。
	svc2 := NewBillingService(backend, defaults, NewLogService(backend))
	state2 := svc2.states["u1"]
	if state2 == nil {
		t.Fatalf("state for u1 missing after second load")
	}
	standardA2 := nestedMap(t, state2, util.ImageBucketA, "standard")
	if got := intField(standardA2, "balance"); got != 42 {
		t.Fatalf("second load bucket_a.standard.balance = %d, want 42 (migration must be idempotent)", got)
	}
	standardB2 := nestedMap(t, state2, util.ImageBucketB, "standard")
	if got := intField(standardB2, "balance"); got != 99 {
		t.Fatalf("second load bucket_b.standard.balance = %d, want 99 (no reset on second pass)", got)
	}
	for _, key := range []string{"billing_type", "standard", "subscription"} {
		if _, exists := state2[key]; exists {
			t.Fatalf("second load: legacy top-level key %q reappeared: %#v", key, state2)
		}
	}
}

// TestBillingMigrationAlreadyDualNoOp 验证已经是 v2 双桶形态的 state 不会
// 被迁移逻辑改写，且不会产生 legacy_billing_state_conflict 日志。
//
// _Requirements: 10.3
func TestBillingMigrationAlreadyDualNoOp(t *testing.T) {
	backend := newTestStorageBackend(t)
	seedBillingDocumentForTest(t, backend, map[string]any{
		"states": map[string]any{
			"u2": map[string]any{
				"user_id":   "u2",
				"unlimited": false,
				util.ImageBucketA: map[string]any{
					"billing_type": BillingTypeStandard,
					"unlimited":    false,
					"standard": map[string]any{
						"balance":           33,
						"lifetime_consumed": 11,
					},
					"subscription": map[string]any{
						"quota_limit":             0,
						"quota_used":              0,
						"manual_delta":            0,
						"quota_period":            BillingPeriodMonthly,
						"quota_period_started_at": "2025-01-01T00:00:00Z",
						"quota_period_ends_at":    "2099-01-01T00:00:00Z",
					},
					"updated_at": "2025-01-01T00:00:00Z",
				},
				util.ImageBucketB: map[string]any{
					"billing_type": BillingTypeStandard,
					"unlimited":    false,
					"standard": map[string]any{
						"balance":           77,
						"lifetime_consumed": 4,
					},
					"subscription": map[string]any{
						"quota_limit":             0,
						"quota_used":              0,
						"manual_delta":            0,
						"quota_period":            BillingPeriodMonthly,
						"quota_period_started_at": "2025-01-01T00:00:00Z",
						"quota_period_ends_at":    "2099-01-01T00:00:00Z",
					},
					"updated_at": "2025-01-01T00:00:00Z",
				},
				"updated_at": "2025-01-01T00:00:00Z",
			},
		},
	})

	defaults := migrationDualBucketDefaults()
	svc := NewBillingService(backend, defaults, NewLogService(backend))

	state := svc.states["u2"]
	if state == nil {
		t.Fatalf("state for u2 missing")
	}
	for _, key := range []string{"billing_type", "standard", "subscription"} {
		if _, exists := state[key]; exists {
			t.Fatalf("state[%q] must not exist for already-dual state: %#v", key, state)
		}
	}
	standardA := nestedMap(t, state, util.ImageBucketA, "standard")
	if got := intField(standardA, "balance"); got != 33 {
		t.Fatalf("bucket_a.standard.balance = %d, want 33 (preserved)", got)
	}
	if got := intField(standardA, "lifetime_consumed"); got != 11 {
		t.Fatalf("bucket_a.standard.lifetime_consumed = %d, want 11 (preserved)", got)
	}
	standardB := nestedMap(t, state, util.ImageBucketB, "standard")
	if got := intField(standardB, "balance"); got != 77 {
		t.Fatalf("bucket_b.standard.balance = %d, want 77 (preserved, NOT reset to defaults)", got)
	}
	if got := intField(standardB, "lifetime_consumed"); got != 4 {
		t.Fatalf("bucket_b.standard.lifetime_consumed = %d, want 4 (preserved)", got)
	}

	if conflicts := countConflictLogs(t, backend); conflicts != 0 {
		t.Fatalf("legacy_billing_state_conflict logs = %d, want 0 for already-dual state", conflicts)
	}
}

// TestBillingMigrationMixedDropsLegacy 验证当 state 同时包含旧顶层字段
// 与新桶字段时，迁移逻辑丢弃旧字段、保留 bucket_a 不变、用默认值补齐
// bucket_b，并写入 legacy_billing_state_conflict 警告日志。
//
// _Requirements: 10.6
func TestBillingMigrationMixedDropsLegacy(t *testing.T) {
	backend := newTestStorageBackend(t)
	seedBillingDocumentForTest(t, backend, map[string]any{
		"states": map[string]any{
			"u3": map[string]any{
				"user_id":   "u3",
				"unlimited": false,
				// 新字段：仅 bucket_a 已就绪，bucket_b 缺失。
				util.ImageBucketA: map[string]any{
					"billing_type": BillingTypeStandard,
					"unlimited":    false,
					"standard": map[string]any{
						"balance":           500,
						"lifetime_consumed": 12,
					},
					"subscription": map[string]any{
						"quota_limit":             0,
						"quota_used":              0,
						"manual_delta":            0,
						"quota_period":            BillingPeriodMonthly,
						"quota_period_started_at": "2025-01-01T00:00:00Z",
						"quota_period_ends_at":    "2099-01-01T00:00:00Z",
					},
					"updated_at": "2025-01-01T00:00:00Z",
				},
				// 旧字段：与 bucket_a 取值故意不一致，验证「新字段为准」。
				"billing_type": BillingTypeSubscription,
				"standard": map[string]any{
					"balance":           1, // 不应被复制到 bucket_a
					"lifetime_consumed": 1,
				},
				"subscription": map[string]any{
					"quota_limit":  9999,
					"quota_used":   0,
					"quota_period": BillingPeriodMonthly,
				},
				"updated_at": "2025-01-01T00:00:00Z",
			},
		},
	})

	defaults := migrationDualBucketDefaults()
	svc := NewBillingService(backend, defaults, NewLogService(backend))

	state := svc.states["u3"]
	if state == nil {
		t.Fatalf("state for u3 missing")
	}
	for _, key := range []string{"billing_type", "standard", "subscription"} {
		if _, exists := state[key]; exists {
			t.Fatalf("legacy top-level key %q must be dropped in mixed state: %#v", key, state)
		}
	}

	// bucket_a 必须保留，新字段获胜（旧的 standard.balance=1 不应替换 500）。
	standardA := nestedMap(t, state, util.ImageBucketA, "standard")
	if got := intField(standardA, "balance"); got != 500 {
		t.Fatalf("bucket_a.standard.balance = %d, want 500 (new field wins, legacy dropped)", got)
	}
	if got := intField(standardA, "lifetime_consumed"); got != 12 {
		t.Fatalf("bucket_a.standard.lifetime_consumed = %d, want 12", got)
	}
	bucketA := nestedMap(t, state, util.ImageBucketA)
	if got := util.Clean(bucketA["billing_type"]); got != BillingTypeStandard {
		t.Fatalf("bucket_a.billing_type = %q, want %q (legacy `subscription` must be dropped)", got, BillingTypeStandard)
	}

	// bucket_b 由默认值补齐。
	standardB := nestedMap(t, state, util.ImageBucketB, "standard")
	if got := intField(standardB, "balance"); got != 99 {
		t.Fatalf("bucket_b.standard.balance = %d, want 99 (DefaultBucketBStandardBalance)", got)
	}

	// 日志：summary=legacy_billing_state_conflict，detail.module=billing，
	// detail.user_id=u3，dropped_keys 含三个旧 key，has_bucket_a=true、has_bucket_b=false。
	logs := NewLogService(backend)
	conflicts := logs.Search(LogQuery{Summary: "legacy_billing_state_conflict", Limit: 50})
	if len(conflicts) != 1 {
		t.Fatalf("legacy_billing_state_conflict logs = %d, want 1; entries = %#v", len(conflicts), conflicts)
	}
	entry := conflicts[0]
	detail := util.StringMap(entry["detail"])
	if got := util.Clean(detail["module"]); got != "billing" {
		t.Fatalf("conflict log detail.module = %q, want billing", got)
	}
	if got := util.Clean(detail["user_id"]); got != "u3" {
		t.Fatalf("conflict log detail.user_id = %q, want u3", got)
	}
	if got, want := util.ToBool(detail["has_bucket_a"]), true; got != want {
		t.Fatalf("conflict log detail.has_bucket_a = %v, want %v", got, want)
	}
	if got, want := util.ToBool(detail["has_bucket_b"]), false; got != want {
		t.Fatalf("conflict log detail.has_bucket_b = %v, want %v", got, want)
	}
	dropped := util.AsStringSlice(detail["dropped_keys"])
	wantDropped := map[string]struct{}{
		"billing_type": {},
		"standard":     {},
		"subscription": {},
	}
	if len(dropped) != len(wantDropped) {
		t.Fatalf("conflict log detail.dropped_keys = %v, want keys %v", dropped, wantDropped)
	}
	for _, key := range dropped {
		if _, ok := wantDropped[key]; !ok {
			t.Fatalf("conflict log detail.dropped_keys contains unexpected key %q (full = %v)", key, dropped)
		}
		delete(wantDropped, key)
	}
	if len(wantDropped) != 0 {
		t.Fatalf("conflict log detail.dropped_keys missing keys: %v (got %v)", wantDropped, dropped)
	}
}
