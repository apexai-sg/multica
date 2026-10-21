import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TranscriptResult } from "./transcript-result";

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); });

describe("transcript sample editor", () => {
  it("keeps speech and screen edits separate and reports clipboard failures", async () => {
    const user = userEvent.setup();
    const copy = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    render(<TranscriptResult />);
    expect(screen.getByRole("button", { name: "复制文字" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "查看示例结果" }));
    await user.clear(screen.getByLabelText("人物说话第 1 段"));
    await user.type(screen.getByLabelText("人物说话第 1 段"), "修改后的口播示例");
    await user.click(screen.getByRole("button", { name: "复制文字" }));
    expect(copy).toHaveBeenLastCalledWith(expect.stringContaining("[00:00–00:04] 修改后的口播示例"));
    await user.click(screen.getByRole("tab", { name: "画面文字" }));
    expect(screen.getByLabelText("画面文字第 1 段")).toHaveValue("产品拍照的 3 个小技巧");
    await user.click(screen.getByRole("checkbox", { name: "显示时间点" }));
    await user.click(screen.getByRole("button", { name: "复制文字" }));
    expect(copy).toHaveBeenLastCalledWith(expect.not.stringContaining("00:00"));
    copy.mockRejectedValueOnce(new Error("clipboard unavailable"));
    await user.click(screen.getByRole("button", { name: "复制文字" }));
    expect(screen.getByRole("status")).toHaveTextContent("复制未成功");
    await user.click(screen.getByRole("tab", { name: "人物说话" }));
    expect(screen.getByLabelText("人物说话第 1 段")).toHaveValue("修改后的口播示例");
  });

  it("exports edited sample text as timed SRT with an explicitly labeled filename", async () => {
    const user = userEvent.setup();
    let exported: Blob | undefined;
    const revoke = vi.fn();
    const NativeURL = URL;
    vi.stubGlobal("URL", class extends NativeURL {
      static createObjectURL(blob: Blob) { exported = blob; return "blob:sample-export"; }
      static revokeObjectURL = revoke;
    });
    let filename = "";
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) { filename = this.download; });
    render(<TranscriptResult />);
    await user.click(screen.getByRole("button", { name: "查看示例结果" }));
    await user.clear(screen.getByLabelText("人物说话第 1 段"));
    await user.type(screen.getByLabelText("人物说话第 1 段"), "编辑后的内容");
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: "导出 SRT 示例" }));
    expect(filename).toBe("Apex-内置示例-人物说话.srt");
    vi.advanceTimersByTime(1000);
    expect(revoke).toHaveBeenCalledWith("blob:sample-export");
    vi.useRealTimers();
    const text = await new Promise<string>((resolve) => {
      const reader = new FileReader();
      reader.onload = () => resolve(String(reader.result));
      reader.readAsText(exported!);
    });
    expect(text).toContain("1\n00:00:00,000 --> 00:00:04,000\n编辑后的内容");
    expect(text).toContain("4\n00:00:14,000 --> 00:00:20,000");
  });
});
