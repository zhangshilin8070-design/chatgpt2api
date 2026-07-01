"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Sparkles, Wand2, RefreshCcw } from "lucide-react";
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import {
  deleteProfileIndustryPromptOverride,
  fetchCurrentIndustry,
  fetchProfileIndustryPrompt,
  fetchProfileIndustryPrompts,
  saveProfileIndustryPromptOverride,
  setCurrentIndustry,
  type IndustryPromptUserItem,
  type IndustryPromptUserDetail,
} from "@/lib/industry-prompts";

const INDUSTRY_PROMPT_MAX_LENGTH = 4000;
const NONE_VALUE = "__none__";

type Props = {
  onIndustryKeyChange: (industryKey: string) => void;
};

export function IndustryPromptSelector({ onIndustryKeyChange }: Props) {
  const [items, setItems] = useState<IndustryPromptUserItem[]>([]);
  const [current, setCurrent] = useState<string>("");
  const [previewOpen, setPreviewOpen] = useState(false);
  const [editorOpen, setEditorOpen] = useState(false);
  const [detail, setDetail] = useState<IndustryPromptUserDetail | null>(null);
  const [draft, setDraft] = useState("");
  const [loadingDetail, setLoadingDetail] = useState(false);
  const [saving, setSaving] = useState(false);

  const refreshList = useCallback(async () => {
    try {
      const response = await fetchProfileIndustryPrompts();
      setItems(response.items ?? []);
    } catch {
      // silent; sidebar can render without industry
    }
  }, []);

  const refreshCurrent = useCallback(async () => {
    try {
      const response = await fetchCurrentIndustry();
      setCurrent(response.industry_key || "");
      onIndustryKeyChange(response.effective ? response.industry_key || "" : "");
    } catch {
      // silent
    }
  }, [onIndustryKeyChange]);

  useEffect(() => {
    void refreshList();
    void refreshCurrent();
  }, [refreshList, refreshCurrent]);

  const currentItem = useMemo(
    () => items.find((item) => item.industry_key === current) ?? null,
    [items, current],
  );

  const loadDetail = useCallback(async (industryKey: string) => {
    setLoadingDetail(true);
    try {
      const response = await fetchProfileIndustryPrompt(industryKey);
      setDetail(response.item);
      setDraft(response.item.user_prompt || response.item.public_prompt || "");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "读取行业提示词失败");
    } finally {
      setLoadingDetail(false);
    }
  }, []);

  const handleChange = useCallback(
    async (value: string) => {
      const nextKey = value === NONE_VALUE ? "" : value;
      setCurrent(nextKey);
      onIndustryKeyChange(nextKey);
      try {
        await setCurrentIndustry(nextKey);
      } catch (err) {
        toast.error(err instanceof Error ? err.message : "保存行业选择失败");
      }
    },
    [onIndustryKeyChange],
  );

  const handleOpenPreview = useCallback(async () => {
    if (!current) return;
    setPreviewOpen(true);
    await loadDetail(current);
  }, [current, loadDetail]);

  const handleOpenEditor = useCallback(async () => {
    if (!current) return;
    setEditorOpen(true);
    await loadDetail(current);
  }, [current, loadDetail]);

  const handleSaveOverride = useCallback(async () => {
    if (!current) return;
    if (draft.length > INDUSTRY_PROMPT_MAX_LENGTH) {
      toast.error(`提示词最长 ${INDUSTRY_PROMPT_MAX_LENGTH} 字符`);
      return;
    }
    setSaving(true);
    try {
      await saveProfileIndustryPromptOverride(current, draft);
      toast.success("已保存自定义行业提示词");
      await refreshList();
      await loadDetail(current);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "保存失败");
    } finally {
      setSaving(false);
    }
  }, [current, draft, loadDetail, refreshList]);

  const handleResetOverride = useCallback(async () => {
    if (!current) return;
    setSaving(true);
    try {
      await deleteProfileIndustryPromptOverride(current);
      toast.success("已恢复默认");
      await refreshList();
      await loadDetail(current);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "恢复默认失败");
    } finally {
      setSaving(false);
    }
  }, [current, loadDetail, refreshList]);

  return (
    <div className="flex flex-wrap items-center gap-2 rounded-md border border-border/70 bg-background/60 px-3 py-2 text-xs">
      <div className="flex items-center gap-1 text-muted-foreground">
        <Sparkles className="h-3.5 w-3.5" />
        行业提示词
      </div>
      <Select value={current || NONE_VALUE} onValueChange={(value) => void handleChange(value)}>
        <SelectTrigger className="h-8 min-w-[160px] text-xs">
          <SelectValue placeholder="无" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={NONE_VALUE}>无（不附加行业提示词）</SelectItem>
          {items.map((item) => (
            <SelectItem key={item.industry_key} value={item.industry_key}>
              {item.label}
              {item.has_override ? "（已自定义）" : ""}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {current ? (
        <>
          <Button variant="ghost" size="sm" className="h-8 px-2 text-xs" onClick={() => void handleOpenPreview()}>
            预览
          </Button>
          <Button variant="ghost" size="sm" className="h-8 px-2 text-xs" onClick={() => void handleOpenEditor()}>
            <Wand2 className="mr-1 h-3.5 w-3.5" />
            编辑我的版本
          </Button>
          {currentItem?.has_override ? (
            <span className="text-emerald-600">已自定义 · 优先生效</span>
          ) : (
            <span className="text-muted-foreground">使用公共默认</span>
          )}
        </>
      ) : (
        <span className="text-muted-foreground">未选择：不附加任何行业提示词</span>
      )}

      <Dialog open={previewOpen} onOpenChange={setPreviewOpen}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>行业提示词预览</DialogTitle>
            <DialogDescription>该内容会自动拼接到你的 prompt 前，随生图请求下发。</DialogDescription>
          </DialogHeader>
          <div className="max-h-[60vh] overflow-y-auto whitespace-pre-wrap rounded-md bg-muted/40 p-3 text-sm">
            {loadingDetail ? "加载中…" : detail?.resolved_prompt || "（空）"}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setPreviewOpen(false)}>
              关闭
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={editorOpen} onOpenChange={setEditorOpen}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>编辑我的行业提示词</DialogTitle>
            <DialogDescription>
              保存后仅对你本人生效，优先级高于管理员维护的公共默认。清空并保存等同于「该行业我不需要任何附加提示词」，如需恢复公共默认请点击「恢复默认」。
            </DialogDescription>
          </DialogHeader>
          {detail ? (
            <div className="grid gap-3 text-sm">
              <div>
                <div className="mb-1 text-xs text-muted-foreground">公共默认（只读）</div>
                <div className="max-h-40 overflow-y-auto whitespace-pre-wrap rounded-md bg-muted/40 p-2 text-xs">
                  {detail.public_prompt || "（空）"}
                </div>
              </div>
              <div>
                <div className="mb-1 flex items-center justify-between text-xs">
                  <span className="text-muted-foreground">我的自定义</span>
                  <span className="text-muted-foreground">
                    {draft.length} / {INDUSTRY_PROMPT_MAX_LENGTH}
                  </span>
                </div>
                <Textarea rows={8} value={draft} onChange={(event) => setDraft(event.target.value)} />
              </div>
            </div>
          ) : (
            <div className="text-sm text-muted-foreground">加载中…</div>
          )}
          <DialogFooter className="gap-2">
            {detail?.has_override ? (
              <Button variant="outline" onClick={() => void handleResetOverride()} disabled={saving}>
                <RefreshCcw className="mr-1 h-4 w-4" />
                恢复默认
              </Button>
            ) : null}
            <Button variant="outline" onClick={() => setEditorOpen(false)} disabled={saving}>
              取消
            </Button>
            <Button onClick={() => void handleSaveOverride()} disabled={saving}>
              {saving ? "保存中…" : "保存"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
