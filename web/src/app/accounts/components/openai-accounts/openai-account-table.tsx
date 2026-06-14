"use client";

import type { ComponentProps } from "react";
import { Ban, CheckCircle2, CircleAlert, CircleOff } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { cn } from "@/lib/utils";
import type { AccountStatus } from "@/lib/api";
import {
  type OpenAIAccount,
  type OpenAIAccountModelState,
  type OpenAIAccountUpstreamModel,
} from "@/lib/openai-accounts";

import { OpenAIAccountRowActions } from "./openai-account-row-actions";

const STATUS_META: Record<
  AccountStatus,
  {
    icon: typeof CheckCircle2;
    badge: ComponentProps<typeof Badge>["variant"];
  }
> = {
  正常: { icon: CheckCircle2, badge: "success" },
  限流: { icon: CircleAlert, badge: "warning" },
  异常: { icon: CircleOff, badge: "danger" },
  禁用: { icon: Ban, badge: "secondary" },
};

function formatTimestamp(value?: string | null) {
  if (!value) {
    return "—";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  const pad = (num: number) => String(num).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(
    date.getHours(),
  )}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

function ModelStatusBadge({ status }: { status: AccountStatus }) {
  const meta = STATUS_META[status];
  const Icon = meta.icon;
  return (
    <Badge variant={meta.badge} className="inline-flex items-center gap-1 rounded-md px-2 py-0.5">
      <Icon className="size-3" />
      <span className="text-[11px]">{status}</span>
    </Badge>
  );
}

function ModelStateRow({
  model,
  state,
}: {
  model: OpenAIAccountUpstreamModel;
  state: OpenAIAccountModelState | undefined;
}) {
  // 当 model 在 allowed_models 中但 model_states 暂未初始化时（理论上不应发生，
  // 仅作为前端兜底渲染），按默认值展示。后端在 Create/Update 路径会保证一致性。
  const status: AccountStatus = state?.status ?? "正常";
  const success = state?.success ?? 0;
  const fail = state?.fail ?? 0;
  const lastUsedAt = state?.last_used_at ?? "";

  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg bg-stone-50 px-3 py-2">
      <code className="font-mono text-[11px] font-medium text-stone-800">{model}</code>
      <ModelStatusBadge status={status} />
      <span className="text-[11px] text-emerald-700">
        success <span className="font-semibold">{success}</span>
      </span>
      <span className="text-[11px] text-rose-700">
        fail <span className="font-semibold">{fail}</span>
      </span>
      <span className="text-[11px] text-muted-foreground">
        last_used_at <span className="font-mono">{formatTimestamp(lastUsedAt)}</span>
      </span>
      {state?.error_message ? (
        <span
          className="max-w-full truncate text-[11px] text-rose-500"
          title={state.error_message}
        >
          {state.error_message}
        </span>
      ) : null}
    </div>
  );
}

export type OpenAIAccountTableProps = {
  accounts: OpenAIAccount[];
  loading?: boolean;
  busyAccountId?: string | null;
  togglingModel?: { accountId: string; model: OpenAIAccountUpstreamModel } | null;
  onEdit: (account: OpenAIAccount) => void;
  onDelete: (account: OpenAIAccount) => void;
  onToggleModel: (
    account: OpenAIAccount,
    model: OpenAIAccountUpstreamModel,
    nextStatus: "正常" | "禁用",
  ) => void;
};

export function OpenAIAccountTable({
  accounts,
  loading = false,
  busyAccountId,
  togglingModel,
  onEdit,
  onDelete,
  onToggleModel,
}: OpenAIAccountTableProps) {
  return (
    <div className="overflow-hidden rounded-2xl border border-border bg-white">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-[14%]">名称</TableHead>
            <TableHead className="w-[16%]">api_key</TableHead>
            <TableHead className="w-[20%]">base_url</TableHead>
            <TableHead className="w-[8%] text-center">priority</TableHead>
            <TableHead className="w-[8%] text-center">concurrency</TableHead>
            <TableHead>模型状态</TableHead>
            <TableHead className="w-[120px] text-right">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {accounts.length === 0 ? (
            <TableRow>
              <TableCell colSpan={7} className="py-12 text-center text-sm text-muted-foreground">
                {loading ? "加载中..." : "暂无 OpenAI 协议账号"}
              </TableCell>
            </TableRow>
          ) : (
            accounts.map((account) => {
              const isToggling =
                togglingModel?.accountId === account.id ? togglingModel.model : null;
              return (
                <TableRow key={account.id} className="align-top">
                  <TableCell>
                    <div className="font-medium text-foreground">{account.name || "—"}</div>
                  </TableCell>
                  <TableCell>
                    <code className="rounded-md bg-stone-100 px-2 py-1 font-mono text-[11px] text-muted-foreground">
                      {account.api_key || "—"}
                    </code>
                  </TableCell>
                  <TableCell>
                    <span
                      className="block truncate font-mono text-xs text-stone-700"
                      title={account.base_url}
                    >
                      {account.base_url}
                    </span>
                  </TableCell>
                  <TableCell className="text-center font-mono text-xs">{account.priority}</TableCell>
                  <TableCell className="text-center font-mono text-xs">{account.concurrency}</TableCell>
                  <TableCell>
                    <div className="flex flex-col gap-1.5">
                      {account.allowed_models.map((model) => (
                        <ModelStateRow
                          key={model}
                          model={model}
                          state={account.model_states[model]}
                        />
                      ))}
                    </div>
                  </TableCell>
                  <TableCell className="text-right">
                    <div className={cn("flex justify-end")}>
                      <OpenAIAccountRowActions
                        account={account}
                        busy={busyAccountId === account.id}
                        togglingModel={isToggling}
                        onEdit={onEdit}
                        onDelete={onDelete}
                        onToggleModel={onToggleModel}
                      />
                    </div>
                  </TableCell>
                </TableRow>
              );
            })
          )}
        </TableBody>
      </Table>
    </div>
  );
}
