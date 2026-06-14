"use client";

import * as React from "react";

import { cn } from "@/lib/utils";

/**
 * 轻量 Tabs 原语：保持 shadcn-style API（Tabs / TabsList / TabsTrigger / TabsContent）。
 *
 * 仓库未引入 `@radix-ui/react-tabs`，因此这里通过 React Context 自实现一个仅覆盖
 * 受控/非受控值切换的最小内核，避免新增运行时依赖。键盘交互保留浏览器默认的
 * Tab/Enter 行为（按钮原生焦点环），不主动接管箭头导航以免与页面级快捷键冲突。
 */

type TabsContextValue = {
  value: string;
  setValue: (next: string) => void;
};

const TabsContext = React.createContext<TabsContextValue | null>(null);

function useTabsContext(component: string) {
  const ctx = React.useContext(TabsContext);
  if (!ctx) {
    throw new Error(`${component} must be used within a <Tabs>`);
  }
  return ctx;
}

type TabsProps = Omit<React.HTMLAttributes<HTMLDivElement>, "onChange"> & {
  /** 受控值；优先级高于 `defaultValue`。 */
  value?: string;
  /** 非受控初始值；仅在首次挂载时生效。 */
  defaultValue?: string;
  /** 选中项变更回调；受控/非受控均会触发。 */
  onValueChange?: (next: string) => void;
};

function Tabs({
  value,
  defaultValue,
  onValueChange,
  className,
  children,
  ...props
}: TabsProps) {
  const [internal, setInternal] = React.useState<string>(defaultValue ?? "");
  const isControlled = value !== undefined;
  const current = isControlled ? (value as string) : internal;

  const setValue = React.useCallback(
    (next: string) => {
      if (!isControlled) {
        setInternal(next);
      }
      onValueChange?.(next);
    },
    [isControlled, onValueChange],
  );

  const ctxValue = React.useMemo<TabsContextValue>(
    () => ({ value: current, setValue }),
    [current, setValue],
  );

  return (
    <TabsContext.Provider value={ctxValue}>
      <div data-slot="tabs" className={cn("flex flex-col gap-3", className)} {...props}>
        {children}
      </div>
    </TabsContext.Provider>
  );
}

function TabsList({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      role="tablist"
      data-slot="tabs-list"
      className={cn(
        "inline-flex h-10 w-fit items-center gap-1 rounded-xl border border-border bg-muted/40 p-1 text-muted-foreground",
        className,
      )}
      {...props}
    />
  );
}

type TabsTriggerProps = React.ButtonHTMLAttributes<HTMLButtonElement> & {
  /** 与对应 `<TabsContent value=...>` 关联的稳定 key。 */
  value: string;
};

function TabsTrigger({ value, className, onClick, ...props }: TabsTriggerProps) {
  const ctx = useTabsContext("TabsTrigger");
  const active = ctx.value === value;
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      data-state={active ? "active" : "inactive"}
      data-slot="tabs-trigger"
      onClick={(event) => {
        ctx.setValue(value);
        onClick?.(event);
      }}
      className={cn(
        "inline-flex h-8 shrink-0 items-center justify-center gap-1.5 rounded-lg px-3 text-sm font-medium transition-colors",
        "outline-none focus-visible:ring-[3px] focus-visible:ring-ring/30",
        "disabled:pointer-events-none disabled:opacity-50",
        active
          ? "bg-white text-foreground shadow-[0_2px_6px_rgba(24,40,72,0.08)]"
          : "text-muted-foreground hover:text-foreground",
        className,
      )}
      {...props}
    />
  );
}

type TabsContentProps = React.HTMLAttributes<HTMLDivElement> & {
  /** 与对应 `<TabsTrigger value=...>` 关联的稳定 key。 */
  value: string;
};

function TabsContent({ value, className, ...props }: TabsContentProps) {
  const ctx = useTabsContext("TabsContent");
  if (ctx.value !== value) {
    return null;
  }
  return (
    <div
      role="tabpanel"
      data-state="active"
      data-slot="tabs-content"
      className={cn("flex flex-col gap-3 outline-none", className)}
      {...props}
    />
  );
}

export { Tabs, TabsContent, TabsList, TabsTrigger };
