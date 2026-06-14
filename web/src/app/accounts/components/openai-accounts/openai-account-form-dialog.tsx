"use client";

import { useEffect, useMemo, useState } from "react";
import { LoaderCircle } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import {
  OPENAI_ACCOUNT_UPSTREAM_MODELS,
  type OpenAIAccount,
  type OpenAIAccountInput,
  type OpenAIAccountUpstreamModel,
} from "@/lib/openai-accounts";

/**
 * 编辑场景下传入 `account`；新增场景传入 `null`。
 *
 * `onSubmit` 在前端校验通过后被调用，由调用方负责发起后端请求并处理 toast。
 * 若 `onSubmit` 抛出错误，由调用方处理；本组件不吞错。
 */
export type OpenAIAccountFormDialogProps = {
  open: boolean;
  account: OpenAIAccount | null;
  onOpenChange: (open: boolean) => void;
  onSubmit: (input: OpenAIAccountInput) => Promise<void> | void;
  submitting?: boolean;
};

type FormErrors = {
  api_key?: string;
  base_url?: string;
  allowed_models?: string;
  concurrency?: string;
};

const MODEL_LABELS: Record<OpenAIAccountUpstreamModel, string> = {
  "gpt-image-2": "gpt-image-2",
  "gemini-3.1-flash-image": "gemini-3.1-flash-image",
};

function isHttpUrl(value: string) {
  try {
    const url = new URL(value);
    return url.protocol === "http:" || url.protocol === "https:";
  } catch {
    return false;
  }
}

type FormState = {
  name: string;
  apiKey: string;
  baseUrl: string;
  allowedModels: OpenAIAccountUpstreamModel[];
  priority: string;
  concurrency: string;
};

function buildInitialState(account: OpenAIAccount | null): FormState {
  if (!account) {
    return {
      name: "",
      apiKey: "",
      baseUrl: "",
      allowedModels: [],
      priority: "0",
      concurrency: "1",
    };
  }
  return {
    name: account.name,
    apiKey: "",
    baseUrl: account.base_url,
    allowedModels: [...account.allowed_models],
    priority: String(account.priority ?? 0),
    concurrency: String(account.concurrency ?? 1),
  };
}

