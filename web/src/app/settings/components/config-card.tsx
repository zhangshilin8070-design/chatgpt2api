"use client";

import type { ReactNode } from "react";
import {
  CircleHelp,
  LoaderCircle,
  PlugZap,
  Save,
  Settings2,
} from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { testProxy, type BillingPeriod, type BillingType, type ProxyTestResult } from "@/lib/api";
import { cn } from "@/lib/utils";

import { useSettingsStore } from "../store";
import { SettingsCard, settingsInputClassName } from "./settings-ui";

const configSectionClassName = "flex flex-col gap-3";
const configFieldClassName = "min-w-0 gap-1.5";

function ConfigTip({ content }: { content: string }) {
  return (
    <span
      aria-label={content}
      title={content}
      className="inline-flex size-5 shrink-0 items-center justify-center rounded-full text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
    >
      <CircleHelp className="size-4" />
    </span>
  );
}

function SectionHeading({
  action,
  tip,
  title,
}: {
  action?: ReactNode;
  tip: string;
  title: string;
}) {
  return (
    <div className="flex min-w-0 flex-wrap items-center justify-between gap-2">
      <div className="flex min-w-0 items-center gap-1.5">
        <h3 className="truncate text-sm leading-6 font-semibold text-foreground">
          {title}
        </h3>
        <ConfigTip content={tip} />
      </div>
      {action ? (
        <div className="flex w-full shrink-0 sm:w-auto sm:justify-end">
          {action}
        </div>
      ) : null}
    </div>
  );
}

function ConfigFieldLabel({
  children,
  htmlFor,
}: {
  children: ReactNode;
  htmlFor: string;
}) {
  return (
    <FieldLabel htmlFor={htmlFor} className="leading-6">
      {children}
    </FieldLabel>
  );
}

function NumberInputWithUnit({
  id,
  max,
  min,
  onChange,
  placeholder,
  unit,
  value,
}: {
  id: string;
  max?: number;
  min?: number;
  onChange: (value: string) => void;
  placeholder: string;
  unit: string;
  value: number | string;
}) {
  return (
    <div className="relative min-w-0">
      <Input
        id={id}
        type="number"
        min={min}
        max={max}
        step={1}
        inputMode="numeric"
        value={String(value)}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder}
        className={cn(settingsInputClassName, "pr-12")}
      />
      <span className="pointer-events-none absolute top-1/2 right-3 -translate-y-1/2 text-xs font-medium text-muted-foreground">
        {unit}
      </span>
    </div>
  );
}

function ConfigOption({
  checked,
  label,
  onCheckedChange,
}: {
  checked: boolean;
  label: string;
  onCheckedChange: (checked: boolean) => void;
}) {
  return (
    <label className="flex min-h-10 min-w-0 items-center gap-2.5 rounded-[12px] border border-border/70 bg-background/75 px-3 py-2 text-sm font-medium text-foreground">
      <Checkbox
        checked={checked}
        onCheckedChange={(value) => onCheckedChange(Boolean(value))}
      />
      <span className="min-w-0 leading-5">{label}</span>
    </label>
  );
}

