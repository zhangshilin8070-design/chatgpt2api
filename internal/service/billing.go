package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"chatgpt2api/internal/storage"
	"chatgpt2api/internal/util"
)

const (
	BillingTypeStandard     = "standard"
	BillingTypeSubscription = "subscription"

	BillingUnitImage = "image"

	BillingPeriodDaily   = "daily"
	BillingPeriodWeekly  = "weekly"
	BillingPeriodMonthly = "monthly"

	billingDocumentName = "user_billing.json"
)

// BillingDefaults 提供两个桶（bucket_a / bucket_b）的初始默认值。
// 实现方需对每个桶分别返回 billing_type、standard balance、subscription quota、subscription period。
type BillingDefaults interface {
	DefaultBucketABillingType() string
	DefaultBucketAStandardBalance() int
	DefaultBucketASubscriptionQuota() int
	DefaultBucketASubscriptionPeriod() string

	DefaultBucketBBillingType() string
	DefaultBucketBStandardBalance() int
	DefaultBucketBSubscriptionQuota() int
	DefaultBucketBSubscriptionPeriod() string
}

// BillingReference 携带本次扣费 / 退款所需的审计字段。Bucket 必填，
// 由调用方根据对外模型解析（util.BucketForModel）。
type BillingReference struct {
	Bucket         string
	Endpoint       string
	Model          string
	TaskID         string
	RequestID      string
	CredentialID   string
	CredentialName string
	ChargeKey      string
	RefundForKey   string
	OutputIndex    int
}

type BillingChargeResult struct {
	Charged        bool
	AlreadyCharged bool
	Billing        map[string]any
}

type BillingRefundResult struct {
	Refunded        bool
	AlreadyRefunded bool
	Billing         map[string]any
}

type BillingBulkAdjustmentResult struct {
	UserID     string
	Billing    map[string]any
	Adjustment map[string]any
	Error      string
}

// BillingLimitError 表示当前桶的余额或配额不足。Code 形如
// `user_balance_insufficient_<bucket>` 或 `user_quota_exceeded_<bucket>`。
type BillingLimitError struct {
	Bucket      string
	BillingType string
	Message     string
	Code        string
}

func (e BillingLimitError) Error() string {
	return e.Message
}

func (e BillingLimitError) OpenAIError() map[string]any {
	return map[string]any{
		"error": map[string]any{
			"message": e.Message,
			"type":    "insufficient_quota",
			"param":   nil,
			"code":    e.Code,
		},
	}
}

// NewBillingLimitError 构造桶级别的限额错误。bucket 必须是 bucket_a / bucket_b。
// 任何其他取值视为编程错误并 panic（Fail-Fast）。
func NewBillingLimitError(bucket, billingType string) BillingLimitError {
	if !isValidBillingBucket(bucket) {
		panic(fmt.Sprintf("invalid billing bucket: %q", bucket))
	}
	if normalizeBillingType(billingType) == BillingTypeSubscription {
		return BillingLimitError{
			Bucket:      bucket,
			BillingType: BillingTypeSubscription,
			Message:     fmt.Sprintf("user quota exceeded (%s)", bucket),
			Code:        "user_quota_exceeded_" + bucket,
		}
	}
	return BillingLimitError{
		Bucket:      bucket,
		BillingType: BillingTypeStandard,
		Message:     fmt.Sprintf("user balance insufficient (%s)", bucket),
		Code:        "user_balance_insufficient_" + bucket,
	}
}

type BillingService struct {
	mu       sync.Mutex
	store    storage.JSONDocumentBackend
	defaults BillingDefaults
	logs     *LogService

	states       map[string]map[string]any
	adjustments  []map[string]any
	transactions []map[string]any
}

// NewBillingService 构造 BillingService 实例。logs 用于在 loadLocked 阶段
// 检测到旧/新字段并存的「混合」迁移状态时记录 `legacy_billing_state_conflict`
// 警告日志；测试场景可传 nil。
func NewBillingService(backend storage.Backend, defaults BillingDefaults, logs *LogService) *BillingService {
	s := &BillingService{
		store:    jsonDocumentStoreFromBackend(backend),
		defaults: defaults,
		logs:     logs,
		states:   map[string]map[string]any{},
	}
	s.mu.Lock()
	s.loadLocked()
	s.mu.Unlock()
	return s
}

