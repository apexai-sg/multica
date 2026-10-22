import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apexTools } from "./tool-definitions";
import { ToolWorkbench } from "./tool-workbench";

const NativeURL = URL;
const fetch = vi.fn();
const revoke = vi.fn();
let urlNumber = 0;
beforeEach(() => {
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
    const view = render(<ToolWorkbench tool={tool} />);
    const extract = screen.getByRole("button", { name: "提取文案" });
    expect(extract).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "查看示例结果" }));
    expect(screen.getByText(/以下文字与上传的视频无关/)).toBeVisible();
    await user.upload(screen.getByLabelText("上传视频文件"), new File(["video"], "interview.mp4", { type: "video/mp4" }));
    expect(screen.queryByLabelText("人物说话第 1 段")).not.toBeInTheDocument();
    expect(extract).toBeEnabled();
    await user.click(extract);
    expect(screen.getByRole("status")).toHaveTextContent("尚未提取视频内容");
    expect(screen.getByRole("button", { name: "复制文字" })).toBeDisabled();
    fireEvent.error(screen.getByLabelText("上传视频预览"));
    expect(extract).toBeDisabled();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    view.unmount();
    expect(revoke).toHaveBeenCalledTimes(1);
    expect(fetch).not.toHaveBeenCalled();
  });

  it("requires both a sheet and a question, accepts replacements, and clears stale status", async () => {
    const user = userEvent.setup();
    const tool = apexTools.find((item) => item.id === "spreadsheet")!;
    render(<ToolWorkbench tool={tool} />);
    const process = screen.getByRole("button", { name: "处理表格" });
    await user.click(screen.getByRole("button", { name: "填入示例文案" }));
    expect(process).toBeDisabled();
    const input = screen.getByLabelText("上传 Excel 或 CSV文件");
    await user.upload(input, new File(["month,sales\n10,100"], "sales.csv", { type: "text/csv" }));
    expect(process).toBeEnabled();
    await user.click(process);
    expect(screen.getByRole("status")).toHaveTextContent("尚未生成作品");
    await user.click(screen.getByRole("button", { name: "制作图表" }));
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    fireEvent.drop(screen.getByRole("region", { name: "上传 Excel 或 CSV" }), { dataTransfer: { files: [new File(["bad"], "photo.png", { type: "image/png" })] } });
    expect(process).toBeDisabled();
    expect(screen.getByRole("alert")).toHaveTextContent("XLSX");
    await user.upload(input, new File(["month,sales\n11,200"], "new.csv", { type: "text/csv" }));
    expect(process).toBeEnabled();
    expect(revoke).toHaveBeenCalledTimes(1);
    expect(fetch).not.toHaveBeenCalled();
  });
});
