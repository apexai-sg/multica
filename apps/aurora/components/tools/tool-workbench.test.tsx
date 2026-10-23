import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ToolSubmissionStatus } from "./use-tool-submission";
import { apexTools } from "./tool-definitions";
import { ToolWorkbench } from "./tool-workbench";

const submission = vi.hoisted(() => ({
  submit: vi.fn(),
  pending: false,
  error: null as string | null,
  createdIdentifier: null as string | null,
  canSubmit: true,
  status: "ready" as ToolSubmissionStatus,
  agent: { id: "agent" },
  reloadAgents: vi.fn(),
}));

vi.mock("./use-tool-submission", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./use-tool-submission")>()),
  useToolSubmission: () => submission,
}));

const NativeURL = URL;
const fetch = vi.fn();
const revoke = vi.fn();
let urlNumber = 0;
beforeEach(() => {
  submission.submit.mockClear();
  submission.reloadAgents.mockClear();
  Object.assign(submission, {
    pending: false,
    error: null,
    createdIdentifier: null,
    canSubmit: true,
    status: "ready",
  });
  fetch.mockClear(); revoke.mockClear();
  vi.stubGlobal("fetch", fetch);
  vi.stubGlobal("URL", class extends NativeURL {
    static createObjectURL() { return `blob:tool-${++urlNumber}`; }
    static revokeObjectURL = revoke;
  });
  vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: true })));
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {};
  vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(() => {});
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe("tool workspace preview boundaries", () => {
  it("never treats sample transcription as an uploaded video's result", async () => {
    const user = userEvent.setup();
    const tool = apexTools.find((item) => item.id === "video-transcript")!;
    const view = render(<ToolWorkbench tool={tool} slug="preview" />);
    const extract = screen.getByRole("button", { name: "提取文案" });
    expect(extract).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "查看示例结果" }));
    expect(screen.getByText(/以下文字与上传的视频无关/)).toBeVisible();
    await user.upload(screen.getByLabelText("上传视频文件"), new File(["video"], "interview.mp4", { type: "video/mp4" }));
    expect(screen.queryByLabelText("人物说话第 1 段")).not.toBeInTheDocument();
    expect(extract).toBeEnabled();
    await user.click(extract);
    expect(submission.submit).toHaveBeenCalledTimes(1);
    expect(submission.submit.mock.calls[0]![0].files.map((item: File) => item.name)).toEqual(["interview.mp4"]);
    expect(Object.keys(submission.submit.mock.calls[0]![0].values).length).toBeGreaterThan(0);
    expect(screen.getByRole("button", { name: "复制文字" })).toBeDisabled();
    fireEvent.error(screen.getByLabelText("上传视频预览"));
    expect(extract).toBeDisabled();
    view.unmount();
    expect(revoke).toHaveBeenCalledTimes(1);
    expect(fetch).not.toHaveBeenCalled();
  });

  it("requires both a sheet and a question and accepts replacements", async () => {
    const user = userEvent.setup();
    const tool = apexTools.find((item) => item.id === "spreadsheet")!;
    render(<ToolWorkbench tool={tool} slug="preview" />);
    const process = screen.getByRole("button", { name: "处理表格" });
    await user.click(screen.getByRole("button", { name: "填入示例文案" }));
    expect(process).toBeDisabled();
    const input = screen.getByLabelText("上传 Excel 或 CSV文件");
    await user.upload(input, new File(["month,sales\n10,100"], "sales.csv", { type: "text/csv" }));
    expect(process).toBeEnabled();
    await user.click(process);
    expect(submission.submit).toHaveBeenCalledTimes(1);
    expect(submission.submit.mock.calls[0]![0].source.length).toBeGreaterThan(0);
    await user.click(screen.getByRole("button", { name: "制作图表" }));
    fireEvent.drop(screen.getByRole("region", { name: "上传 Excel 或 CSV" }), { dataTransfer: { files: [new File(["bad"], "photo.png", { type: "image/png" })] } });
    expect(process).toBeDisabled();
    expect(screen.getByRole("alert")).toHaveTextContent("XLSX");
    await user.upload(input, new File(["month,sales\n11,200"], "new.csv", { type: "text/csv" }));
    expect(process).toBeEnabled();
    expect(revoke).toHaveBeenCalledTimes(1);
    expect(fetch).not.toHaveBeenCalled();
  });

  it("disables the action and names the reason when no assistant can run the task", () => {
    Object.assign(submission, { canSubmit: false, status: "no-agent" });
    const tool = apexTools.find((item) => item.id === "poster")!;
    render(<ToolWorkbench tool={tool} slug="preview" />);

    expect(screen.getByRole("button", { name: "制作海报" })).toBeDisabled();
    expect(screen.getByText(/工作区还没有可用的助手/)).toBeVisible();
  });

  it("names a tool the server has no expert for", () => {
    Object.assign(submission, { canSubmit: false, status: "no-expert" });
    const tool = apexTools.find((item) => item.id === "poster")!;
    render(<ToolWorkbench tool={tool} slug="preview" />);

    expect(screen.getByRole("button", { name: "制作海报" })).toBeDisabled();
    expect(screen.getByText(/该工具尚未配置执行专家/)).toBeVisible();
  });

  it("shows an assistant-list failure with a way to reload it", async () => {
    Object.assign(submission, { canSubmit: false, status: "error" });
    const user = userEvent.setup();
    const tool = apexTools.find((item) => item.id === "poster")!;
    render(<ToolWorkbench tool={tool} slug="preview" />);

    expect(screen.getByText(/无法加载工作区助手/)).toBeVisible();
    await user.click(screen.getByRole("button", { name: "重新加载" }));
    expect(submission.reloadAgents).toHaveBeenCalledTimes(1);
  });

  it("shows progress and blocks a second click while submitting", () => {
    Object.assign(submission, { pending: true, canSubmit: false });
    const tool = apexTools.find((item) => item.id === "poster")!;
    render(<ToolWorkbench tool={tool} slug="preview" />);

    const button = screen.getByRole("button", { name: /提交中/ });
    expect(button).toBeDisabled();
    expect(submission.submit).not.toHaveBeenCalled();
  });

  it("shows a submission error and, when the detail page cannot open, the created issue", () => {
    Object.assign(submission, {
      error: "任务已创建（MUL-42），但打开详情页失败，请到任务列表查看。",
      createdIdentifier: "MUL-42",
      pending: false,
      canSubmit: false,
    });
    const tool = apexTools.find((item) => item.id === "poster")!;
    render(<ToolWorkbench tool={tool} slug="preview" />);

    expect(screen.getByRole("alert")).toHaveTextContent("打开详情页失败");
    expect(screen.getByText(/任务已创建：MUL-42/)).toBeVisible();
  });
});
