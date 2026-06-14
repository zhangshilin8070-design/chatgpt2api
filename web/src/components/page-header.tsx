import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

type PageHeaderProps = {
  eyebrow: string;
  title: string;
  description?: string;
  actions?: ReactNode;
  className?: string;
};

export function PageHeader({ eyebrow, title, description, actions, className }: PageHeaderProps) {
  return (
    <section
      className={cn(
        "flex flex-col gap-4 pb-5 border-b border-[rgba(14,14,14,0.12)] dark:border-[rgba(255,255,255,0.12)]",
        className,
      )}
    >
      <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
        <div className="flex flex-col gap-2">
          <div className="text-[11px] font-medium tracking-[0.18em] uppercase text-[color:var(--color-accent)]">
            {eyebrow}
          </div>
          <h1 className="font-display text-[2.4rem] leading-[1.1] font-semibold tracking-tight text-foreground sm:text-[2.8rem]">
            {title}
          </h1>
          {description ? (
            <p className="max-w-2xl text-sm leading-6 text-muted-foreground">{description}</p>
          ) : null}
        </div>
        {actions ? (
          <div className="flex flex-wrap items-center gap-2 lg:pt-1.5">{actions}</div>
        ) : null}
      </div>
    </section>
  );
}
