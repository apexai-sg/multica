import { useEffect, useId, useRef, useState } from "react";
import { Check, FileText, Film, ImagePlus, Sheet, Upload, X } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import type { FileKind } from "./tool-definitions";

const formats: Record<FileKind, { accept: string; mime: string[]; maxMB: number; label: string }> = {
  image: { accept: ".jpg,.jpeg,.png,.webp", mime: ["image/jpeg", "image/png", "image/webp"], maxMB: 20, label: "JPG / PNG / WebP" },
  video: { accept: ".mp4,.mov,.webm", mime: ["video/mp4", "video/quicktime", "video/webm"], maxMB: 200, label: "MP4 / MOV / WebM" },
  pdf: { accept: ".pdf", mime: ["application/pdf"], maxMB: 30, label: "PDF" },
  sheet: { accept: ".xlsx,.xls,.csv", mime: ["application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "application/vnd.ms-excel", "text/csv", "text/plain"], maxMB: 30, label: "XLSX / XLS / CSV" },
  resume: { accept: ".pdf,.docx,.doc", mime: ["application/pdf", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/msword"], maxMB: 30, label: "PDF / DOCX / DOC" },
};

export function fileSize(bytes: number) {
  return bytes < 1024 * 1024
    ? `${Math.max(1, Math.round(bytes / 1024))} KB`
    : `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

export function useLocalFile(kind: FileKind, onChange: () => void) {
  const [media, setMedia] = useState<{ file: File; url: string } | null>(null);
  const [error, setError] = useState("");
  useEffect(() => () => { if (media) URL.revokeObjectURL(media.url); }, [media]);

  function pick(files: File[]) {
    if (!files.length) return;
    onChange();
    if (files.length !== 1) { setError("每个方框只能放一个文件，请重新选择。"); return; }
    const file = files[0]!;
    const format = formats[kind];
    const extensionMatches = format.accept.split(",").some((ext) => file.name.toLowerCase().endsWith(ext));
    const supportedType = format.mime.includes(file.type) || (!file.type && extensionMatches);
    const documentKind = kind === "pdf" || kind === "sheet" || kind === "resume";
    if (!supportedType || (documentKind && !extensionMatches)) {
      setError(`请选择 ${format.label.replaceAll(" / ", "、")} ${kind === "video" ? "视频" : kind === "image" ? "图片" : "文件"}。`);
      return;
    }
    if (!file.size || file.size > format.maxMB * 1024 * 1024) {
      setError(!file.size ? "这个文件为空，请重新选择。" : `预览支持 ${format.maxMB} MB 以内的文件。`);
      return;
    }
    setMedia({ file, url: URL.createObjectURL(file) });
    setError("");
  }

  return {
    media, error, pick,
    remove: () => { setMedia(null); setError(""); onChange(); },
    fail: () => {
      setError(kind === "video" ? "浏览器无法播放这个视频，请尝试 MP4 或 WebM 文件。" : "无法读取这张图片，请换一张 JPG 或 PNG 图片。");
      onChange();
    },
  };
}

export type LocalFileSlot = ReturnType<typeof useLocalFile>;

export function LocalFileCard({ kind, title, hint, optional, slot }: {
  kind: FileKind; title: string; hint: string; optional?: boolean; slot: LocalFileSlot;
}) {
  const input = useRef<HTMLInputElement>(null);
  const id = useId();
  const [dragging, setDragging] = useState(false);
  const format = formats[kind];
  const Icon = kind === "video" ? Film : kind === "image" ? ImagePlus : kind === "sheet" ? Sheet : FileText;
  const action = kind === "video" ? "选择视频" : kind === "image" ? "选择图片" : "选择文件";
  return (
    <section className="apex-upload-card apex-input-card" aria-labelledby={`${id}-title`}
      onDragOver={(event) => { event.preventDefault(); setDragging(true); }}
      onDragLeave={(event) => {
        if (!(event.relatedTarget instanceof Node) || !event.currentTarget.contains(event.relatedTarget)) setDragging(false);
      }}
      onDrop={(event) => { event.preventDefault(); setDragging(false); slot.pick(Array.from(event.dataTransfer.files)); }}>
      <div className="apex-upload-heading">
        <h2 id={`${id}-title`}>{title}{optional ? <small>选填</small> : null}</h2>
        {slot.media && !slot.error ? <span className="apex-file-ready"><Check aria-hidden="true" />已选择</span> : null}
      </div>
      <input ref={input} hidden type="file" accept={format.accept} aria-label={`${title}文件`}
        onChange={(event) => { slot.pick(Array.from(event.target.files ?? [])); event.target.value = ""; }} />
      <div className={`apex-dropzone${dragging ? " is-dragging" : ""}${slot.error ? " has-error" : ""}`}>
        {slot.media ? (
          <>
            <div className={`apex-media-preview${kind !== "video" ? " is-face" : ""}`}>
              {kind === "video" ? <video key={slot.media.url} src={slot.media.url} controls playsInline preload="metadata" aria-label="上传视频预览" onError={slot.fail} />
                : kind === "image" ? (
                  // Blob URLs must stay local and bypass remote image loaders.
                  // eslint-disable-next-line @next/next/no-img-element
                  <img src={slot.media.url} alt="上传图片预览" onError={slot.fail} />
                ) : <div className="apex-document-file"><Icon aria-hidden="true" /><strong>{slot.media.file.name}</strong><span>文件已选择</span></div>}
            </div>
            <div className="apex-file-bar">
              <div className="apex-file-name"><strong title={slot.media.file.name}>{slot.media.file.name}</strong><span>{fileSize(slot.media.file.size)} · 仅在本机预览</span></div>
              <Button type="button" size="sm" variant="outline" onClick={() => input.current?.click()}>替换</Button>
              <Button type="button" size="icon-sm" variant="ghost" aria-label="移除文件" onClick={slot.remove}><X aria-hidden="true" /></Button>
            </div>
          </>
        ) : <div className="apex-upload-empty">
          <span className="apex-upload-art" aria-hidden="true"><Icon /></span>
          <Button type="button" size="lg" variant="outline" className="apex-pick-button" aria-describedby={`${id}-hint ${id}-format`} onClick={() => input.current?.click()}><Upload aria-hidden="true" />{action}</Button>
          <p id={`${id}-hint`}>{hint}</p>
          <span id={`${id}-format`}>{format.label} · 预览上限 {format.maxMB} MB</span>
          <span className="apex-drop-hint">也可以把文件拖到这里</span>
        </div>}
      </div>
      {slot.error ? <p className="apex-upload-error" role="alert">{slot.error}</p> : null}
    </section>
  );
}
