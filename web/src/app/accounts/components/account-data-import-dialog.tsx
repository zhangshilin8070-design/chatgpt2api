"use client";

import { useRef, useState, type ChangeEvent } from "react";
import { Database, FileJson, LoaderCircle, Upload } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import { importAccountData, type Account, type AccountImportItem } from "@/lib/api";
import { cn } from "@/lib/utils";

type AccountDataImportDialogProps = {
  disabled?: boolean;
  onImported: (items: Account[]) => void;
};

type ImportSummary = {
  total: number;
  created: number;
  updated: number;
  skipped: number;
  failed: number;
  items: AccountImportItem[];
};

export function AccountDataImportDialog({ disabled, onImported }: AccountDataImportDialogProps) {
  const [open, setOpen] = useState(false);
  const [content, setContent] = useState("");
  const [fileName, setFileName] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [summary, setSummary] = useState<ImportSummary | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);

  const reset = () => {
    setContent("");
    setFileName("");
    setSummary(null);
    if (fileInputRef.current) {
      fileInputRef.current.value = "";
    }
  };

  const handleClose = (next: boolean) => {
    if (submitting) return;
    setOpen(next);
    if (!next) {
      reset();
    }
  };

  const handleFileChange = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (!file) return;
    try {
      const text = await file.text();
      setContent(text);
      setFileName(file.name);
      setSummary(null);
    } catch (error) {
      toast.error("读取文件失败", {
        description: error instanceof Error ? error.message : String(error),
      });
    }
  };

  const handleImport = async () => {
    const trimmed = content.trim();
    if (!trimmed) {
      toast.error("请选择导入文件或粘贴 JSON 内容");
      return;
    }
    try {
      JSON.parse(trimmed);
    } catch {
      toast.error("内容不是合法 JSON");
      return;
    }
    setSubmitting(true);
    try {
      const response = await importAccountData(trimmed);
      const next: ImportSummary = {
        total: response.total,
        created: response.created,
        updated: response.updated,
        skipped: response.skipped,
        failed: response.failed,
        items: response.items ?? [],
      };
      setSummary(next);
      onImported(response.accounts?.items ?? []);
      const successText = `创建 ${next.created} 个，更新 ${next.updated} 个，跳过 ${next.skipped} 个，失败 ${next.failed} 个`;
      if (next.failed > 0) {
        toast.warning("导入完成（含失败项）", { description: successText });
      } else {
        toast.success("导入完成", { description: successText });
      }
    } catch (error) {
      toast.error("导入失败", {
        description: error instanceof Error ? error.message : String(error),
      });
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <Button
        type="button"
        variant="outline"
        className="h-10 rounded-lg"
        disabled={disabled}
        onClick={() => setOpen(true)}
      >
        <Database className="size-4" />
        导入数据
      </Button>
      <DialogContent className="max-w-2xl rounded-2xl">
        <DialogHeader>
          <DialogTitle>导入账号数据</DialogTitle>
          <DialogDescription>
            支持 sub2api 导出的 JSON（含 accounts 字段）。仅会导入 platform=openai、type=oauth 的账号；
            apikey 类型账号会被跳过并在结果中提示。
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="flex items-center gap-3 rounded-xl border border-dashed border-gray-300 bg-gray-50 px-4 py-3 dark:border-gray-700 dark:bg-gray-900/40">
            <FileJson className="size-5 text-muted-foreground" />
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm">{fileName || "未选择文件"}</div>
              <div className="text-xs text-muted-foreground">JSON (.json)</div>
            </div>
            <Button
              type="button"
              variant="secondary"
              className="shrink-0"
              disabled={submitting}
              onClick={() => fileInputRef.current?.click()}
            >
              <Upload className="size-4" />
              选择文件
            </Button>
            <input
              ref={fileInputRef}
              type="file"
              accept="application/json,.json"
              className="hidden"
              onChange={(event) => void handleFileChange(event)}
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">或粘贴 JSON 内容</label>
            <Textarea
              value={content}
              onChange={(event) => {
                setContent(event.target.value);
                setSummary(null);
                if (fileName) setFileName("");
              }}
              rows={8}
              spellCheck={false}
              placeholder='{"accounts":[{"platform":"openai","type":"oauth","credentials":{...}}]}'
              disabled={submitting}
              className="font-mono text-xs"
            />
          </div>

          {summary ? (
            <div className="space-y-2 rounded-xl border border-gray-200 p-3 text-sm dark:border-gray-700">
              <div className="font-medium">导入结果</div>
              <div className="grid grid-cols-2 gap-x-6 gap-y-1 text-xs sm:grid-cols-5">
                <div>共 {summary.total}</div>
                <div className="text-emerald-600">创建 {summary.created}</div>
                <div className="text-blue-600">更新 {summary.updated}</div>
                <div className="text-amber-600">跳过 {summary.skipped}</div>
                <div className="text-red-600">失败 {summary.failed}</div>
              </div>
              {summary.items.length > 0 ? (
                <div className="max-h-48 overflow-auto rounded-lg bg-gray-50 p-2 font-mono text-[11px] dark:bg-gray-900/60">
                  {summary.items.map((item) => (
                    <div
                      key={item.index}
                      className={cn(
                        "whitespace-pre-wrap py-0.5",
                        item.action === "failed" && "text-red-600",
                        item.action === "skipped" && "text-amber-600",
                        item.action === "created" && "text-emerald-600",
                        item.action === "updated" && "text-blue-600",
                      )}
                    >
                      #{item.index} [{item.action}] {item.name || item.email || "-"}
                      {item.type ? ` · ${item.type}` : ""}
                      {item.message ? ` — ${item.message}` : ""}
                    </div>
                  ))}
                </div>
              ) : null}
            </div>
          ) : null}
        </div>

        <DialogFooter>
          <Button type="button" variant="ghost" disabled={submitting} onClick={() => handleClose(false)}>
            关闭
          </Button>
          <Button type="button" disabled={submitting} onClick={() => void handleImport()}>
            {submitting ? <LoaderCircle className="size-4 animate-spin" /> : <Upload className="size-4" />}
            开始导入
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
