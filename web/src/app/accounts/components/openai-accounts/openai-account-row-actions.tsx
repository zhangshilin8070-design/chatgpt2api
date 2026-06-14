"use client";

import { Ban, CircleCheck, LoaderCircle, Pencil, Trash2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { cn } from "@/lib/utils";
import type {
  OpenAIAccount,
  OpenAIAccountUpstreamModel,
} from "@/lib/openai-accounts";

/**
 * 行级操作：编辑、删除、按模型启用/禁用。按模型启用/禁用通过 Popover 列出
 * 该账号 `allowed_models` 中的所有 Upstream_Image_Model，分别提供切换按钮。
 *
 * 「启用」语义：把对应模型 `model_states[m].status` 置为 `正常`。
 * 「禁用」语义：把对应模型 `model_states[m].status` 置为 `禁用`。
 *
 * 状态变更结果由调用方负责 toast 与列表刷新。
 */
export type OpenAIAccountRowActionsProps = {
  account: OpenAIAccount;
  busy?: boolean;
  togglingModel?: OpenAIAccountUpstreamModel | null;
  onEdit: (account: OpenAIAccount) => void;
  onDelete: (account: OpenAIAccount) => void;
  onToggleModel: (
    account: OpenAIAccount,
    model: OpenAIAccountUpstreamModel,
    nextStatus: "正常" | "禁用",
  ) => void;
};

export function OpenAIAccountRowActions({
  account,
  busy = false,
  togglingModel,
  onEdit,
  onDelete,
  onToggleModel,
}: OpenAIAccountRowActionsProps) {
  return (
    <div className="flex items-center gap-1 text-muted-foreground">
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="size-8 rounded-lg hover:bg-muted hover:text-foreground"
        onClick={() => onEdit(account)}
        disabled={busy}
        aria-label="编辑账号"
        title="编辑账号"
      >
        <Pencil className="size-4" />
      </Button>

      <Popover>
        <PopoverTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="size-8 rounded-lg hover:bg-muted hover:text-foreground"
            disabled={busy || account.allowed_models.length === 0}
            aria-label="按模型启用/禁用"
            title="按模型启用/禁用"
          >
            <Ban className="size-4" />
          </Button>
        </PopoverTrigger>
        <PopoverContent align="end" className="w-64 p-2">
          <div className="space-y-1.5">
            <div className="px-2 pb-1 pt-0.5 text-xs font-medium text-muted-foreground">
              按模型启用 / 禁用
            </div>
            {account.allowed_models.map((model) => {
              const state = account.model_states[model];
              const status = state?.status ?? "正常";
              const disabled = status === "禁用";
              const isToggling = togglingModel === model;
              const nextStatus: "正常" | "禁用" = disabled ? "正常" : "禁用";
              return (
                <button
                  key={model}
                  type="button"
                  onClick={() => onToggleModel(account, model, nextStatus)}
                  disabled={busy || isToggling}
                  className={cn(
                    "flex w-full items-center justify-between gap-2 rounded-lg px-2 py-2 text-left text-sm transition-colors hover:bg-muted",
                    "disabled:pointer-events-none disabled:opacity-60",
                  )}
                >
                  <span className="flex min-w-0 items-center gap-2">
                    {disabled ? (
                      <Ban className="size-3.5 shrink-0 text-stone-400" />
                    ) : (
                      <CircleCheck className="size-3.5 shrink-0 text-emerald-500" />
                    )}
                    <span className="truncate font-mono text-xs">{model}</span>
                  </span>
                  <span className="shrink-0 text-xs text-muted-foreground">
                    {isToggling ? (
                      <LoaderCircle className="size-3.5 animate-spin" />
                    ) : disabled ? (
                      "启用"
                    ) : (
                      "禁用"
                    )}
                  </span>
                </button>
              );
            })}
          </div>
        </PopoverContent>
      </Popover>

      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="size-8 rounded-lg text-rose-500 hover:bg-rose-50 hover:text-rose-600"
        onClick={() => onDelete(account)}
        disabled={busy}
        aria-label="删除账号"
        title="删除账号"
      >
        <Trash2 className="size-4" />
      </Button>
    </div>
  );
}
