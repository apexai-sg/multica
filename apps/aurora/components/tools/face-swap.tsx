"use client";

import { useId, useRef, useState } from "react";
import {
  ArrowRight, Check, Download, Film, ImagePlus, Info, Link2,
  Play, ScanFace, Sparkles, Upload, X,
} from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Spinner } from "@multica/ui/components/ui/spinner";

import { fileSize, useLocalFile as useLocalMedia } from "./local-file";
import { findTool } from "./tool-definitions";
import { toolSubmissionNotice, useToolSubmission } from "./use-tool-submission";

type MediaKind = "video" | "image";

type MediaSlot = ReturnType<typeof useLocalMedia>;

function UploadCard({
  kind, title, step, face = false, slot,
}: {
  kind: MediaKind;
  title: string;
  step: string;
  face?: boolean;
  slot: MediaSlot;
}) {
  const input = useRef<HTMLInputElement>(null);
  const id = useId();
  const [dragging, setDragging] = useState(false);
  const Icon = face ? ScanFace : kind === "video" ? Film : ImagePlus;
  const label = face ? "人脸照片" : kind === "video" ? "原视频" : "原图片";
  const action = face ? "选择照片" : kind === "video" ? "选择视频" : "选择图片";
  const hint = face
    ? "清晰、无遮挡的正面人脸照片"
    : kind === "video" ? "保留原视频的动作、场景和声音" : "保留原图片的姿势、背景和构图";
  const formats = kind === "video" ? "MP4 / MOV / WebM · 预览上限 200 MB" : "JPG / PNG / WebP · 预览上限 20 MB";

  return (
    <section className="apex-upload-card" aria-labelledby={`${id}-title`}
      onDragOver={(event) => { event.preventDefault(); setDragging(true); }}
      onDragLeave={(event) => {
        const next = event.relatedTarget;
        if (!(next instanceof Node) || !event.currentTarget.contains(next)) setDragging(false);
      }}
      onDrop={(event) => {
        event.preventDefault();
        setDragging(false);
        slot.pick(Array.from(event.dataTransfer.files));
      }}
    >
      <div className="apex-upload-heading">
        <h2 id={`${id}-title`}><span>{step}</span>{title}</h2>
        {slot.media && !slot.error ? <span className="apex-file-ready"><Check aria-hidden="true" />已选择</span> : null}
      </div>
      <input
        ref={input} type="file" hidden
        aria-label={`选择${label}文件`}
        accept={kind === "video" ? ".mp4,.mov,.webm" : ".jpg,.jpeg,.png,.webp"}
        onChange={(event) => {
          slot.pick(Array.from(event.target.files ?? []));
          event.target.value = "";
        }}
      />
      <div className={`apex-dropzone${dragging ? " is-dragging" : ""}${slot.media ? " has-media" : ""}${slot.error ? " has-error" : ""}`}>
        {slot.media ? (
          <>
            <div className={`apex-media-preview${face ? " is-face" : ""}`}>
              {kind === "video" ? (
                <video key={slot.media.url} src={slot.media.url} controls playsInline preload="metadata"
                  aria-label="原视频预览" onError={slot.fail} />
              ) : (
                // Blob URLs are local files; they must not use the Next image loader.
                // eslint-disable-next-line @next/next/no-img-element
                <img src={slot.media.url} alt={`${label}预览`} onError={slot.fail} />
              )}
            </div>
            <div className="apex-file-bar">
              <div className="apex-file-name">
                <strong title={slot.media.file.name}>{slot.media.file.name}</strong>
                <span>{fileSize(slot.media.file.size)} · 提交时上传为任务附件</span>
              </div>
              <Button type="button" variant="outline" size="sm" onClick={() => input.current?.click()}>
                替换<span className="sr-only">{label}</span>
              </Button>
              <Button type="button" variant="ghost" size="icon-sm" aria-label={`移除${label}`} onClick={slot.remove}>
                <X aria-hidden="true" />
              </Button>
            </div>
          </>
        ) : (
          <div className="apex-upload-empty">
            <span className="apex-upload-art" aria-hidden="true"><Icon /></span>
            <Button type="button" size="lg" variant="outline" className="apex-pick-button"
              onClick={() => input.current?.click()} aria-describedby={`${id}-hint ${id}-format`}>
              <Upload aria-hidden="true" />{action}
            </Button>
            <p id={`${id}-hint`}>{hint}</p>
            <span id={`${id}-format`}>{formats}</span>
            <span className="apex-drop-hint">也可以把文件拖到这里</span>
          </div>
        )}
      </div>
      {slot.error ? <p className="apex-upload-error" role="alert">{slot.error}</p> : null}
    </section>
  );
}

