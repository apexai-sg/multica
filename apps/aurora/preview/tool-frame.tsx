import type { ReactNode } from "react";
import { ArrowLeft } from "lucide-react";
import { AppLink } from "@multica/views/navigation";

export function ToolFrame({ children }: { children: ReactNode }) {
  return (
    <div className="apex-tool-page">
      <header className="apex-tool-nav">
        <AppLink className="apex-tool-brand" href="/preview/skills" aria-label="Apex AI 工具首页">
          APEX <span>AI</span>
        </AppLink>
        <nav aria-label="工具导航">
          <AppLink className="apex-tool-back" href="/preview/skills">
            <ArrowLeft aria-hidden="true" />
            返回工具列表
          </AppLink>
          <span className="apex-tool-user" aria-label="Hannah">H</span>
        </nav>
      </header>
      {children}
    </div>
  );
}