function BillingBucketDefaults({
  title,
  billingTypeId,
  billingType,
  setBillingType,
  standardBalanceId,
  standardBalance,
  setStandardBalance,
  subscriptionQuotaId,
  subscriptionQuota,
  setSubscriptionQuota,
  subscriptionPeriodId,
  subscriptionPeriod,
  setSubscriptionPeriod,
}: {
  title: string;
  billingTypeId: string;
  billingType: string;
  setBillingType: (value: BillingType) => void;
  standardBalanceId: string;
  standardBalance: number | string;
  setStandardBalance: (value: string) => void;
  subscriptionQuotaId: string;
  subscriptionQuota: number | string;
  setSubscriptionQuota: (value: string) => void;
  subscriptionPeriodId: string;
  subscriptionPeriod: string;
  setSubscriptionPeriod: (value: BillingPeriod) => void;
}) {
  return (
    <div className="flex flex-col gap-3 rounded-xl border border-border/70 bg-muted/20 p-3">
      <div className="text-sm font-semibold text-foreground">{title}</div>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field className={configFieldClassName}>
          <ConfigFieldLabel htmlFor={billingTypeId}>计费类型</ConfigFieldLabel>
          <Select
            value={billingType}
            onValueChange={(value) =>
              setBillingType(value === "subscription" ? "subscription" : "standard")
            }
          >
            <SelectTrigger id={billingTypeId} className={settingsInputClassName}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="standard">标准余额制</SelectItem>
              <SelectItem value="subscription">订阅配额制</SelectItem>
            </SelectContent>
          </Select>
        </Field>
        {billingType === "standard" ? (
          <Field className={configFieldClassName}>
            <ConfigFieldLabel htmlFor={standardBalanceId}>标准余额</ConfigFieldLabel>
            <NumberInputWithUnit
              id={standardBalanceId}
              min={0}
              value={standardBalance}
              onChange={setStandardBalance}
              placeholder="0"
              unit="点"
            />
          </Field>
        ) : (
          <>
            <Field className={configFieldClassName}>
              <ConfigFieldLabel htmlFor={subscriptionQuotaId}>订阅配额</ConfigFieldLabel>
              <NumberInputWithUnit
                id={subscriptionQuotaId}
                min={0}
                value={subscriptionQuota}
                onChange={setSubscriptionQuota}
                placeholder="0"
                unit="点"
              />
            </Field>
            <Field className={configFieldClassName}>
              <ConfigFieldLabel htmlFor={subscriptionPeriodId}>订阅周期</ConfigFieldLabel>
              <Select
                value={subscriptionPeriod}
                onValueChange={(value) => {
                  if (value === "daily" || value === "weekly" || value === "monthly") {
                    setSubscriptionPeriod(value);
                  }
                }}
              >
                <SelectTrigger id={subscriptionPeriodId} className={settingsInputClassName}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="daily">每日</SelectItem>
                  <SelectItem value="weekly">每周</SelectItem>
                  <SelectItem value="monthly">每月</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </>
        )}
      </div>
    </div>
  );
}