func (s *BillingService) InitializeUserDefaults(userID string) map[string]any {
	userID = strings.TrimSpace(userID)
	if s == nil || userID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = map[string]map[string]any{}
	}
	state := s.states[userID]
	changed := false
	if state == nil {
		state = defaultBillingState(userID, s.defaults)
		s.states[userID] = state
		changed = true
	} else {
		changed = normalizeBillingState(state, userID, s.defaults)
	}
	if changed {
		_ = s.saveLocked()
	}
	return publicBillingState(state)
}

func (s *BillingService) Get(userID string) map[string]any {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.stateForReadLocked(userID)
	if !ok {
		return publicBillingState(legacyBillingState(userID))
	}
	changed := false
	if normalizeBillingState(state, userID, nil) {
		changed = true
	}
	if s.resetSubscriptionIfDueLocked(state, time.Now()) {
		changed = true
	}
	if changed {
		_ = s.saveLocked()
	}
	return publicBillingState(state)
}

func (s *BillingService) GetMany(userIDs []string) map[string]map[string]any {
	out := map[string]map[string]any{}
	if len(userIDs) == 0 {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	now := time.Now()
	for _, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		state, ok := s.stateForReadLocked(userID)
		if !ok {
			out[userID] = publicBillingState(legacyBillingState(userID))
			continue
		}
		if normalizeBillingState(state, userID, nil) {
			changed = true
		}
		if s.resetSubscriptionIfDueLocked(state, now) {
			changed = true
		}
		out[userID] = publicBillingState(state)
	}
	if changed {
		_ = s.saveLocked()
	}
	return out
}