export function FaceSwapWorkbench({ slug }: { slug: string }) {
  const [mode, setMode] = useState<MediaKind>("video");
  const clearResult = () => undefined;
  const video = useLocalMedia("video", clearResult);
  const picture = useLocalMedia("image", clearResult);
  const face = useLocalMedia("image", clearResult);
  const source = mode === "video" ? video : picture;
  const ready = !!(source.media && face.media && !source.error && !face.error);
  const noun = mode === "video" ? "视频" : "图片";
  const submission = useToolSubmission(findTool("face-swap")!, slug);

  return (
    <main className="apex-face-workbench" onDragOver={(event) => event.preventDefault()} onDrop={(event) => event.preventDefault()}>
      <section className="apex-tool-hero" aria-labelledby="apex-face-title">
        <div className="apex-tool-avatar">
          {/* The avatar is a fictional local design fixture. */}
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img src="/avatars/person-2.svg" alt="" />
          <span aria-hidden="true"><Sparkles /></span>
        </div>
        <div className="apex-tool-intro">
          <span className="apex-tool-eyebrow">你的 AI 创作工具人</span>
          <h1 id="apex-face-title">{noun}换脸</h1>
          <p>上传原{noun}和人脸照片，生成新{noun}</p>
        </div>
        <div className="apex-hero-formula" aria-hidden="true">
          <span>{mode === "video" ? <Film /> : <ImagePlus />}</span>
          <b>+</b><span><ScanFace /></span><ArrowRight /><span><Sparkles /></span>
        </div>
      </section>

      <div className="apex-face-controls">
        <div className="apex-mode-switch" role="group" aria-label="换脸类型">
          {(["video", "image"] as const).map((value) => (
            <Button key={value} type="button" variant={value === mode ? "brand" : "ghost"}
              aria-pressed={value === mode} onClick={() => { setMode(value); clearResult(); }}>
              {value === "video" ? <Film aria-hidden="true" /> : <ImagePlus aria-hidden="true" />}
              {value === "video" ? "视频换脸" : "图片换脸"}
            </Button>
          ))}
        </div>
        <span className="apex-workflow-note">两份素材，一个新作品</span>
      </div>

      <div className="apex-upload-grid">
        <UploadCard key={mode} kind={mode} title={`上传原${noun}`} step="01" slot={source} />
        <div className="apex-upload-connector" aria-hidden="true"><Link2 /></div>
        <UploadCard kind="image" title="上传人脸照片" step="02" face slot={face} />
      </div>

      <div className="apex-generate-area">
        <Button type="button" size="lg" className="apex-generate-button" disabled={!ready || !submission.canSubmit}
          aria-describedby="apex-generation-note"
          onClick={() => {
            const files = [source.media?.file, face.media?.file].filter((item): item is File => !!item);
            void submission.submit({
              values: { 换脸类型: mode === "video" ? "视频换脸" : "图片换脸" },
              files,
            });
          }}>
          {submission.pending ? <Spinner aria-hidden="true" /> : <Sparkles aria-hidden="true" />}
          {submission.pending ? "提交中…" : `生成换脸${noun}`}
          {submission.pending ? null : <ArrowRight aria-hidden="true" />}
        </Button>
        <p id="apex-generation-note">
          <Info aria-hidden="true" />
          {ready ? "素材已就绪" : `请先选择原${noun}和人脸照片`}
          <span>·</span>{toolSubmissionNotice(submission.status)}
          {submission.status === "error" || submission.status === "no-agent" || submission.status === "no-runtime" ? <><span>·</span><Button type="button" variant="outline" size="sm" onClick={submission.reloadAgents}>重新加载</Button></> : null}
        </p>
        {submission.error ? <p className="apex-upload-error" role="alert">{submission.error}</p> : null}
        {submission.createdIdentifier ? <p className="apex-sample-note" role="status">任务已创建：{submission.createdIdentifier}，请到任务列表查看。</p> : null}
      </div>

      <section className="apex-result-panel" aria-labelledby="apex-result-title">
        <div className="apex-result-screen">
          <span className="apex-result-type">{noun}预览</span>
          <span className="apex-result-play" aria-hidden="true">{mode === "video" ? <Play /> : <ImagePlus />}</span>
          <p>提交后，结果会显示在任务详情页</p>
          <div className="apex-result-rail" aria-hidden="true"><span /><span /></div>
        </div>
        <div className="apex-result-copy">
          <span className="apex-result-eyebrow">YOUR CREATION</span>
          <h2 id="apex-result-title">生成结果</h2>
          <p role="status" aria-live="polite">
            提交后会自动打开该任务的问题详情页；AI 的执行结果以评论和附件的形式显示在那里。若无法生成真实换脸{noun}，结果中会说明限制并给出方案。
          </p>
          <Button type="button" variant="outline" size="lg" disabled aria-describedby="apex-result-title">
            <Download aria-hidden="true" />下载{noun}
          </Button>
        </div>
      </section>
      <footer className="apex-workbench-footer">APEX AI <span>让创作，简单一点。</span></footer>
    </main>
  );
}
