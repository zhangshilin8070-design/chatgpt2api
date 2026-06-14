"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Database, LoaderCircle, RefreshCw, Search, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { PageHeader } from "@/components/page-header";
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
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  cleanupLogs,
  fetchLogGovernance,
  fetchSystemLogs,
  type LogGovernanceSummary,
  type SystemLog,
  type SystemLogFilters,
} from "@/lib/api";
import { useAuthGuard } from "@/lib/use-auth-guard";
import { cn } from "@/lib/utils";

const LOG_LEVEL_OPTIONS = [
  { value: "all", label: "全部级别" },
  { value: "info", label: "信息" },
  { value: "warning", label: "警告" },
  { value: "error", label: "错误" },
] as const;

const OPERATION_TYPE_OPTIONS = [
  { value: "all", label: "全部操作" },
  { value: "查询", label: "查询" },
  { value: "提交", label: "提交" },
  { value: "更新", label: "更新" },
  { value: "删除", label: "删除" },
] as const;

const PAGE_SIZE_OPTIONS = [
  { value: "100", label: "100 条" },
  { value: "200", label: "200 条" },
  { value: "500", label: "500 条" },
] as const;

const DEFAULT_FILTERS: SystemLogFilters = {
  username: "",
  summary: "",
  ip_address: "",
  log_level: "all",
  operation_type: "all",
  start_date: "",
  end_date: "",
  page_size: "200",
};

const LOG_LEVEL_BADGE: Record<string, "secondary" | "warning" | "danger"> = {
  info: "secondary",
  warning: "warning",
  error: "danger",
};

function logDetail(log: SystemLog): Record<string, unknown> {
  const detail = log.detail;
  return detail && typeof detail === "object" ? (detail as Record<string, unknown>) : {};
}

function detailString(log: SystemLog, key: string): string {
  const value = logDetail(log)[key];
  if (value === undefined || value === null) {
    return "";
  }
  return String(value);
}

function logLevelOf(log: SystemLog): string {
  const level = detailString(log, "log_level").toLowerCase();
  return level || "info";
}

function formatStatus(log: SystemLog): string {
  const status = detailString(log, "status");
  return status || "-";
}

export default function LogsPage() {
  const { isCheckingAuth, session } = useAuthGuard(undefined, "/logs");
  if (isCheckingAuth || !session) {
    return (
      <div className="flex min-h-[40vh] items-center justify-center">
        <LoaderCircle className="size-5 animate-spin text-stone-400" />
      </div>
    );
  }
  return <LogsContent />;
}

