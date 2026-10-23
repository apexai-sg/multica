import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ToolSubmissionStatus } from "./use-tool-submission";
import { FaceSwapWorkbench } from "./face-swap";

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
let urlNumber = 0;
const revoke = vi.fn();
const fetch = vi.fn();

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
  it("requires both inputs and submits the source before the face photo", async () => {
    const user = userEvent.setup();
    render(<FaceSwapWorkbench slug="preview" />);
    const generate = screen.getByRole("button", { name: "生成换脸视频" });
    expect(generate).toBeDisabled();
    await user.upload(screen.getByLabelText("选择原视频文件"), video());
    expect(generate).toBeDisabled();
    await user.upload(screen.getByLabelText("选择人脸照片文件"), photo());
    expect(generate).toBeEnabled();
    await user.click(generate);
    expect(submission.submit).toHaveBeenCalledTimes(1);
    // Order decides which file the agent treats as the face.
    expect(submission.submit.mock.calls[0]![0].files.map((item: File) => item.name)).toEqual(["clip.mp4", "face.png"]);
    expect(submission.submit.mock.calls[0]![0].values).toEqual({ 换脸类型: "视频换脸" });
    expect(screen.getByRole("button", { name: "下载视频" })).toBeDisabled();
    expect(fetch).not.toHaveBeenCalled();
  });

  it("retains a source per mode and releases local media when removed or unmounted", async () => {
    const user = userEvent.setup();
    const view = render(<FaceSwapWorkbench slug="preview" />);
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
    render(<FaceSwapWorkbench slug="preview" />);
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

  it("disables the action and names the reason when no assistant can run the task", async () => {
    const user = userEvent.setup();
    Object.assign(submission, { canSubmit: false, status: "no-runtime" });
    render(<FaceSwapWorkbench slug="preview" />);
    await user.upload(screen.getByLabelText("选择原视频文件"), video());
    await user.upload(screen.getByLabelText("选择人脸照片文件"), photo());

    expect(screen.getByRole("button", { name: "生成换脸视频" })).toBeDisabled();
    expect(screen.getByText(/工作区的助手尚未连接运行环境/)).toBeVisible();
  });

  it("shows a submission error and the created issue when the detail page cannot open", async () => {
    Object.assign(submission, {
      error: "任务已创建（MUL-42），但打开详情页失败，请到任务列表查看。",
      createdIdentifier: "MUL-42",
    });
    render(<FaceSwapWorkbench slug="preview" />);

    expect(screen.getByRole("alert")).toHaveTextContent("打开详情页失败");
    expect(screen.getByText(/任务已创建：MUL-42/)).toBeVisible();
  });

  it("shows progress and blocks a second click while submitting", async () => {
    const user = userEvent.setup();
    Object.assign(submission, { pending: true, canSubmit: false });
    render(<FaceSwapWorkbench slug="preview" />);
    await user.upload(screen.getByLabelText("选择原视频文件"), video());
    await user.upload(screen.getByLabelText("选择人脸照片文件"), photo());

    expect(screen.getByRole("button", { name: /提交中/ })).toBeDisabled();
    expect(submission.submit).not.toHaveBeenCalled();
  });
});
