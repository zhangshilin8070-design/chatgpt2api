import { Component, type ErrorInfo, type ReactNode } from "react";

import { Button } from "@/components/ui/button";

type AppErrorBoundaryProps = {
  children: ReactNode;
};

type AppErrorBoundaryState = {
  error: Error | null;
};

export class AppErrorBoundary extends Component<AppErrorBoundaryProps, AppErrorBoundaryState> {
  state: AppErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): AppErrorBoundaryState {
    return { error };
  }

  componentDidCatch(error: Error, errorInfo: ErrorInfo) {
    console.error("chatgpt2api UI crashed", error, errorInfo);
  }

  render() {
    if (!this.state.error) {
      return this.props.children;
    }

    return (
      <main className="min-h-screen bg-background px-4 py-10 text-foreground">
        <div className="mx-auto max-w-xl rounded-[24px] border border-border bg-card p-6 shadow-[0_0_22px_rgba(44,74,116,0.12)]">
          <p className="text-xs font-semibold uppercase tracking-[0.18em] text-muted-foreground">UI Error</p>
          <h1 className="mt-2 text-2xl font-semibold">页面刚刚渲染失败</h1>
          <p className="mt-3 text-sm leading-6 text-muted-foreground">
            已拦截白屏错误。你可以直接刷新页面继续使用；如果反复出现，请把下面的错误信息发给维护者。
          </p>
          <pre className="mt-4 max-h-56 overflow-auto rounded-xl bg-muted p-3 text-xs text-muted-foreground">
            {this.state.error.message || String(this.state.error)}
          </pre>
          <div className="mt-5 flex gap-2">
            <Button type="button" onClick={() => window.location.reload()}>
              刷新页面
            </Button>
            <Button type="button" variant="outline" onClick={() => this.setState({ error: null })}>
              返回应用
            </Button>
          </div>
        </div>
      </main>
    );
  }
}