export function ConfigCard() {
  const [isTestingProxy, setIsTestingProxy] = useState(false);
  const [proxyTestResult, setProxyTestResult] =
    useState<ProxyTestResult | null>(null);
  const config = useSettingsStore((state) => state.config);
  const isLoadingConfig = useSettingsStore((state) => state.isLoadingConfig);
  const isSavingConfig = useSettingsStore((state) => state.isSavingConfig);
  const setRefreshAccountIntervalMinute = useSettingsStore(
    (state) => state.setRefreshAccountIntervalMinute,
  );
  const setImageTaskTimeoutSeconds = useSettingsStore(
    (state) => state.setImageTaskTimeoutSeconds,
  );
  const setUserDefaultConcurrentLimit = useSettingsStore(
    (state) => state.setUserDefaultConcurrentLimit,
  );
  const setUserDefaultRpmLimit = useSettingsStore(
    (state) => state.setUserDefaultRpmLimit,
  );
  const setDefaultBucketABillingType = useSettingsStore(
    (state) => state.setDefaultBucketABillingType,
  );
  const setDefaultBucketAStandardBalance = useSettingsStore(
    (state) => state.setDefaultBucketAStandardBalance,
  );
  const setDefaultBucketASubscriptionQuota = useSettingsStore(
    (state) => state.setDefaultBucketASubscriptionQuota,
  );
  const setDefaultBucketASubscriptionPeriod = useSettingsStore(
    (state) => state.setDefaultBucketASubscriptionPeriod,
  );
  const setDefaultBucketBBillingType = useSettingsStore(
    (state) => state.setDefaultBucketBBillingType,
  );
  const setDefaultBucketBStandardBalance = useSettingsStore(
    (state) => state.setDefaultBucketBStandardBalance,
  );
  const setDefaultBucketBSubscriptionQuota = useSettingsStore(
    (state) => state.setDefaultBucketBSubscriptionQuota,
  );
  const setDefaultBucketBSubscriptionPeriod = useSettingsStore(
    (state) => state.setDefaultBucketBSubscriptionPeriod,
  );
  const setAutoPreferBucketBModel = useSettingsStore(
    (state) => state.setAutoPreferBucketBModel,
  );
  const setImageRetentionDays = useSettingsStore(
    (state) => state.setImageRetentionDays,
  );
  const setImageStorageLimitMb = useSettingsStore(
    (state) => state.setImageStorageLimitMb,
  );
  const setAutoRemoveInvalidAccounts = useSettingsStore(
    (state) => state.setAutoRemoveInvalidAccounts,
  );
  const setAutoRemoveRateLimitedAccounts = useSettingsStore(
    (state) => state.setAutoRemoveRateLimitedAccounts,
  );
  const setProxy = useSettingsStore((state) => state.setProxy);
  const setBaseUrl = useSettingsStore((state) => state.setBaseUrl);
  const setRegistrationEnabled = useSettingsStore(
    (state) => state.setRegistrationEnabled,
  );
  const saveConfig = useSettingsStore((state) => state.saveConfig);
  const defaultBucketABillingType = config?.default_bucket_a_billing_type || "standard";
  const defaultBucketBBillingType = config?.default_bucket_b_billing_type || "standard";

  const handleTestProxy = async () => {
    const candidate = String(config?.proxy || "").trim();
    if (!candidate) {
      toast.error("请先填写代理地址");
      return;
    }
    setIsTestingProxy(true);
    setProxyTestResult(null);
    try {
      const data = await testProxy(candidate);
      setProxyTestResult(data.result);
      if (data.result.ok) {
        toast.success(
          `代理可用（${data.result.latency_ms} ms，HTTP ${data.result.status}）`,
        );
      } else {
        toast.error(`代理不可用：${data.result.error ?? "未知错误"}`);
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "测试代理失败");
    } finally {
      setIsTestingProxy(false);
    }
  };

  if (isLoadingConfig) {
    return (
      <SettingsCard
        icon={Settings2}
        title="系统配置"
        description="调整账号刷新、代理和图片任务。"
      >
        <div className="flex items-center justify-center py-10">
          <LoaderCircle className="size-5 animate-spin text-muted-foreground" />
        </div>
      </SettingsCard>
    );
  }

  return (
    <SettingsCard
      icon={Settings2}
      title="系统配置"
      description="调整账号刷新、代理和图片任务。"
      action={
        <Button
          size="lg"
          onClick={() => void saveConfig()}
          disabled={isSavingConfig}
        >
          {isSavingConfig ? (
            <LoaderCircle data-icon="inline-start" className="animate-spin" />
          ) : (
            <Save data-icon="inline-start" />
          )}
          保存
        </Button>
      }
    >
      <div className="flex flex-col gap-5">
        <section className={configSectionClassName}>
          <SectionHeading
            title="基础参数"
            tip="账号刷新间隔单位分钟；图片访问地址是图片结果访问前缀；任务超时时间单位秒；图片自动清理会删除指定天数前的本地图片。"
          />
          <div className="grid gap-3 sm:grid-cols-2">
            <Field className={configFieldClassName}>
              <ConfigFieldLabel htmlFor="settings-refresh-interval">
                账号刷新间隔
              </ConfigFieldLabel>
              <NumberInputWithUnit
                id="settings-refresh-interval"
                min={1}
                value={config?.refresh_account_interval_minute || ""}
                onChange={setRefreshAccountIntervalMinute}
                placeholder="5"
                unit="分钟"
              />
            </Field>
            <Field className={configFieldClassName}>
              <ConfigFieldLabel htmlFor="settings-base-url">
                图片访问地址
              </ConfigFieldLabel>
              <Input
                id="settings-base-url"
                value={String(config?.base_url || "")}
                onChange={(event) => setBaseUrl(event.target.value)}
                placeholder="https://example.com"
                className={settingsInputClassName}
              />
            </Field>
            <Field className={configFieldClassName}>
              <ConfigFieldLabel htmlFor="settings-image-retention-days">
                图片自动清理
              </ConfigFieldLabel>
              <NumberInputWithUnit
                id="settings-image-retention-days"
                min={1}
                value={config?.image_retention_days || ""}
                onChange={setImageRetentionDays}
                placeholder="30"
                unit="天"
              />
            </Field>
            <Field className={configFieldClassName}>
              <ConfigFieldLabel htmlFor="settings-image-storage-limit-mb">
                图片容量上限
              </ConfigFieldLabel>
              <NumberInputWithUnit
                id="settings-image-storage-limit-mb"
                min={0}
                value={config?.image_storage_limit_mb ?? ""}
                onChange={setImageStorageLimitMb}
                placeholder="0"
                unit="MB"
              />
            </Field>
            <Field className={configFieldClassName}>
              <ConfigFieldLabel htmlFor="settings-image-task-timeout-seconds">
                任务超时时间
              </ConfigFieldLabel>
              <NumberInputWithUnit
                id="settings-image-task-timeout-seconds"
                min={30}
                max={3600}
                value={config?.image_task_timeout_seconds || ""}
                onChange={setImageTaskTimeoutSeconds}
                placeholder="300"
                unit="秒"
              />
            </Field>
          </div>
        </section>

        <section className={configSectionClassName}>
          <SectionHeading
            title="用户默认限制"
            tip="限制普通用户创作并发额度和速率；图片生成/编辑按请求张数计入，聊天任务按 1 个计入；管理员不受影响；0 表示不限制。"
          />
          <div className="grid gap-3 sm:grid-cols-2">
            <Field className={configFieldClassName}>
              <ConfigFieldLabel htmlFor="settings-user-default-concurrent-limit">
                创作并发额度
              </ConfigFieldLabel>
              <NumberInputWithUnit
                id="settings-user-default-concurrent-limit"
                min={0}
                value={config?.user_default_concurrent_limit ?? ""}
                onChange={setUserDefaultConcurrentLimit}
                placeholder="0"
                unit="个"
              />
            </Field>
            <Field className={configFieldClassName}>
              <ConfigFieldLabel htmlFor="settings-user-default-rpm-limit">
                用户默认 RPM
              </ConfigFieldLabel>
              <NumberInputWithUnit
                id="settings-user-default-rpm-limit"
                min={0}
                value={config?.user_default_rpm_limit ?? ""}
                onChange={setUserDefaultRpmLimit}
                placeholder="0"
                unit="次/分"
              />
            </Field>
          </div>
        </section>

        <section className={configSectionClassName}>
          <SectionHeading
            title="默认计费"
            tip="创建或注册新普通用户时使用这些默认值；管理员不受本地计费限制。bucket_a 服务对外模型 gpt-image-2，bucket_b 服务 codex-gpt-image-2 与 gemini-3.1-flash-image。"
          />
          <div className="grid gap-4 lg:grid-cols-2">
            <BillingBucketDefaults
              title="桶 A 默认计费（gpt-image-2）"
              billingTypeId="settings-default-bucket-a-billing-type"
              billingType={defaultBucketABillingType}
              setBillingType={setDefaultBucketABillingType}
              standardBalanceId="settings-default-bucket-a-standard-balance"
              standardBalance={config?.default_bucket_a_standard_balance ?? ""}
              setStandardBalance={setDefaultBucketAStandardBalance}
              subscriptionQuotaId="settings-default-bucket-a-subscription-quota"
              subscriptionQuota={config?.default_bucket_a_subscription_quota ?? ""}
              setSubscriptionQuota={setDefaultBucketASubscriptionQuota}
              subscriptionPeriodId="settings-default-bucket-a-subscription-period"
              subscriptionPeriod={config?.default_bucket_a_subscription_period || "monthly"}
              setSubscriptionPeriod={setDefaultBucketASubscriptionPeriod}
            />
            <BillingBucketDefaults
              title="桶 B 默认计费（codex / gemini）"
              billingTypeId="settings-default-bucket-b-billing-type"
              billingType={defaultBucketBBillingType}
              setBillingType={setDefaultBucketBBillingType}
              standardBalanceId="settings-default-bucket-b-standard-balance"
              standardBalance={config?.default_bucket_b_standard_balance ?? ""}
              setStandardBalance={setDefaultBucketBStandardBalance}
              subscriptionQuotaId="settings-default-bucket-b-subscription-quota"
              subscriptionQuota={config?.default_bucket_b_subscription_quota ?? ""}
              setSubscriptionQuota={setDefaultBucketBSubscriptionQuota}
              subscriptionPeriodId="settings-default-bucket-b-subscription-period"
              subscriptionPeriod={config?.default_bucket_b_subscription_period || "monthly"}
              setSubscriptionPeriod={setDefaultBucketBSubscriptionPeriod}
            />
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field className={configFieldClassName}>
              <ConfigFieldLabel htmlFor="settings-auto-prefer-bucket-b-model">
                Auto 桶 B 偏好
              </ConfigFieldLabel>
              <Select
                value={String(config?.auto_prefer_bucket_b_model || "codex") === "gemini" ? "gemini" : "codex"}
                onValueChange={(value) =>
                  setAutoPreferBucketBModel(value === "gemini" ? "gemini" : "codex")
                }
              >
                <SelectTrigger
                  id="settings-auto-prefer-bucket-b-model"
                  className={settingsInputClassName}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="codex">优先 codex-gpt-image-2</SelectItem>
                  <SelectItem value="gemini">优先 gemini-3.1-flash-image</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </div>
        </section>

        <section className={configSectionClassName}>
          <SectionHeading
            title="出站代理"
            tip="留空表示不使用代理。"
            action={
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="w-full sm:w-auto"
                onClick={() => void handleTestProxy()}
                disabled={isTestingProxy}
              >
                {isTestingProxy ? (
                  <LoaderCircle
                    data-icon="inline-start"
                    className="animate-spin"
                  />
                ) : (
                  <PlugZap data-icon="inline-start" />
                )}
                测试代理
              </Button>
            }
          />
          <Field className="gap-1.5">
            <ConfigFieldLabel htmlFor="settings-proxy">
              全局代理
            </ConfigFieldLabel>
            <Input
              id="settings-proxy"
              value={String(config?.proxy || "")}
              onChange={(event) => {
                setProxy(event.target.value);
                setProxyTestResult(null);
              }}
              placeholder="http://127.0.0.1:7890"
              className={settingsInputClassName}
            />
            {proxyTestResult ? (
              <div
                className={cn(
                  "rounded-[13px] border px-3 py-2 text-xs leading-5",
                  proxyTestResult.ok
                    ? "border-emerald-200 bg-emerald-50 text-emerald-800"
                    : "border-rose-200 bg-rose-50 text-rose-800",
                )}
              >
                {proxyTestResult.ok
                  ? `代理可用：HTTP ${proxyTestResult.status}，用时 ${proxyTestResult.latency_ms} ms`
                  : `代理不可用：${proxyTestResult.error ?? "未知错误"}（用时 ${proxyTestResult.latency_ms} ms）`}
              </div>
            ) : null}
          </Field>
        </section>

        <section className={configSectionClassName}>
          <SectionHeading
            title="账号入口"
            tip="开启后登录页会显示账号注册入口，新账号默认绑定普通用户角色。"
          />
          <div className="grid gap-2 sm:grid-cols-2">
            <ConfigOption
              checked={Boolean(config?.registration_enabled)}
              onCheckedChange={setRegistrationEnabled}
              label="开放账号注册"
            />
          </div>
        </section>

        <section className={configSectionClassName}>
          <SectionHeading
            title="自动维护"
            tip="账号异常或限流时自动从号池移除。"
          />
          <div className="grid gap-2 sm:grid-cols-2">
            <ConfigOption
              checked={Boolean(config?.auto_remove_invalid_accounts)}
              onCheckedChange={setAutoRemoveInvalidAccounts}
              label="自动移除异常账号"
            />
            <ConfigOption
              checked={Boolean(config?.auto_remove_rate_limited_accounts)}
              onCheckedChange={setAutoRemoveRateLimitedAccounts}
              label="自动移除限流账号"
            />
          </div>
        </section>

      </div>
    </SettingsCard>
  );
}
