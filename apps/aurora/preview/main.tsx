import { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createAuthStore, registerAuthStore } from "@multica/core/auth";
import { setApiInstance } from "@multica/core/api";
import { I18nProvider } from "@multica/core/i18n/react";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { NavigationProvider, type NavigationAdapter } from "@multica/views/navigation";
import { RESOURCES } from "@multica/views/locales";
import { TooltipProvider } from "@multica/ui/components/ui/tooltip";
import { AuroraShell } from "../components/aurora-shell";
import { createPreviewApi, storage, user, workspace } from "./fixtures";
import { FaceSwapPreview } from "./face-swap";
import { ToolFrame } from "./tool-frame";
import { PreviewDirectory, type DirectorySelection } from "./tool-directory";
import { ToolWorkbench } from "./tool-workbench";
import { findPreviewTool } from "./tool-definitions";
import "../app/globals.css";
import "./preview.css";
import "./tool-workbench.css";

const api = createPreviewApi();
setApiInstance(api);
const auth = createAuthStore({ api, storage });
auth.setState({ user, isLoading: false, status: "authenticated" });
registerAuthStore(auth);
const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, refetchOnWindowFocus: false } } });
client.setQueryData(workspaceKeys.list(), [workspace]);

function Preview() {
  return (
    <QueryClientProvider client={client}>
      <I18nProvider locale="zh-Hans" resources={RESOURCES}>
        <WorkspaceSlugProvider slug={workspace.slug}>
          <PreviewContent />
        </WorkspaceSlugProvider>
      </I18nProvider>
    </QueryClientProvider>
  );
}

const DEFAULT_NOTICE = "设计预览 · 仅展示页面和交互，尚未连接实际处理功能";
const currentPath = () => location.pathname === "/" ? "/preview/skills" : location.pathname;

function PreviewContent() {
  const [pathname, setPathname] = useState(currentPath);
  const [notice, setNotice] = useState(DEFAULT_NOTICE);
  const [selection, setSelection] = useState<DirectorySelection>({ category: "all", query: "" });
  const tool = findPreviewTool(pathname);
  const toolOpen = !!tool;
  const explain = () => setNotice("当前为设计预览。账户、积分和作品库为示例；工具页面可以试选素材和调整设置。");

  useEffect(() => {
    const handlePopState = () => { setPathname(currentPath()); setNotice(DEFAULT_NOTICE); };
    window.addEventListener("popstate", handlePopState);
    return () => window.removeEventListener("popstate", handlePopState);
  }, []);

  useEffect(() => {
    document.title = `${tool?.name ?? "工具首页"} · Apex AI 设计预览`;
  }, [tool]);

  function navigate(path: string, replace = false) {
    if (path !== "/preview/skills" && !findPreviewTool(path)) {
      explain();
      return;
    }
    if (replace) history.replaceState(null, "", path);
    else history.pushState(null, "", path);
    setPathname(path);
    setNotice(DEFAULT_NOTICE);
    window.scrollTo(0, 0);
  }

  const navigation: NavigationAdapter = {
    pathname, searchParams: new URLSearchParams(), hash: "",
    push: (path) => navigate(path), replace: (path) => navigate(path, true),
    back: () => navigate("/preview/skills"),
    getShareableUrl: (path) => `${location.origin}${path}`,
  };

  return (
    <NavigationProvider value={navigation}>
      <TooltipProvider>
        <div className={`design-preview${toolOpen ? " design-preview--tool" : ""}`}
          onClickCapture={(event) => {
            // The production logout also clears the workspace cache.
            if (event.target instanceof Element && event.target.closest("button.aurora-nav-link")) {
              event.preventDefault(); event.stopPropagation(); explain();
            }
          }}
          onSubmitCapture={(event) => {
            event.preventDefault(); event.stopPropagation(); explain();
          }}
          onDropCapture={(event) => {
            if (!toolOpen) {
              event.preventDefault(); event.stopPropagation(); explain();
            }
          }}>
          <div className="design-preview-notice" role="status">{notice}</div>
          {tool ? (
            <ToolFrame>
              {tool.id === "face-swap" ? <FaceSwapPreview /> : <ToolWorkbench key={tool.id} tool={tool} />}
            </ToolFrame>
          ) : (
            <AuroraShell slug={workspace.slug}>
              <PreviewDirectory selection={selection} onChange={setSelection} />
            </AuroraShell>
          )}
        </div>
      </TooltipProvider>
    </NavigationProvider>
  );
}

createRoot(document.getElementById("root")!).render(<Preview />);
