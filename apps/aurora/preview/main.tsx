import { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createAuthStore, registerAuthStore } from "@multica/core/auth";
import { setApiInstance } from "@multica/core/api";
import { useAuroraSkills } from "@multica/core/aurora";
import { I18nProvider } from "@multica/core/i18n/react";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { GenerationComposer, SkillDirectory, type DirectoryEntry } from "@multica/views/aurora";
import { NavigationProvider, type NavigationAdapter } from "@multica/views/navigation";
import { RESOURCES } from "@multica/views/locales";
import { TooltipProvider } from "@multica/ui/components/ui/tooltip";
import { AuroraShell } from "../components/aurora-shell";
import { agents, createPreviewApi, FACE_SWAP_AGENT_ID, FACE_SWAP_PATH, storage, user, workspace } from "./fixtures";
import { FaceSwapPreview } from "./face-swap";
import { ToolFrame } from "./tool-frame";
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

const DEFAULT_NOTICE = "设计预览 · 功能、头像、积分为示例数据 · 不连接线上服务";
const currentPath = () => location.pathname === "/" ? "/preview/skills" : location.pathname;

function PreviewContent() {
  const [pathname, setPathname] = useState(currentPath);
  const [notice, setNotice] = useState(DEFAULT_NOTICE);
  const catalog = useAuroraSkills();
  const isFaceSwap = pathname === FACE_SWAP_PATH;
  const agent = agents.find((candidate) => pathname === `/preview/tools/${candidate.id}`);
  const toolOpen = isFaceSwap || !!agent;
  const skill = catalog.data?.value.find((candidate) => agent?.system_key === `aurora:${candidate.id}`) ?? null;
  const explain = () => setNotice("当前为设计预览。真实生成、账户及作品库需在正式环境使用；换脸工作台可以预览本地素材。");

  useEffect(() => {
    const handlePopState = () => { setPathname(currentPath()); setNotice(DEFAULT_NOTICE); };
    window.addEventListener("popstate", handlePopState);
    return () => window.removeEventListener("popstate", handlePopState);
  }, []);

  useEffect(() => {
    document.title = `${isFaceSwap ? "图片／视频换脸" : agent?.name ?? "工具首页"} · Apex AI 设计预览`;
  }, [isFaceSwap, agent]);

  function navigate(path: string, replace = false) {
    if (path !== "/preview/skills" && path !== FACE_SWAP_PATH &&
      !agents.some((candidate) => path === `/preview/tools/${candidate.id}`)) {
      explain();
      return;
    }
    if (replace) history.replaceState(null, "", path);
    else history.pushState(null, "", path);
    setPathname(path);
    setNotice(DEFAULT_NOTICE);
    window.scrollTo(0, 0);
  }

  function select(entry: DirectoryEntry) {
    navigate(entry.agent.id === FACE_SWAP_AGENT_ID ? FACE_SWAP_PATH : `/preview/tools/${entry.agent.id}`);
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
          onChangeCapture={(event) => {
            if (!isFaceSwap && event.target instanceof HTMLInputElement && event.target.type === "file") {
              event.stopPropagation(); explain();
            }
          }}
          onDropCapture={(event) => {
            if (!isFaceSwap) {
              event.preventDefault(); event.stopPropagation(); explain();
            }
          }}>
          <div className="design-preview-notice" role="status">{notice}</div>
          {toolOpen ? (
            <ToolFrame>
              {isFaceSwap ? <FaceSwapPreview /> : (
                <main className="apex-generic-tool">
                  <GenerationComposer key={agent!.id}
                    agent={agent} skill={skill} open presentation="page"
                    onOpenChange={(open) => { if (!open) navigate("/preview/skills"); }}
                    worksHref="/preview/works" topUpHref="/preview/billing" />
                </main>
              )}
            </ToolFrame>
          ) : (
            <AuroraShell slug={workspace.slug}>
              <SkillDirectory onSelect={select} worksHref="/preview/works" topUpHref="/preview/billing" />
            </AuroraShell>
          )}
        </div>
      </TooltipProvider>
    </NavigationProvider>
  );
}

createRoot(document.getElementById("root")!).render(<Preview />);