function LogsContent() {
  const [filters, setFilters] = useState<SystemLogFilters>(DEFAULT_FILTERS);
  const [logs, setLogs] = useState<SystemLog[]>([]);
  const [governance, setGovernance] = useState<LogGovernanceSummary | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [selectedLog, setSelectedLog] = useState<SystemLog | null>(null);
  const [isCleanupOpen, setIsCleanupOpen] = useState(false);
  const [retentionDays, setRetentionDays] = useState("7");
  const [isCleaning, setIsCleaning] = useState(false);

  const updateFilter = useCallback(<Key extends keyof SystemLogFilters>(key: Key, value: SystemLogFilters[Key]) => {
    setFilters((prev) => ({ ...prev, [key]: value }));
  }, []);

  const loadLogs = useCallback(async () => {
    setIsLoading(true);
    try {
      const [logsData, governanceData] = await Promise.all([
        fetchSystemLogs(filters),
        fetchLogGovernance(),
      ]);
      setLogs(Array.isArray(logsData.items) ? logsData.items : []);
      setGovernance(governanceData.governance);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "加载日志失败");
    } finally {
      setIsLoading(false);
    }
  }, [filters]);

  useEffect(() => {
    void loadLogs();
    // 仅在挂载时自动加载，后续通过查询按钮触发
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleCleanup = async () => {
    const days = Number.parseInt(retentionDays, 10);
    if (!Number.isFinite(days) || days < 1) {
      toast.error("保留天数必须是大于 0 的整数");
      return;
    }
    setIsCleaning(true);
    try {
      const data = await cleanupLogs(days);
      setGovernance(data.governance);
      setIsCleanupOpen(false);
      toast.success(`已清理 ${data.cleanup.deleted} 条日志，保留 ${data.cleanup.remaining} 条`);
      void loadLogs();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "清理日志失败");
    } finally {
      setIsCleaning(false);
    }
  };

  const governanceLabel = useMemo(() => {
    if (!governance) {
      return "—";
    }
    const range = [governance.oldest_time, governance.latest_time].filter(Boolean).join(" ~ ");
    return range ? `${governance.total} 条 · ${range}` : `${governance.total} 条`;
  }, [governance]);

  return (
    <section className="flex flex-col gap-5">
      <PageHeader
        eyebrow="LOGS"
        title="日志管理"
        actions={
          <>
            <Button variant="outline" onClick={() => void loadLogs()} disabled={isLoading} className="h-10 rounded-lg">
              <RefreshCw className={cn("size-4", isLoading ? "animate-spin" : "")} />
              刷新
            </Button>
            <Button variant="outline" onClick={() => setIsCleanupOpen(true)} className="h-10 rounded-lg">
              <Trash2 className="size-4" />
              清理日志
            </Button>
          </>
        }
      />

      <Card>
        <CardContent className="flex flex-col gap-4 p-5">
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
            <Input
              value={filters.username ?? ""}
              onChange={(event) => updateFilter("username", event.target.value)}
              placeholder="用户 / 账号"
              className="h-10 rounded-lg"
            />
            <Input
              value={filters.summary ?? ""}
              onChange={(event) => updateFilter("summary", event.target.value)}
              placeholder="摘要 / 路径"
              className="h-10 rounded-lg"
            />
            <Input
              value={filters.ip_address ?? ""}
              onChange={(event) => updateFilter("ip_address", event.target.value)}
              placeholder="IP 地址"
              className="h-10 rounded-lg"
            />
            <Select value={String(filters.log_level ?? "all")} onValueChange={(value) => updateFilter("log_level", value)}>
              <SelectTrigger className="h-10">
                <SelectValue placeholder="日志级别" />
              </SelectTrigger>
              <SelectContent>
                {LOG_LEVEL_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select value={String(filters.operation_type ?? "all")} onValueChange={(value) => updateFilter("operation_type", value)}>
              <SelectTrigger className="h-10">
                <SelectValue placeholder="操作类型" />
              </SelectTrigger>
              <SelectContent>
                {OPERATION_TYPE_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Input
              type="date"
              value={filters.start_date ?? ""}
              onChange={(event) => updateFilter("start_date", event.target.value)}
              className="h-10 rounded-lg"
            />
            <Input
              type="date"
              value={filters.end_date ?? ""}
              onChange={(event) => updateFilter("end_date", event.target.value)}
              className="h-10 rounded-lg"
            />
            <Select value={String(filters.page_size ?? "200")} onValueChange={(value) => updateFilter("page_size", value)}>
              <SelectTrigger className="h-10">
                <SelectValue placeholder="每页条数" />
              </SelectTrigger>
              <SelectContent>
                {PAGE_SIZE_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Database className="size-4" />
              <span>存量 {governanceLabel}</span>
            </div>
            <Button onClick={() => void loadLogs()} disabled={isLoading} className="h-10 rounded-lg">
              <Search className="size-4" />
              查询
            </Button>
          </div>
        </CardContent>
      </Card>

      <Card className="overflow-hidden">
        <CardContent className="p-0">
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-[170px]">时间</TableHead>
                  <TableHead className="w-[90px]">级别</TableHead>
                  <TableHead>摘要</TableHead>
                  <TableHead className="w-[120px]">用户</TableHead>
                  <TableHead className="w-[90px]">状态</TableHead>
                  <TableHead className="w-[130px]">IP</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {isLoading ? (
                  <TableRow>
                    <TableCell colSpan={6} className="py-16 text-center">
                      <LoaderCircle className="mx-auto size-5 animate-spin text-stone-400" />
                    </TableCell>
                  </TableRow>
                ) : logs.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={6} className="py-16 text-center text-sm text-muted-foreground">
                      暂无日志
                    </TableCell>
                  </TableRow>
                ) : (
                  logs.map((log, index) => {
                    const level = logLevelOf(log);
                    return (
                      <TableRow
                        key={`${log.time}-${index}`}
                        className="cursor-pointer"
                        onClick={() => setSelectedLog(log)}
                      >
                        <TableCell className="font-mono text-xs text-muted-foreground">{log.time}</TableCell>
                        <TableCell>
                          <Badge variant={LOG_LEVEL_BADGE[level] ?? "secondary"} className="rounded-md uppercase">
                            {level}
                          </Badge>
                        </TableCell>
                        <TableCell className="max-w-[420px] truncate">{log.summary || "-"}</TableCell>
                        <TableCell className="truncate text-sm">{detailString(log, "username") || "-"}</TableCell>
                        <TableCell className="font-mono text-xs">{formatStatus(log)}</TableCell>
                        <TableCell className="font-mono text-xs text-muted-foreground">
                          {detailString(log, "ip_address") || "-"}
                        </TableCell>
                      </TableRow>
                    );
                  })
                )}
              </TableBody>
            </Table>
          </div>
        </CardContent>
      </Card>

      <Dialog open={Boolean(selectedLog)} onOpenChange={(open) => (!open ? setSelectedLog(null) : null)}>
        <DialogContent className="max-h-[80vh] overflow-y-auto rounded-2xl p-6 sm:max-w-2xl">
          <DialogHeader className="gap-2">
            <DialogTitle>日志详情</DialogTitle>
            <DialogDescription className="font-mono text-xs">{selectedLog?.time}</DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="text-sm font-medium text-foreground">{selectedLog?.summary || "-"}</div>
            <pre className="overflow-x-auto rounded-xl bg-muted/60 p-4 text-xs leading-relaxed">
              {selectedLog ? JSON.stringify(logDetail(selectedLog), null, 2) : ""}
            </pre>
          </div>
        </DialogContent>
      </Dialog>

      <Dialog open={isCleanupOpen} onOpenChange={setIsCleanupOpen}>
        <DialogContent className="rounded-2xl p-6">
          <DialogHeader className="gap-2">
            <DialogTitle>清理历史日志</DialogTitle>
            <DialogDescription className="text-sm leading-6">
              将删除早于保留天数的日志，操作不可恢复。
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <label className="text-sm font-medium text-stone-700 dark:text-foreground">保留最近天数</label>
            <Input
              type="number"
              min={1}
              max={3650}
              value={retentionDays}
              onChange={(event) => setRetentionDays(event.target.value)}
              className="h-11 rounded-xl"
            />
          </div>
          <DialogFooter>
            <Button type="button" variant="secondary" className="h-10 rounded-xl px-5" onClick={() => setIsCleanupOpen(false)} disabled={isCleaning}>
              取消
            </Button>
            <Button type="button" variant="destructive" className="h-10 rounded-xl px-5" onClick={() => void handleCleanup()} disabled={isCleaning}>
              {isCleaning ? <LoaderCircle className="size-4 animate-spin" /> : <Trash2 className="size-4" />}
              清理
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}