export function OpenAIAccountFormDialog({
  open,
  account,
  onOpenChange,
  onSubmit,
  submitting = false,
}: OpenAIAccountFormDialogProps) {
  const isEditing = Boolean(account);
  const [state, setState] = useState<FormState>(() => buildInitialState(account));
  const [errors, setErrors] = useState<FormErrors>({});

  // 每次打开对话框或切换编辑对象时重置表单状态。关闭时不重置 errors，
  // 避免视觉抖动；下次打开统一由 buildInitialState 重置。
  useEffect(() => {
    if (open) {
      setState(buildInitialState(account));
      setErrors({});
    }
  }, [open, account]);

  const dialogTitle = isEditing ? "编辑 OpenAI 协议账号" : "新增 OpenAI 协议账号";
  const dialogDescription = isEditing
    ? "修改账号基础信息与允许的上游模型；api_key 留空表示保留原值。"
    : "配置 OpenAI 兼容协议账号的 api_key、base_url 与上游模型。";

  const placeholderApiKey = useMemo(() => {
    if (!isEditing) {
      return "sk-...";
    }
    return account?.api_key || "保留原值";
  }, [isEditing, account]);

  const validate = (): FormErrors => {
    const next: FormErrors = {};

    if (!isEditing && !state.apiKey.trim()) {
      next.api_key = "请填写 api_key";
    }

    const trimmedBaseUrl = state.baseUrl.trim();
    if (!trimmedBaseUrl) {
      next.base_url = "请填写 base_url";
    } else if (!isHttpUrl(trimmedBaseUrl)) {
      next.base_url = "base_url 必须是 http:// 或 https:// 开头的合法 URL";
    }

    if (state.allowedModels.length === 0) {
      next.allowed_models = "请至少选择一个上游模型";
    }

    const concurrencyNum = Number(state.concurrency);
    if (!Number.isFinite(concurrencyNum) || concurrencyNum < 1 || !Number.isInteger(concurrencyNum)) {
      next.concurrency = "concurrency 必须是 ≥ 1 的整数";
    }

    return next;
  };

  const toggleModel = (model: OpenAIAccountUpstreamModel, checked: boolean) => {
    setState((prev) => ({
      ...prev,
      allowedModels: checked
        ? Array.from(new Set([...prev.allowedModels, model]))
        : prev.allowedModels.filter((item) => item !== model),
    }));
  };

  const handleSubmit = async () => {
    const validationErrors = validate();
    if (Object.keys(validationErrors).length > 0) {
      setErrors(validationErrors);
      return;
    }
    setErrors({});

    const priorityNum = Number(state.priority);
    const concurrencyNum = Number(state.concurrency);

    const payload: OpenAIAccountInput = {
      name: state.name.trim(),
      base_url: state.baseUrl.trim(),
      allowed_models: state.allowedModels,
      priority: Number.isFinite(priorityNum) ? Math.trunc(priorityNum) : 0,
      concurrency: Math.trunc(concurrencyNum),
    };

    // 编辑场景：api_key 留空时不下发，让后端保留原值。
    const trimmedApiKey = state.apiKey.trim();
    if (!isEditing || trimmedApiKey.length > 0) {
      payload.api_key = trimmedApiKey;
    }

    await onSubmit(payload);
  };

  const handleOpenChange = (next: boolean) => {
    if (submitting && !next) {
      // 提交中不允许关闭，避免请求中状态丢失。
      return;
    }
    onOpenChange(next);
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent showCloseButton={false} className="rounded-2xl p-6">
        <DialogHeader className="gap-2">
          <DialogTitle>{dialogTitle}</DialogTitle>
          <DialogDescription className="text-sm leading-6">
            {dialogDescription}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-2">
            <label className="text-sm font-medium text-stone-700">名称</label>
            <Input
              value={state.name}
              onChange={(event) => setState((prev) => ({ ...prev, name: event.target.value }))}
              placeholder="可选，便于识别该账号"
              className="h-11 rounded-xl border-stone-200 bg-white"
              disabled={submitting}
            />
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium text-stone-700">
              api_key
              {isEditing ? <span className="ml-1 text-xs text-muted-foreground">(留空保留原值)</span> : null}
            </label>
            <Input
              value={state.apiKey}
              onChange={(event) => setState((prev) => ({ ...prev, apiKey: event.target.value }))}
              placeholder={placeholderApiKey}
              className={cn(
                "h-11 rounded-xl border-stone-200 bg-white font-mono text-xs",
                errors.api_key ? "border-rose-400" : "",
              )}
              autoComplete="off"
              spellCheck={false}
              disabled={submitting}
            />
            {errors.api_key ? (
              <p className="text-xs text-rose-500">{errors.api_key}</p>
            ) : null}
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium text-stone-700">base_url</label>
            <Input
              value={state.baseUrl}
              onChange={(event) => setState((prev) => ({ ...prev, baseUrl: event.target.value }))}
              placeholder="https://api.example.com"
              className={cn(
                "h-11 rounded-xl border-stone-200 bg-white font-mono text-xs",
                errors.base_url ? "border-rose-400" : "",
              )}
              autoComplete="off"
              spellCheck={false}
              disabled={submitting}
            />
            {errors.base_url ? (
              <p className="text-xs text-rose-500">{errors.base_url}</p>
            ) : null}
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium text-stone-700">允许的上游模型</label>
            <div
              className={cn(
                "rounded-xl border bg-white p-3",
                errors.allowed_models ? "border-rose-400" : "border-stone-200",
              )}
            >
              <div className="flex flex-col gap-2">
                {OPENAI_ACCOUNT_UPSTREAM_MODELS.map((model) => {
                  const checked = state.allowedModels.includes(model);
                  return (
                    <label
                      key={model}
                      className="flex cursor-pointer items-center gap-2 text-sm text-stone-700"
                    >
                      <Checkbox
                        checked={checked}
                        onCheckedChange={(value) => toggleModel(model, value === true)}
                        disabled={submitting}
                      />
                      <span className="font-mono text-xs">{MODEL_LABELS[model]}</span>
                    </label>
                  );
                })}
              </div>
            </div>
            {errors.allowed_models ? (
              <p className="text-xs text-rose-500">{errors.allowed_models}</p>
            ) : null}
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-2">
              <label className="text-sm font-medium text-stone-700">priority</label>
              <Input
                type="number"
                value={state.priority}
                onChange={(event) => setState((prev) => ({ ...prev, priority: event.target.value }))}
                className="h-11 rounded-xl border-stone-200 bg-white"
                disabled={submitting}
              />
              <p className="text-xs text-muted-foreground">越小越优先调度</p>
            </div>
            <div className="space-y-2">
              <label className="text-sm font-medium text-stone-700">concurrency</label>
              <Input
                type="number"
                min={1}
                value={state.concurrency}
                onChange={(event) => setState((prev) => ({ ...prev, concurrency: event.target.value }))}
                className={cn(
                  "h-11 rounded-xl border-stone-200 bg-white",
                  errors.concurrency ? "border-rose-400" : "",
                )}
                disabled={submitting}
              />
              {errors.concurrency ? (
                <p className="text-xs text-rose-500">{errors.concurrency}</p>
              ) : (
                <p className="text-xs text-muted-foreground">同账号最大并发出图槽位</p>
              )}
            </div>
          </div>
        </div>

        <DialogFooter className="pt-2">
          <Button
            variant="secondary"
            className="h-10 rounded-xl bg-stone-100 px-5 text-stone-700 hover:bg-stone-200"
            onClick={() => handleOpenChange(false)}
            disabled={submitting}
          >
            取消
          </Button>
          <Button
            className="h-10 rounded-xl bg-stone-950 px-5 text-white hover:bg-stone-800"
            onClick={() => void handleSubmit()}
            disabled={submitting}
          >
            {submitting ? <LoaderCircle className="size-4 animate-spin" /> : null}
            {isEditing ? "保存修改" : "新增账号"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
