"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { LoaderCircle, Plus, RefreshCw } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { hasAPIPermission, type StoredAuthSession } from "@/store/auth";
import {
  createOpenAIAccount,
  deleteOpenAIAccount,
  listOpenAIAccounts,
  updateOpenAIAccount,
  updateOpenAIAccountModelState,
  type OpenAIAccount,
  type OpenAIAccountInput,
  type OpenAIAccountUpstreamModel,
} from "@/lib/openai-accounts";

import { OpenAIAccountFormDialog } from "./openai-account-form-dialog";
import { OpenAIAccountTable } from "./openai-account-table";

/**
 * 「OpenAI 协议账号」Tab 主面板：负责数据加载、CRUD 与按模型启用/禁用，
 * 与 ChatGPT 账号视图完全解耦，确保两边字段互不渗透（Requirement 7.7）。
 */
export type OpenAIAccountPanelProps = {
  session: StoredAuthSession;
};

type DeleteTarget = OpenAIAccount | null;

function extractErrorMessage(error: unknown, fallback: string) {
  if (error instanceof Error && error.message) {
    return error.message;
  }
  return fallback;
}

export function OpenAIAccountPanel({ session }: OpenAIAccountPanelProps) {
  const didLoadRef = useRef(false);
  const [accounts, setAccounts] = useState<OpenAIAccount[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [formOpen, setFormOpen] = useState(false);
  const [editingAccount, setEditingAccount] = useState<OpenAIAccount | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<DeleteTarget>(null);
  const [busyAccountId, setBusyAccountId] = useState<string | null>(null);
  const [togglingModel, setTogglingModel] = useState<{
    accountId: string;
    model: OpenAIAccountUpstreamModel;
  } | null>(null);

  const canList = hasAPIPermission(session, "GET", "/api/openai-accounts");
  const canCreate = hasAPIPermission(session, "POST", "/api/openai-accounts");
  const canUpdate = hasAPIPermission(session, "PATCH", "/api/openai-accounts");
  const canDelete = hasAPIPermission(session, "DELETE", "/api/openai-accounts");
  const canPatchModelState = hasAPIPermission(
    session,
    "PATCH",
    "/api/openai-accounts/model-states",
  );

  const loadAccounts = useCallback(
    async (silent = false) => {
      if (!silent) {
        setIsLoading(true);
      } else {
        setIsRefreshing(true);
      }
      try {
        const data = await listOpenAIAccounts();
        setAccounts(Array.isArray(data.items) ? data.items : []);
      } catch (error) {
        toast.error(extractErrorMessage(error, "加载 OpenAI 协议账号失败"));
      } finally {
        if (!silent) {
          setIsLoading(false);
        } else {
          setIsRefreshing(false);
        }
      }
    },
    [],
  );

  useEffect(() => {
    if (didLoadRef.current) {
      return;
    }
    if (!canList) {
      setIsLoading(false);
      return;
    }
    didLoadRef.current = true;
    void loadAccounts();
  }, [canList, loadAccounts]);

  const summary = useMemo(() => {
    let total = accounts.length;
    let normal = 0;
    let limited = 0;
    let abnormal = 0;
    let disabled = 0;
    accounts.forEach((account) => {
      const states = Object.values(account.model_states);
      if (states.length === 0) {
        return;
      }
      states.forEach((state) => {
        switch (state?.status) {
          case "正常":
            normal += 1;
            break;
          case "限流":
            limited += 1;
            break;
          case "异常":
            abnormal += 1;
            break;
          case "禁用":
            disabled += 1;
            break;
          default:
            break;
        }
      });
    });
    return { total, normal, limited, abnormal, disabled };
  }, [accounts]);

  const openCreateDialog = () => {
    if (!canCreate) {
      toast.error("没有创建 OpenAI 协议账号的权限");
      return;
    }
    setEditingAccount(null);
    setFormOpen(true);
  };

  const openEditDialog = (account: OpenAIAccount) => {
    if (!canUpdate) {
      toast.error("没有更新 OpenAI 协议账号的权限");
      return;
    }
    setEditingAccount(account);
    setFormOpen(true);
  };

  const handleSubmit = async (input: OpenAIAccountInput) => {
    setSubmitting(true);
    try {
      if (editingAccount) {
        const data = await updateOpenAIAccount(editingAccount.id, input);
        setAccounts(Array.isArray(data.items) ? data.items : accounts);
        toast.success("已更新 OpenAI 协议账号");
      } else {
        const data = await createOpenAIAccount(input);
        setAccounts(Array.isArray(data.items) ? data.items : accounts);
        toast.success("已新增 OpenAI 协议账号");
      }
      setFormOpen(false);
      setEditingAccount(null);
    } catch (error) {
      toast.error(extractErrorMessage(error, "保存 OpenAI 协议账号失败"));
    } finally {
      setSubmitting(false);
    }
  };

  const requestDelete = (account: OpenAIAccount) => {
    if (!canDelete) {
      toast.error("没有删除 OpenAI 协议账号的权限");
      return;
    }
    setDeleteTarget(account);
  };

  const confirmDelete = async () => {
    if (!deleteTarget) {
      return;
    }
    setBusyAccountId(deleteTarget.id);
    try {
      const data = await deleteOpenAIAccount(deleteTarget.id);
      setAccounts(Array.isArray(data.items) ? data.items : accounts);
      toast.success("已删除 OpenAI 协议账号");
      setDeleteTarget(null);
    } catch (error) {
      toast.error(extractErrorMessage(error, "删除 OpenAI 协议账号失败"));
    } finally {
      setBusyAccountId(null);
    }
  };

  const handleToggleModel = async (
    account: OpenAIAccount,
    model: OpenAIAccountUpstreamModel,
    nextStatus: "正常" | "禁用",
  ) => {
    if (!canPatchModelState) {
      toast.error("没有更新模型状态的权限");
      return;
    }
    setTogglingModel({ accountId: account.id, model });
    try {
      const data = await updateOpenAIAccountModelState(account.id, model, {
        status: nextStatus,
      });
      setAccounts(Array.isArray(data.items) ? data.items : accounts);
      toast.success(
        nextStatus === "禁用"
          ? `已禁用 ${account.name || account.id} 的 ${model}`
          : `已启用 ${account.name || account.id} 的 ${model}`,
      );
    } catch (error) {
      toast.error(extractErrorMessage(error, "更新模型状态失败"));
    } finally {
      setTogglingModel(null);
    }
  };

  if (!canList) {
    return (
      <Card>
        <CardContent className="flex min-h-[200px] flex-col items-center justify-center gap-2 py-10 text-center">
          <p className="text-sm font-medium text-foreground">没有访问 OpenAI 协议账号的权限</p>
          <p className="max-w-sm text-sm text-muted-foreground">
            请联系管理员为当前账号开放 `GET /api/openai-accounts` 权限。
          </p>
        </CardContent>
      </Card>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <section className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
        <Card className="rounded-2xl bg-white">
          <CardContent className="px-4 py-3">
            <div className="text-xs text-muted-foreground">账号总数</div>
            <div className="mt-1 font-display text-2xl font-semibold text-foreground">
              {summary.total}
            </div>
          </CardContent>
        </Card>
        <Card className="rounded-2xl bg-white">
          <CardContent className="px-4 py-3">
            <div className="text-xs text-muted-foreground">正常模型槽位</div>
            <div className="mt-1 font-display text-2xl font-semibold text-emerald-600">
              {summary.normal}
            </div>
          </CardContent>
        </Card>
        <Card className="rounded-2xl bg-white">
          <CardContent className="px-4 py-3">
            <div className="text-xs text-muted-foreground">限流</div>
            <div className="mt-1 font-display text-2xl font-semibold text-amber-600">
              {summary.limited}
            </div>
          </CardContent>
        </Card>
        <Card className="rounded-2xl bg-white">
          <CardContent className="px-4 py-3">
            <div className="text-xs text-muted-foreground">异常</div>
            <div className="mt-1 font-display text-2xl font-semibold text-rose-600">
              {summary.abnormal}
            </div>
          </CardContent>
        </Card>
        <Card className="rounded-2xl bg-white">
          <CardContent className="px-4 py-3">
            <div className="text-xs text-muted-foreground">禁用</div>
            <div className="mt-1 font-display text-2xl font-semibold text-stone-500">
              {summary.disabled}
            </div>
          </CardContent>
        </Card>
      </section>

      <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <div className="flex items-center gap-3">
          <h2 className="text-lg font-semibold tracking-tight">OpenAI 协议账号</h2>
          <Badge variant="secondary" className="rounded-lg bg-stone-200 px-2 py-0.5 text-stone-700">
            {accounts.length}
          </Badge>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="outline"
            className="h-10 rounded-lg"
            onClick={() => void loadAccounts(true)}
            disabled={isLoading || isRefreshing}
          >
            {isRefreshing ? (
              <LoaderCircle className="size-4 animate-spin" />
            ) : (
              <RefreshCw className="size-4" />
            )}
            刷新
          </Button>
          {canCreate ? (
            <Button
              className="h-10 rounded-lg bg-stone-950 px-4 text-white hover:bg-stone-800"
              onClick={openCreateDialog}
              disabled={isLoading}
            >
              <Plus className="size-4" />
              新增账号
            </Button>
          ) : null}
        </div>
      </div>

      {isLoading && accounts.length === 0 ? (
        <Card>
          <CardContent className="flex min-h-[200px] items-center justify-center py-10 text-muted-foreground">
            <LoaderCircle className="size-5 animate-spin" />
          </CardContent>
        </Card>
      ) : (
        <OpenAIAccountTable
          accounts={accounts}
          loading={isLoading}
          busyAccountId={busyAccountId}
          togglingModel={togglingModel}
          onEdit={openEditDialog}
          onDelete={requestDelete}
          onToggleModel={handleToggleModel}
        />
      )}

      <OpenAIAccountFormDialog
        open={formOpen}
        account={editingAccount}
        submitting={submitting}
        onOpenChange={(open) => {
          if (!open && !submitting) {
            setFormOpen(false);
            setEditingAccount(null);
          } else if (open) {
            setFormOpen(true);
          }
        }}
        onSubmit={handleSubmit}
      />

      <Dialog
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => {
          if (!open && busyAccountId !== deleteTarget?.id) {
            setDeleteTarget(null);
          }
        }}
      >
        <DialogContent showCloseButton={false} className="rounded-2xl p-6">
          <DialogHeader className="gap-2">
            <DialogTitle>删除 OpenAI 协议账号</DialogTitle>
            <DialogDescription className="text-sm leading-6">
              即将删除账号
              <span className="mx-1 font-medium text-foreground">
                {deleteTarget?.name || deleteTarget?.id || ""}
              </span>
              。此操作不可撤销，已发起的并发槽位会被同步释放。
            </DialogDescription>
          </DialogHeader>
          <DialogFooter className="pt-2">
            <Button
              variant="secondary"
              className="h-10 rounded-xl bg-stone-100 px-5 text-stone-700 hover:bg-stone-200"
              onClick={() => setDeleteTarget(null)}
              disabled={Boolean(busyAccountId)}
            >
              取消
            </Button>
            <Button
              variant="destructive"
              className="h-10 rounded-xl px-5"
              onClick={() => void confirmDelete()}
              disabled={Boolean(busyAccountId)}
            >
              {busyAccountId ? <LoaderCircle className="size-4 animate-spin" /> : null}
              删除
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
