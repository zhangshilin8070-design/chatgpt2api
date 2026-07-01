"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Copy, Pencil, RefreshCw, Search, Sparkles, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { useAuthGuard } from "@/lib/use-auth-guard";
import { cn } from "@/lib/utils";
import { hasAPIPermission } from "@/store/auth";
import {
  createIndustryPromptPreset,
  deleteIndustryPromptPreset,
  fetchAdminIndustryPrompts,
  updateIndustryPromptPreset,
  type IndustryPromptPreset,
} from "@/lib/industry-prompts";

const INDUSTRY_PROMPT_MAX_LENGTH = 4000;

type StatusFilter = "all" | "enabled" | "disabled";

export default function IndustryPromptsPage() {
  const { isCheckingAuth, session } = useAuthGuard(undefined, "/industry-prompts");
  const [items, setItems] = useState<IndustryPromptPreset[]>([]);
  const [overrides, setOverrides] = useState<Record<string, number>>({});
  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [loading, setLoading] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<IndustryPromptPreset | null>(null);
  const [form, setForm] = useState<{
    industry_key: string;
    label: string;
    description: string;
    prompt: string;
    sort_order: string;
    enabled: boolean;
  }>({
    industry_key: "",
    label: "",
    description: "",
    prompt: "",
    sort_order: "10",
    enabled: true,
  });

  const canWrite = useMemo(
    () => (session ? hasAPIPermission(session, "POST", "/api/admin/industry-prompts") : false),
    [session],
  );
  const canDelete = useMemo(
    () => (session ? hasAPIPermission(session, "DELETE", "/api/admin/industry-prompts") : false),
    [session],
  );

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const response = await fetchAdminIndustryPrompts({
        search,
        status: statusFilter === "all" ? "" : statusFilter,
      });
      setItems(response.items ?? []);
      setOverrides(response.overrides_count_by_key ?? {});
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "加载行业提示词失败");
    } finally {
      setLoading(false);
    }
  }, [search, statusFilter]);

  useEffect(() => {
    if (!session) return;
    void refresh();
  }, [session, refresh]);

  const metrics = useMemo(() => {
    const total = items.length;
    const enabled = items.filter((item) => item.enabled).length;
    const disabled = total - enabled;
    const latest = items.reduce((acc, item) => (item.updated_at > acc ? item.updated_at : acc), "");
    return { total, enabled, disabled, latest };
  }, [items]);

  const openCreateDialog = () => {
    setEditing(null);
    setForm({ industry_key: "", label: "", description: "", prompt: "", sort_order: "10", enabled: true });
    setDialogOpen(true);
  };

  const openEditDialog = (item: IndustryPromptPreset) => {
    setEditing(item);
    setForm({
      industry_key: item.industry_key,
      label: item.label,
      description: item.description ?? "",
      prompt: item.prompt,
      sort_order: String(item.sort_order),
      enabled: item.enabled,
    });
    setDialogOpen(true);
  };

  const handleSave = async () => {
    if (form.prompt.length > INDUSTRY_PROMPT_MAX_LENGTH) {
      toast.error(`提示词最长 ${INDUSTRY_PROMPT_MAX_LENGTH} 字符`);
      return;
    }
    try {
      if (editing) {
        await updateIndustryPromptPreset(editing.id, {
          label: form.label,
          description: form.description,
          prompt: form.prompt,
          sort_order: Number(form.sort_order) || 0,
          enabled: form.enabled,
        });
        toast.success("已保存");
      } else {
        await createIndustryPromptPreset({
          industry_key: form.industry_key,
          label: form.label,
          description: form.description,
          prompt: form.prompt,
          sort_order: Number(form.sort_order) || 0,
          enabled: form.enabled,
        });
        toast.success("已创建");
      }
      setDialogOpen(false);
      await refresh();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "保存失败");
    }
  };

  const handleDelete = async (item: IndustryPromptPreset) => {
    if (!confirm(`确认删除「${item.label}」？用户已存的自定义内容将被保留。`)) return;
    try {
      await deleteIndustryPromptPreset(item.id);
      toast.success("已删除");
      await refresh();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "删除失败");
    }
  };

  const toggleEnabled = async (item: IndustryPromptPreset) => {
    try {
      await updateIndustryPromptPreset(item.id, { enabled: !item.enabled });
      await refresh();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "状态切换失败");
    }
  };

  const copyKey = async (key: string) => {
    try {
      await navigator.clipboard.writeText(key);
      toast.success("已复制 industry_key");
    } catch {
      toast.error("复制失败");
    }
  };

  const toggleSelect = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const allSelected = items.length > 0 && items.every((item) => selected.has(item.id));

  if (!session) return null;
  if (isCheckingAuth) return null;

  return (
    <div className="mx-auto flex w-full max-w-6xl flex-col gap-6 p-4 pb-16">
      <PageHeader
        eyebrow="Prompt Library"
        title="行业提示词"
        description="维护面向全体用户的公共行业默认提示词。用户可基于此在生图时自定义，优先级高于此处的公共版本。"
        actions={
          canWrite ? (
            <div className="flex items-center gap-2">
              <Button variant="outline" size="sm" onClick={() => refresh()} disabled={loading}>
                <RefreshCw className={cn("mr-1 h-4 w-4", loading && "animate-spin")} />
                刷新
              </Button>
              <Button size="sm" onClick={openCreateDialog}>
                <Sparkles className="mr-1 h-4 w-4" />
                新建行业
              </Button>
            </div>
          ) : null
        }
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <MetricCard label="行业总数" value={metrics.total} />
        <MetricCard label="启用" value={metrics.enabled} tone="success" />
        <MetricCard label="停用" value={metrics.disabled} tone="muted" />
        <MetricCard label="最近更新" value={metrics.latest ? metrics.latest.slice(0, 19).replace("T", " ") : "—"} small />
      </div>

      <Card>
        <CardContent className="flex flex-col gap-4 p-4">
          <div className="flex flex-col gap-3 md:flex-row md:items-center">
            <div className="relative md:max-w-xs md:flex-1">
              <Search className="pointer-events-none absolute left-2 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder="搜索 key / 名称 / 提示词内容"
                className="pl-8"
              />
            </div>
            <Select value={statusFilter} onValueChange={(value) => setStatusFilter(value as StatusFilter)}>
              <SelectTrigger className="md:w-40"><SelectValue placeholder="状态" /></SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部状态</SelectItem>
                <SelectItem value="enabled">启用</SelectItem>
                <SelectItem value="disabled">停用</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-10">
                  <Checkbox
                    checked={allSelected}
                    onCheckedChange={(checked) => {
                      if (checked) setSelected(new Set(items.map((item) => item.id)));
                      else setSelected(new Set());
                    }}
                  />
                </TableHead>
                <TableHead>industry_key</TableHead>
                <TableHead>名称</TableHead>
                <TableHead>排序</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>覆盖用户</TableHead>
                <TableHead>更新时间</TableHead>
                <TableHead className="w-32 text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.length === 0 && (
                <TableRow>
                  <TableCell colSpan={8} className="py-10 text-center text-sm text-muted-foreground">
                    {loading ? "加载中…" : "暂无行业提示词，点击右上「新建行业」开始添加。"}
                  </TableCell>
                </TableRow>
              )}
              {items.map((item) => (
                <TableRow key={item.id}>
                  <TableCell>
                    <Checkbox checked={selected.has(item.id)} onCheckedChange={() => toggleSelect(item.id)} />
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    <button
                      type="button"
                      className="inline-flex items-center gap-1 rounded bg-muted px-2 py-0.5 hover:bg-muted/70"
                      onClick={() => copyKey(item.industry_key)}
                    >
                      {item.industry_key}
                      <Copy className="h-3 w-3" />
                    </button>
                  </TableCell>
                  <TableCell>
                    <div className="font-medium">{item.label}</div>
                    {item.description ? (
                      <div className="text-xs text-muted-foreground">{item.description}</div>
                    ) : null}
                  </TableCell>
                  <TableCell>{item.sort_order}</TableCell>
                  <TableCell>
                    <Badge variant={item.enabled ? "success" : "secondary"}>
                      {item.enabled ? "启用" : "停用"}
                    </Badge>
                  </TableCell>
                  <TableCell>{overrides[item.industry_key] ?? 0}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {item.updated_at ? item.updated_at.slice(0, 19).replace("T", " ") : "—"}
                  </TableCell>
                  <TableCell className="space-x-1 text-right">
                    <Button variant="ghost" size="icon" title="编辑" onClick={() => openEditDialog(item)} disabled={!canWrite}>
                      <Pencil className="h-4 w-4" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      title={item.enabled ? "点击停用" : "点击启用"}
                      onClick={() => toggleEnabled(item)}
                      disabled={!canWrite}
                    >
                      {item.enabled ? "停用" : "启用"}
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      title="删除"
                      onClick={() => handleDelete(item)}
                      disabled={!canDelete}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>{editing ? `编辑「${editing.label}」` : "新建行业提示词"}</DialogTitle>
            <DialogDescription>
              保存后立即对所有终端用户可见；用户已有的自定义内容仍会优先于本条内容。
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-4">
            <div className="grid gap-1">
              <label className="text-sm font-medium">industry_key</label>
              <Input
                value={form.industry_key}
                onChange={(event) => setForm((prev) => ({ ...prev, industry_key: event.target.value }))}
                placeholder="ecommerce"
                disabled={!!editing}
              />
              <p className="text-xs text-muted-foreground">稳定英文标识；创建后不可修改。</p>
            </div>
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
              <div className="grid gap-1">
                <label className="text-sm font-medium">中文名称</label>
                <Input
                  value={form.label}
                  onChange={(event) => setForm((prev) => ({ ...prev, label: event.target.value }))}
                  placeholder="电商零售"
                />
              </div>
              <div className="grid gap-1">
                <label className="text-sm font-medium">排序</label>
                <Input
                  type="number"
                  value={form.sort_order}
                  onChange={(event) => setForm((prev) => ({ ...prev, sort_order: event.target.value }))}
                />
              </div>
            </div>
            <div className="grid gap-1">
              <label className="text-sm font-medium">简介</label>
              <Input
                value={form.description}
                onChange={(event) => setForm((prev) => ({ ...prev, description: event.target.value }))}
                placeholder="商品图 / 详情页视觉"
              />
            </div>
            <div className="grid gap-1">
              <label className="text-sm font-medium">
                系统提示词
                <span className="ml-2 text-xs text-muted-foreground">
                  {form.prompt.length} / {INDUSTRY_PROMPT_MAX_LENGTH}
                </span>
              </label>
              <Textarea
                value={form.prompt}
                onChange={(event) => setForm((prev) => ({ ...prev, prompt: event.target.value }))}
                rows={8}
                placeholder="面向 XX 场景生成图像：…"
              />
            </div>
            <div className="flex items-center gap-2">
              <Checkbox
                checked={form.enabled}
                onCheckedChange={(checked) => setForm((prev) => ({ ...prev, enabled: !!checked }))}
              />
              <span className="text-sm">启用（用户端下拉可见）</span>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>取消</Button>
            <Button onClick={handleSave}>保存</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function MetricCard({ label, value, tone, small }: { label: string; value: number | string; tone?: "success" | "muted"; small?: boolean }) {
  return (
    <Card>
      <CardContent className="p-4">
        <div className="text-xs text-muted-foreground">{label}</div>
        <div
          className={cn(
            "mt-1 font-semibold",
            small ? "text-sm" : "text-2xl",
            tone === "success" && "text-emerald-600",
            tone === "muted" && "text-muted-foreground",
          )}
        >
          {value}
        </div>
      </CardContent>
    </Card>
  );
}