// CheckAvailable 仅检查指定 bucket 的余额或周期配额，与另一个桶无关。
func (s *BillingService) CheckAvailable(identity Identity, amount int, bucket string) error {
	if s == nil || identity.Role != AuthRoleUser || amount <= 0 {
		return nil
	}
	userID := billingUserID(identity)
	if userID == "" {
		return nil
	}
	if !isValidBillingBucket(bucket) {
		return errors.New("unsupported billing bucket")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, changed := s.ensureStateLocked(userID)
	if s.resetSubscriptionIfDueLocked(state, time.Now()) {
		changed = true
	}
	if util.ToBool(state["unlimited"]) {
		if changed {
			_ = s.saveLocked()
		}
		return nil
	}
	bucketState := bucketStateForRead(state, bucket)
	if util.ToBool(bucketState["unlimited"]) {
		if changed {
			_ = s.saveLocked()
		}
		return nil
	}
	billingType := normalizeBillingType(util.Clean(bucketState["billing_type"]))
	switch billingType {
	case BillingTypeStandard:
		standard := bucketStandardState(state, bucket)
		if availableStandardBalance(standard) < amount {
			if changed {
				_ = s.saveLocked()
			}
			return NewBillingLimitError(bucket, BillingTypeStandard)
		}
	case BillingTypeSubscription:
		subscription := bucketSubscriptionState(state, bucket)
		if availableSubscriptionQuota(subscription) < amount {
			if changed {
				_ = s.saveLocked()
			}
			return NewBillingLimitError(bucket, BillingTypeSubscription)
		}
	default:
		return fmt.Errorf("unsupported billing type: %s", billingType)
	}
	if changed {
		_ = s.saveLocked()
	}
	return nil
}

// Charge 执行扣费。ref.Bucket 必填，否则返回 `unsupported billing bucket`。
func (s *BillingService) Charge(identity Identity, amount int, ref BillingReference) error {
	if identity.Role != AuthRoleUser {
		return nil
	}
	_, err := s.ChargeUserID(billingUserID(identity), amount, ref)
	return err
}

func (s *BillingService) ChargeUserID(userID string, amount int, ref BillingReference) (BillingChargeResult, error) {
	return s.chargeUserID(strings.TrimSpace(userID), amount, ref)
}

func (s *BillingService) chargeUserID(userID string, amount int, ref BillingReference) (BillingChargeResult, error) {
	result := BillingChargeResult{}
	if s == nil || userID == "" || amount <= 0 {
		return result, nil
	}
	bucket := strings.TrimSpace(ref.Bucket)
	if !isValidBillingBucket(bucket) {
		return result, errors.New("unsupported billing bucket")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, _ := s.ensureStateLocked(userID)
	s.resetSubscriptionIfDueLocked(state, time.Now())
	if util.ToBool(state["unlimited"]) {
		_ = s.saveLocked()
		result.Billing = publicBillingState(state)
		return result, nil
	}
	chargeKey := strings.TrimSpace(ref.ChargeKey)
	if chargeKey != "" && s.hasChargeKeyLocked(userID, chargeKey, bucket) {
		result.AlreadyCharged = true
		result.Billing = publicBillingState(state)
		return result, nil
	}
	bucketState := bucketStateForRead(state, bucket)
	if util.ToBool(bucketState["unlimited"]) {
		bucketState["updated_at"] = util.NowISO()
		state["updated_at"] = util.NowISO()
		s.addTransactionLocked(map[string]any{
			"user_id":         userID,
			"bucket":          bucket,
			"billing_type":    normalizeBillingType(util.Clean(bucketState["billing_type"])),
			"unit":            BillingUnitImage,
			"action":          "charge",
			"consumed_amount": amount,
			"charge_key":      chargeKey,
			"endpoint":        ref.Endpoint,
			"model":           ref.Model,
			"task_id":         ref.TaskID,
			"request_id":      ref.RequestID,
			"output_index":    ref.OutputIndex,
		})
		result.Charged = true
		result.Billing = publicBillingState(state)
		if err := s.saveLocked(); err != nil {
			return result, err
		}
		return result, nil
	}
	billingType := normalizeBillingType(util.Clean(bucketState["billing_type"]))
	switch billingType {
	case BillingTypeStandard:
		standard := bucketStandardState(state, bucket)
		if availableStandardBalance(standard) < amount {
			return result, NewBillingLimitError(bucket, BillingTypeStandard)
		}
		standard["balance"] = intField(standard, "balance") - amount
		standard["lifetime_consumed"] = intField(standard, "lifetime_consumed") + amount
	case BillingTypeSubscription:
		subscription := bucketSubscriptionState(state, bucket)
		if availableSubscriptionQuota(subscription) < amount {
			return result, NewBillingLimitError(bucket, BillingTypeSubscription)
		}
		subscription["quota_used"] = intField(subscription, "quota_used") + amount
	default:
		return result, fmt.Errorf("unsupported billing type: %s", billingType)
	}
	bucketState["updated_at"] = util.NowISO()
	state["updated_at"] = util.NowISO()
	s.addTransactionLocked(map[string]any{
		"user_id":         userID,
		"bucket":          bucket,
		"billing_type":    billingType,
		"unit":            BillingUnitImage,
		"action":          "charge",
		"consumed_amount": amount,
		"charge_key":      chargeKey,
		"endpoint":        ref.Endpoint,
		"model":           ref.Model,
		"task_id":         ref.TaskID,
		"request_id":      ref.RequestID,
		"output_index":    ref.OutputIndex,
	})
	result.Charged = true
	result.Billing = publicBillingState(state)
	if err := s.saveLocked(); err != nil {
		return result, err
	}
	return result, nil
}

func (s *BillingService) RefundUserID(userID string, amount int, ref BillingReference) (BillingRefundResult, error) {
	result := BillingRefundResult{}
	userID = strings.TrimSpace(userID)
	if s == nil || userID == "" || amount <= 0 {
		return result, nil
	}
	bucket := strings.TrimSpace(ref.Bucket)
	if !isValidBillingBucket(bucket) {
		return result, errors.New("unsupported billing bucket")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, _ := s.ensureStateLocked(userID)
	s.resetSubscriptionIfDueLocked(state, time.Now())
	if util.ToBool(state["unlimited"]) {
		result.Billing = publicBillingState(state)
		return result, nil
	}
	refundKey := strings.TrimSpace(ref.ChargeKey)
	if refundKey != "" && s.hasRefundKeyLocked(userID, refundKey, bucket) {
		result.AlreadyRefunded = true
		result.Billing = publicBillingState(state)
		return result, nil
	}
	refundForKey := strings.TrimSpace(ref.RefundForKey)
	amount = s.refundableAmountLocked(userID, amount, refundForKey, bucket)
	if amount <= 0 {
		result.Billing = publicBillingState(state)
		return result, nil
	}
	bucketState := bucketStateForRead(state, bucket)
	billingType := normalizeBillingType(util.Clean(bucketState["billing_type"]))
	switch billingType {
	case BillingTypeStandard:
		standard := bucketStandardState(state, bucket)
		standard["balance"] = intField(standard, "balance") + amount
		standard["lifetime_consumed"] = max(0, intField(standard, "lifetime_consumed")-amount)
	case BillingTypeSubscription:
		subscription := bucketSubscriptionState(state, bucket)
		subscription["quota_used"] = max(0, intField(subscription, "quota_used")-amount)
	default:
		return result, fmt.Errorf("unsupported billing type: %s", billingType)
	}
	bucketState["updated_at"] = util.NowISO()
	state["updated_at"] = util.NowISO()
	s.addTransactionLocked(map[string]any{
		"user_id":               userID,
		"bucket":                bucket,
		"billing_type":          billingType,
		"unit":                  BillingUnitImage,
		"action":                "refund",
		"refunded_amount":       amount,
		"charge_key":            refundKey,
		"refund_for_charge_key": refundForKey,
		"endpoint":              ref.Endpoint,
		"model":                 ref.Model,
		"task_id":               ref.TaskID,
		"request_id":            ref.RequestID,
		"output_index":          ref.OutputIndex,
	})
	result.Refunded = true
	result.Billing = publicBillingState(state)
	if err := s.saveLocked(); err != nil {
		return result, err
	}
	return result, nil
}

// ApplyAdjustment 仅作用于 body["bucket"] 指定的桶；before / after 视图始终是双桶完整状态。
func (s *BillingService) ApplyAdjustment(userID string, operator Identity, body map[string]any) (map[string]any, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("user id is required")
	}
	adjustmentType := strings.TrimSpace(util.Clean(body["type"]))
	if adjustmentType == "" {
		return nil, errors.New("adjustment type is required")
	}
	bucket := strings.TrimSpace(util.Clean(body["bucket"]))
	if !isValidBillingBucket(bucket) {
		return nil, errors.New("unsupported billing bucket")
	}
	reason := strings.TrimSpace(util.Clean(body["reason"]))

	s.mu.Lock()
	defer s.mu.Unlock()
	state, _ := s.ensureStateLocked(userID)
	now := time.Now()
	s.resetSubscriptionIfDueLocked(state, now)
	before := publicBillingState(state)
	amount := adjustmentAmount(body)

	if err := s.applyAdjustmentLocked(state, bucket, adjustmentType, amount, body, now); err != nil {
		return nil, err
	}

	bucketState := bucketStateForRead(state, bucket)
	bucketState["billing_type"] = normalizeBillingType(util.Clean(bucketState["billing_type"]))
	bucketState["updated_at"] = util.NowISO()
	state["updated_at"] = util.NowISO()
	after := publicBillingState(state)
	adjustment := s.addAdjustmentLocked(userID, operator, bucket, adjustmentType, amount, reason, before, after)
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return map[string]any{"billing": after, "adjustment": adjustment}, nil
}

func (s *BillingService) ApplyBulkAdjustment(userIDs []string, operator Identity, body map[string]any) ([]BillingBulkAdjustmentResult, error) {
	if s == nil {
		return nil, errors.New("billing service is unavailable")
	}
	ids := uniqueBillingUserIDs(userIDs)
	if len(ids) == 0 {
		return nil, errors.New("user ids are required")
	}
	if len(ids) > 500 {
		return nil, errors.New("cannot adjust more than 500 users at once")
	}
	adjustmentType := strings.TrimSpace(util.Clean(body["type"]))
	if adjustmentType == "" {
		return nil, errors.New("adjustment type is required")
	}
	if !isSupportedBillingAdjustmentType(adjustmentType) {
		return nil, fmt.Errorf("unsupported billing adjustment type: %s", adjustmentType)
	}
	bucket := strings.TrimSpace(util.Clean(body["bucket"]))
	if !isValidBillingBucket(bucket) {
		return nil, errors.New("unsupported billing bucket")
	}
	amount := adjustmentAmount(body)
	if billingAdjustmentNeedsPositiveAmount(adjustmentType) && amount <= 0 {
		return nil, errors.New("amount must be greater than 0")
	}
	reason := strings.TrimSpace(util.Clean(body["reason"]))

	results := make([]BillingBulkAdjustmentResult, 0, len(ids))
	changed := false
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, userID := range ids {
		result := BillingBulkAdjustmentResult{UserID: userID}
		state, _ := s.ensureStateLocked(userID)
		s.resetSubscriptionIfDueLocked(state, now)
		before := publicBillingState(state)
		if err := s.applyAdjustmentLocked(state, bucket, adjustmentType, amount, body, now); err != nil {
			result.Error = err.Error()
			result.Billing = publicBillingState(state)
			results = append(results, result)
			continue
		}
		bucketState := bucketStateForRead(state, bucket)
		bucketState["billing_type"] = normalizeBillingType(util.Clean(bucketState["billing_type"]))
		bucketState["updated_at"] = util.NowISO()
		state["updated_at"] = util.NowISO()
		after := publicBillingState(state)
		adjustment := s.addAdjustmentLocked(userID, operator, bucket, adjustmentType, amount, reason, before, after)
		result.Billing = after
		result.Adjustment = adjustment
		results = append(results, result)
		changed = true
	}
	if changed {
		if err := s.saveLocked(); err != nil {
			return results, err
		}
	}
	return results, nil
}

func (s *BillingService) applyAdjustmentLocked(state map[string]any, bucket, adjustmentType string, amount int, body map[string]any, now time.Time) error {
	bucketState := bucketStateForRead(state, bucket)
	switch adjustmentType {
	case "set_unlimited":
		bucketState["unlimited"] = util.ToBool(body["unlimited"])
	case "switch_to_standard":
		bucketState["billing_type"] = BillingTypeStandard
		if _, ok := body["balance"]; ok {
			if err := setBucketStandardBalance(state, bucket, util.ToInt(body["balance"], 0)); err != nil {
				return err
			}
		} else if _, ok := body["amount"]; ok {
			if err := setBucketStandardBalance(state, bucket, amount); err != nil {
				return err
			}
		}
	case "switch_to_subscription":
		rawQuotaLimit, ok := body["quota_limit"]
		if !ok {
			return errors.New("quota limit is required")
		}
		quotaLimit := util.ToInt(rawQuotaLimit, 0)
		if quotaLimit < 0 {
			return errors.New("quota limit cannot be negative")
		}
		period := normalizeBillingPeriod(util.Clean(body["quota_period"]))
		if period == "" {
			return errors.New("quota period must be daily, weekly, or monthly")
		}
		bucketState["billing_type"] = BillingTypeSubscription
		subscription := bucketSubscriptionState(state, bucket)
		subscription["quota_limit"] = quotaLimit
		subscription["quota_period"] = period
		resetSubscriptionPeriod(subscription, now)
	case "set_balance":
		if err := setBucketStandardBalance(state, bucket, firstIntValue(body, "balance", "amount")); err != nil {
			return err
		}
	case "increase_balance":
		if amount <= 0 {
			return errors.New("amount must be greater than 0")
		}
		standard := bucketStandardState(state, bucket)
		standard["balance"] = intField(standard, "balance") + amount
	case "decrease_balance":
		if amount <= 0 {
			return errors.New("amount must be greater than 0")
		}
		standard := bucketStandardState(state, bucket)
		if intField(standard, "balance")-amount < 0 {
			return errors.New("balance cannot be negative")
		}
		standard["balance"] = intField(standard, "balance") - amount
	case "set_quota_limit":
		limit := firstIntValue(body, "quota_limit", "amount")
		if limit < 0 {
			return errors.New("quota limit cannot be negative")
		}
		bucketSubscriptionState(state, bucket)["quota_limit"] = limit
	case "set_quota_period":
		period := normalizeBillingPeriod(util.Clean(body["quota_period"]))
		if period == "" {
			return errors.New("quota period must be daily, weekly, or monthly")
		}
		subscription := bucketSubscriptionState(state, bucket)
		subscription["quota_period"] = period
		resetSubscriptionPeriod(subscription, now)
	case "reset_quota":
		resetSubscriptionPeriod(bucketSubscriptionState(state, bucket), now)
	case "clear_quota_used":
		bucketSubscriptionState(state, bucket)["quota_used"] = 0
	case "increase_quota":
		if amount <= 0 {
			return errors.New("amount must be greater than 0")
		}
		subscription := bucketSubscriptionState(state, bucket)
		subscription["manual_delta"] = intField(subscription, "manual_delta") + amount
	case "decrease_quota":
		if amount <= 0 {
			return errors.New("amount must be greater than 0")
		}
		subscription := bucketSubscriptionState(state, bucket)
		if availableSubscriptionQuota(subscription) < amount {
			return errors.New("quota decrease cannot exceed remaining quota")
		}
		subscription["manual_delta"] = intField(subscription, "manual_delta") - amount
	default:
		return fmt.Errorf("unsupported billing adjustment type: %s", adjustmentType)
	}
	return nil
}

func (s *BillingService) addAdjustmentLocked(userID string, operator Identity, bucket, adjustmentType string, amount int, reason string, before, after map[string]any) map[string]any {
	bucketAfter := util.StringMap(after[bucket])
	billingTypeAfter := ""
	if bucketAfter != nil {
		billingTypeAfter = util.Clean(bucketAfter["type"])
	}
	adjustment := map[string]any{
		"id":            "billing_adj_" + util.NewHex(18),
		"user_id":       userID,
		"operator_id":   billingUserID(operator),
		"operator_name": operator.Name,
		"bucket":        bucket,
		"billing_type":  billingTypeAfter,
		"type":          adjustmentType,
		"amount":        amount,
		"reason":        reason,
		"before":        before,
		"after":         after,
		"created_at":    util.NowISO(),
	}
	s.adjustments = append(s.adjustments, adjustment)
	s.addTransactionLocked(map[string]any{
		"user_id":       userID,
		"bucket":        bucket,
		"billing_type":  billingTypeAfter,
		"unit":          BillingUnitImage,
		"action":        "adjust",
		"adjustment_id": adjustment["id"],
		"adjustment":    adjustmentType,
		"amount":        amount,
	})
	return adjustment
}

func (s *BillingService) ListAdjustments(userID string, limit int) []map[string]any {
	userID = strings.TrimSpace(userID)
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0, min(limit, len(s.adjustments)))
	for i := len(s.adjustments) - 1; i >= 0 && len(out) < limit; i-- {
		item := s.adjustments[i]
		if userID != "" && util.Clean(item["user_id"]) != userID {
			continue
		}
		out = append(out, copyBillingMap(item))
	}
	return out
}

func (s *BillingService) ensureStateLocked(userID string) (map[string]any, bool) {
	if s.states == nil {
		s.states = map[string]map[string]any{}
	}
	state := s.states[userID]
	if state == nil {
		state = legacyBillingState(userID)
		s.states[userID] = state
		return state, true
	}
	changed := normalizeBillingState(state, userID, nil)
	return state, changed
}

func (s *BillingService) stateForReadLocked(userID string) (map[string]any, bool) {
	if s.states == nil {
		return nil, false
	}
	state := s.states[userID]
	if state == nil {
		return nil, false
	}
	return state, true
}

func (s *BillingService) resetSubscriptionIfDueLocked(state map[string]any, now time.Time) bool {
	changed := false
	for _, bucket := range []string{util.ImageBucketA, util.ImageBucketB} {
		bucketState := bucketStateForRead(state, bucket)
		if normalizeBillingType(util.Clean(bucketState["billing_type"])) != BillingTypeSubscription {
			continue
		}
		subscription := bucketSubscriptionState(state, bucket)
		endsAt := parseBillingTime(util.Clean(subscription["quota_period_ends_at"]))
		if !endsAt.IsZero() && now.Before(endsAt) {
			continue
		}
		resetSubscriptionPeriod(subscription, now)
		bucketState["updated_at"] = util.NowISO()
		state["updated_at"] = util.NowISO()
		s.addTransactionLocked(map[string]any{
			"user_id":      util.Clean(state["user_id"]),
			"bucket":       bucket,
			"billing_type": BillingTypeSubscription,
			"unit":         BillingUnitImage,
			"action":       "reset_subscription_period",
		})
		changed = true
	}
	return changed
}

func (s *BillingService) loadLocked() {
	raw := loadStoredJSON(s.store, billingDocumentName)
	doc, _ := raw.(map[string]any)
	s.states = map[string]map[string]any{}
	migrated := false
	if states, ok := doc["states"].(map[string]any); ok {
		for userID, value := range states {
			state, ok := value.(map[string]any)
			if !ok {
				continue
			}
			if s.migrateLegacyBillingStateLocked(state, userID) {
				migrated = true
			}
			normalizeBillingState(state, userID, s.defaults)
			s.states[userID] = state
		}
	}
	s.adjustments = util.AsMapSlice(doc["adjustments"])
	s.transactions = util.AsMapSlice(doc["transactions"])
	if s.adjustments == nil {
		s.adjustments = []map[string]any{}
	}
	if s.transactions == nil {
		s.transactions = []map[string]any{}
	}
	if migrated {
		_ = s.saveLocked()
	}
}

// migrateLegacyBillingStateLocked 把 v1 单桶 state 迁移到 v2 双桶 state。
// 仅在 loadLocked 阶段调用一次；返回是否对入参 state 做了修改。
//
// 规则：
//   - 已迁移（同时含 bucket_a 与 bucket_b 两个 map）：直接返回 false。
//   - 纯旧版（顶层 standard / subscription / billing_type 且无双桶字段）：
//     把 {billing_type, standard, subscription, unlimited, updated_at} 复制到 bucket_a，
//     用 DefaultBucketB* 初始化 bucket_b，并删除顶层旧字段（保留 user_id / 顶层 unlimited / updated_at）。
//   - 新旧混合（同时含旧字段与至少一个新桶字段）：丢弃旧字段，记录
//     `legacy_billing_state_conflict` 警告日志，并把缺失的桶按默认值补齐。
func (s *BillingService) migrateLegacyBillingStateLocked(state map[string]any, userID string) bool {
	if state == nil {
		return false
	}
	_, hasBucketA := state[util.ImageBucketA].(map[string]any)
	_, hasBucketB := state[util.ImageBucketB].(map[string]any)
	hasAnyNewBucket := hasBucketA || hasBucketB

	legacyKeys := legacyBillingStateKeys(state)
	hasLegacy := len(legacyKeys) > 0

	switch {
	case hasBucketA && hasBucketB && !hasLegacy:
		// 已是规范双桶形态，无需迁移。
		return false
	case !hasAnyNewBucket && hasLegacy:
		// 纯旧版：把顶层 standard / subscription / billing_type 整体迁入 bucket_a。
		bucketA := map[string]any{
			"billing_type": normalizeBillingType(util.Clean(state["billing_type"])),
			"unlimited":    util.ToBool(state["unlimited"]),
			"updated_at":   util.Clean(state["updated_at"]),
		}
		if standard, ok := state["standard"].(map[string]any); ok {
			bucketA["standard"] = copyBillingMap(standard)
		}
		if subscription, ok := state["subscription"].(map[string]any); ok {
			bucketA["subscription"] = copyBillingMap(subscription)
		}
		state[util.ImageBucketA] = bucketA
		state[util.ImageBucketB] = defaultBucketState(util.ImageBucketB, s.defaults)
		for _, key := range legacyKeys {
			delete(state, key)
		}
		return true
	case hasAnyNewBucket && hasLegacy:
		// 混合：以新字段为准，丢弃旧字段并记录警告。
		if s.logs != nil {
			_ = s.logs.Add("legacy_billing_state_conflict", map[string]any{
				"module":       "billing",
				"user_id":      userID,
				"dropped_keys": legacyKeys,
				"has_bucket_a": hasBucketA,
				"has_bucket_b": hasBucketB,
			})
		}
		for _, key := range legacyKeys {
			delete(state, key)
		}
		return true
	default:
		return false
	}
}

// legacyBillingStateKeys 返回当前 state 中残留的旧版顶层字段名。
// 顶层 unlimited / updated_at 不属于旧字段（双桶形态仍保留）。
func legacyBillingStateKeys(state map[string]any) []string {
	if state == nil {
		return nil
	}
	keys := make([]string, 0, 3)
	for _, key := range []string{"billing_type", "standard", "subscription"} {
		if _, ok := state[key]; ok {
			keys = append(keys, key)
		}
	}
	return keys
}

func (s *BillingService) saveLocked() error {
	states := map[string]any{}
	keys := make([]string, 0, len(s.states))
	for key := range s.states {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		states[key] = s.states[key]
	}
	doc := map[string]any{
		"states":       states,
		"adjustments":  s.adjustments,
		"transactions": s.transactions,
		"updated_at":   util.NowISO(),
	}
	return saveStoredJSON(s.store, billingDocumentName, doc)
}

// addTransactionLocked 把一条交易流水写入持久化日志。调用方必须在 item 中
// 设置 `bucket` 字段；缺失视为编程错误并 panic（Fail-Fast）。
func (s *BillingService) addTransactionLocked(item map[string]any) {
	if item == nil {
		return
	}
	if !isValidBillingBucket(util.Clean(item["bucket"])) {
		panic(fmt.Sprintf("billing transaction missing valid bucket: %#v", item))
	}
	item = copyBillingMap(item)
	if util.Clean(item["id"]) == "" {
		item["id"] = "billing_txn_" + util.NewHex(18)
	}
	if util.Clean(item["created_at"]) == "" {
		item["created_at"] = util.NowISO()
	}
	s.transactions = append(s.transactions, item)
	if len(s.transactions) > 5000 {
		s.transactions = append([]map[string]any(nil), s.transactions[len(s.transactions)-5000:]...)
	}
}

func (s *BillingService) hasChargeKeyLocked(userID, chargeKey, bucket string) bool {
	userID = strings.TrimSpace(userID)
	chargeKey = strings.TrimSpace(chargeKey)
	bucket = strings.TrimSpace(bucket)
	if userID == "" || chargeKey == "" || bucket == "" {
		return false
	}
	for i := len(s.transactions) - 1; i >= 0; i-- {
		txn := s.transactions[i]
		if util.Clean(txn["user_id"]) == userID &&
			util.Clean(txn["charge_key"]) == chargeKey &&
			util.Clean(txn["bucket"]) == bucket {
			return true
		}
	}
	return false
}

func (s *BillingService) hasRefundKeyLocked(userID, refundKey, bucket string) bool {
	userID = strings.TrimSpace(userID)
	refundKey = strings.TrimSpace(refundKey)
	bucket = strings.TrimSpace(bucket)
	if userID == "" || refundKey == "" || bucket == "" {
		return false
	}
	for i := len(s.transactions) - 1; i >= 0; i-- {
		txn := s.transactions[i]
		if util.Clean(txn["user_id"]) == userID &&
			util.Clean(txn["charge_key"]) == refundKey &&
			util.Clean(txn["bucket"]) == bucket &&
			util.Clean(txn["action"]) == "refund" {
			return true
		}
	}
	return false
}

func (s *BillingService) refundableAmountLocked(userID string, amount int, chargeKey, bucket string) int {
	amount = max(0, amount)
	chargeKey = strings.TrimSpace(chargeKey)
	bucket = strings.TrimSpace(bucket)
	if amount <= 0 || chargeKey == "" || bucket == "" {
		return amount
	}
	charged := 0
	refunded := 0
	for _, txn := range s.transactions {
		if util.Clean(txn["user_id"]) != userID || util.Clean(txn["bucket"]) != bucket {
			continue
		}
		switch util.Clean(txn["action"]) {
		case "charge":
			if util.Clean(txn["charge_key"]) == chargeKey {
				charged += util.ToInt(txn["consumed_amount"], 0)
			}
		case "refund":
			if util.Clean(txn["refund_for_charge_key"]) == chargeKey {
				refunded += util.ToInt(txn["refunded_amount"], 0)
			}
		}
	}
	return min(amount, max(0, charged-refunded))
}
