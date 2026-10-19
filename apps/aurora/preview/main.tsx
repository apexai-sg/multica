import { useState } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createAuthStore, registerAuthStore } from "@multica/core/auth";
import { setApiInstance } from "@multica/core/api";
import { I18nProvider } from "@multica/core/i18n/react";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { SkillDirectory } from "@multica/views/aurora";
import { NavigationProvider, type NavigationAdapter } from "@multica/views/navigation";
import { RESOURCES } from "@multica/views/locales";
import { TooltipProvider } from "@multica/ui/components/ui/tooltip";
import { AuroraShell } from "../components/aurora-shell";
import { createPreviewApi, storage, user, workspace } from "./fixtures";
import "../app/globals.css";
import "./preview.css";

const api = createPreviewApi();
setApiInstance(api);
const auth = createAuthStore({ api, storage });
auth.setState({ user, isLoading: false, status: "authenticated" });
registerAuthStore(auth);
const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, refetchOnWindowFocus: false } } });
client.setQueryData(workspaceKeys.list(), [workspace]);

function Preview() {
  const [notice, setNotice] = useState("设计预览 · 功能、头像、积分为示例数据 · 不连接线上服务");
  const explain = () => setNotice("当前预览可体验搜索、筛选和填写需求。生成、上传、账户及其他页面需在正式环境使用。");
  const navigation: NavigationAdapter = {
    pathname: "/preview/skills", searchParams: new URLSearchParams(), hash: "",
    push: explain, replace: explain, back: explain,
    getShareableUrl: (path) => `${location.origin}${path}`,
  };
  return (
    <QueryClientProvider client={client}>
      <I18nProvider locale="zh-Hans" resources={RESOURCES}>
        <WorkspaceSlugProvider slug={workspace.slug}>
          <NavigationProvider value={navigation}>
            <TooltipProvider>
              <div className="design-preview" onClickCapture={(event) => {
                // The production logout also clears the workspace cache.
                if (event.target instanceof Element && event.target.closest("button.aurora-nav-link")) {
                  event.preventDefault(); event.stopPropagation(); explain();
                }
              }} onSubmitCapture={(event) => {
                event.preventDefault(); event.stopPropagation(); explain();
              }} onChangeCapture={(event) => {
                if (event.target instanceof HTMLInputElement && event.target.type === "file") {
                  event.stopPropagation(); explain();
                }
              }}>
                <div className="design-preview-notice" role="status">{notice}</div>
                <AuroraShell slug={workspace.slug}>
                  <SkillDirectory worksHref="/preview/works" topUpHref="/preview/billing" />
                </AuroraShell>
              </div>
            </TooltipProvider>
          </NavigationProvider>
        </WorkspaceSlugProvider>
      </I18nProvider>
    </QueryClientProvider>
  );
}

createRoot(document.getElementById("root")!).render(<Preview />);
