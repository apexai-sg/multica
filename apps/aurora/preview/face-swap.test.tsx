import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { FaceSwapPreview } from "./face-swap";

const NativeURL = URL;
let urlNumber = 0;
const revoke = vi.fn();
const fetch = vi.fn();

beforeEach(() => {
  revoke.mockClear();
  fetch.mockClear();
  vi.stubGlobal("fetch", fetch);
  vi.stubGlobal("URL", class extends NativeURL {
    static createObjectURL() { return `blob:preview-${++urlNumber}`; }
    static revokeObjectURL = revoke;
  });
  vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: true })));
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {};
  vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(() => {});
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const video = () => new File(["video"], "clip.mp4", { type: "video/mp4" });
const photo = (name = "face.png") => new File(["photo"], name, { type: "image/png" });

describe("face swap design preview", () => {
  it("requires both inputs and never submits or presents a fake generated output", async () => {
    const user = userEvent.setup();
    render(<FaceSwapPreview />);
    const generate = screen.getByRole("button", { name: "生成换脸视频" });
    expect(generate).toBeDisabled();
    await user.upload(screen.getByLabelText("选择原视频文件"), video());
    expect(generate).toBeDisabled();
    await user.upload(screen.getByLabelText("选择人脸照片文件"), photo());
    expect(generate).toBeEnabled();
    await user.click(generate);
    expect(screen.getByRole("status")).toHaveTextContent("尚未生成换脸视频");
    expect(screen.getByRole("button", { name: "下载视频" })).toBeDisabled();
    expect(fetch).not.toHaveBeenCalled();
  });

  it("retains a source per mode and releases local media when removed or unmounted", async () => {
    const user = userEvent.setup();
    const view = render(<FaceSwapPreview />);
    await user.upload(screen.getByLabelText("选择原视频文件"), video());
    await user.upload(screen.getByLabelText("选择人脸照片文件"), photo());
    await user.click(screen.getByRole("button", { name: "图片换脸" }));
    expect(screen.getByRole("img", { name: "人脸照片预览" })).toBeVisible();
    expect(screen.getByRole("button", { name: "生成换脸图片" })).toBeDisabled();
    await user.upload(screen.getByLabelText("选择原图片文件"), photo("source.png"));
    await user.click(screen.getByRole("button", { name: "视频换脸" }));
    expect(screen.getByText("clip.mp4")).toBeVisible();
    expect(screen.getByRole("button", { name: "生成换脸视频" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "移除原视频" }));
    expect(screen.getByRole("button", { name: "生成换脸视频" })).toBeDisabled();
    expect(revoke).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("button", { name: "图片换脸" }));
    expect(screen.getByText("source.png")).toBeVisible();
    expect(screen.getByRole("button", { name: "生成换脸图片" })).toBeEnabled();
    view.unmount();
    expect(revoke).toHaveBeenCalledTimes(3);
  });

  it("rejects invalid dropped files and unreadable images, then recovers on replacement", async () => {
    const user = userEvent.setup();
    render(<FaceSwapPreview />);
    fireEvent.drop(screen.getByRole("region", { name: /上传原视频/ }), {
      dataTransfer: { files: [photo()] },
    });
    expect(screen.getByRole("alert")).toHaveTextContent("请选择 MP4");
    fireEvent.drop(screen.getByRole("region", { name: /上传原视频/ }), {
      dataTransfer: { files: [video()] },
    });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    await user.upload(screen.getByLabelText("选择人脸照片文件"), photo());
    fireEvent.error(screen.getByRole("img", { name: "人脸照片预览" }));
    expect(screen.getByRole("button", { name: "生成换脸视频" })).toBeDisabled();
    expect(screen.getByRole("alert")).toHaveTextContent("无法读取这张图片");
    await user.upload(screen.getByLabelText("选择人脸照片文件"), photo("replacement.png"));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "生成换脸视频" })).toBeEnabled();
  });
});
