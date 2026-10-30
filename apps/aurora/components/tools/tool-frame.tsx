"use client";

import type { ReactNode } from "react";
import { ArrowLeft } from "@multica/ui/icons";
import { AppLink } from "@multica/views/navigation";

export function ToolFrame({ children, directoryHref, userName }: {
  children: ReactNode;
  directoryHref: string;
  userName?: string;
}) {
  return (
    <div className="apex-tool-page">
      <header className="apex-tool-nav">
        <AppLink className="apex-tool-brand" href={directoryHref} aria-label="Apex AI 工具首页">
          APEX <span>AI</span>
        </AppLink>
        <nav aria-label="工具导航">
          <AppLink className="apex-tool-back" href={directoryHref}>
            <ArrowLeft aria-hidden="true" />
            返回工具列表
          </AppLink>
          {userName ? <span className="apex-tool-user" aria-label={userName}>{Array.from(userName.trim())[0]?.toLocaleUpperCase()}</span> : null}
        </nav>
      </header>
      {children}
    </div>
  );
}
