"use client";

import { useState } from "react";
import { ArrowRight, Check, FileText, Film, Globe, ImagePlus, Monitor, Play, Presentation, Sheet, Smartphone, Sparkles, Volume2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { LocalFileCard, useLocalFile } from "./local-file";
import { TranscriptResult } from "./transcript-result";
import { toolCategories, type ToolDefinition } from "./tool-definitions";
import { toolSubmissionNotice, useToolSubmission } from "./use-tool-submission";

const resultIcons = { image: ImagePlus, video: Film, slides: Presentation, table: Sheet, document: FileText, audio: Volume2, website: Globe, transcript: FileText };

export function ToolWorkbench({ tool, slug }: { tool: ToolDefinition; slug: string }) {
  const [source, setSource] = useState("");
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(tool.fields.map((field) => [field.id, field.options[0] ?? ""])));
  const [resultVersion, setResultVersion] = useState(0);
  const clearResult = () => { setResultVersion((value) => value + 1); };
  const file = useLocalFile(tool.upload?.kind ?? "image", clearResult);
  const ready = (!tool.upload || tool.upload.optional || !!file.media) && !file.error &&
    (!tool.source || tool.source.optional || !!source.trim());
  const submission = useToolSubmission(tool, slug);
  const Icon = resultIcons[tool.result.kind];

  return <main className="apex-face-workbench apex-config-workbench" onDragOver={(event) => event.preventDefault()} onDrop={(event) => event.preventDefault()}>
    <section className="apex-tool-hero" aria-labelledby="apex-tool-title">
      <div className="apex-tool-avatar">
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img src={`/avatars/person-${tool.avatar}.svg`} alt="" />
        <span aria-hidden="true"><Sparkles /></span>
      </div>
      <div className="apex-tool-intro">
        <span className="apex-tool-eyebrow">{toolCategories.find((category) => category.id === tool.category)?.name} · 你的专属工具人</span>
        <h1 id="apex-tool-title">{tool.name}</h1><p>{tool.tagline}</p>
      </div>
      <div className="apex-hero-formula" aria-hidden="true"><span><Icon /></span><ArrowRight /><span><Sparkles /></span></div>
    </section>
    <ol className="apex-workbench-steps"><li><span>01</span>准备素材</li><li><span>02</span>选择效果</li><li><span>03</span>{tool.result.kind === "transcript" ? "提取与导出" : "预览与导出"}</li></ol>
    <div className="apex-workbench-grid">
      <div className="apex-input-stack">
        {tool.upload ? <LocalFileCard {...tool.upload} slot={file} /> : null}
        {tool.source ? <section className="apex-upload-card apex-source-card" aria-labelledby="source-title">
          <div className="apex-upload-heading"><h2 id="source-title"><label htmlFor="tool-source">{tool.source.label}</label>{tool.source.optional ? <small>选填</small> : null}</h2></div>
          <Textarea id="tool-source" className="apex-source-text" placeholder={tool.source.placeholder} value={source} onChange={(event) => { setSource(event.target.value); clearResult(); }} rows={tool.upload ? 4 : 8} />
          <Button type="button" variant="ghost" size="sm" className="apex-example-button" onClick={() => { setSource(tool.source!.example); clearResult(); }}><Sparkles aria-hidden="true" />填入示例文案</Button>
        </section> : null}
      </div>
      <section className="apex-upload-card apex-settings-card" aria-labelledby="settings-title">
        <div className="apex-upload-heading"><h2 id="settings-title">{tool.result.kind === "transcript" ? "提取设置" : "效果设置"}</h2><span className="apex-settings-tag">按你的需要选择</span></div>
        {tool.fields.map((field) => <fieldset className="apex-choice-field" key={field.id}>
          <legend>{field.label}</legend>
          <div>
            {field.options.map((option) => <Button type="button" key={option} variant="outline" aria-pressed={values[field.id] === option} onClick={() => { setValues({ ...values, [field.id]: option }); clearResult(); }}>
              {values[field.id] === option ? <Check aria-hidden="true" /> : null}{option}
            </Button>)}
          </div>
        </fieldset>)}
        <div className="apex-output-summary"><Icon aria-hidden="true" /><div><strong>{tool.result.title}</strong><p>{tool.result.items.join(" · ")}</p></div></div>
      </section>
    </div>
    <div className="apex-generate-area">
      <Button type="button" size="lg" className="apex-generate-button" disabled={!ready || !submission.canSubmit} aria-describedby="tool-preview-note" onClick={() => {
        void submission.submit({ source, values, files: file.media ? [file.media.file] : [] });
      }}>
        {submission.pending ? <Spinner aria-hidden="true" /> : <Sparkles aria-hidden="true" />}
        {submission.pending ? "提交中…" : tool.action}
        {submission.pending ? null : <ArrowRight aria-hidden="true" />}
      </Button>
      <p id="tool-preview-note">{ready ? "素材和设置已准备好" : "请先补充必填素材或文案"}<span>·</span>{toolSubmissionNotice(submission.status)}
        {submission.status === "error" ? <><span>·</span><Button type="button" variant="outline" size="sm" onClick={submission.reloadAgents}>重新加载</Button></> : null}
      </p>
      {submission.error ? <p className="apex-upload-error" role="alert">{submission.error}</p> : null}
      {submission.createdIdentifier ? <p className="apex-sample-note" role="status">任务已创建：{submission.createdIdentifier}，请到任务列表查看。</p> : null}
    </div>
    <div>
      <p className="apex-sample-note">提交后会自动打开该任务的问题详情页；AI 的执行结果以评论和附件的形式显示在那里。若无法生成真实媒体文件，结果中会说明限制并给出方案。</p>
      {tool.result.kind === "transcript" ? <TranscriptResult key={resultVersion} /> : <ResultPreview tool={tool} />}
    </div>
    <footer className="apex-workbench-footer">APEX AI <span>让创作，简单一点。</span></footer>
  </main>;
}

function ResultPreview({ tool }: { tool: ToolDefinition }) {
  const [screen, setScreen] = useState("desktop");
  const Icon = resultIcons[tool.result.kind];
  return <section className={`apex-output-panel apex-output-panel--${tool.result.kind}`} aria-labelledby="output-title">
    <header className="apex-result-heading"><div><span className="apex-result-eyebrow">YOUR CREATION</span><h2 id="output-title">{tool.result.title}</h2></div><span className="apex-placeholder-label">结果区布局预览</span></header>
    <div className="apex-output-body">
      <div className="apex-output-visual" aria-label={`${tool.result.title}占位区`}>
        {tool.result.kind === "image" ? <div className="apex-image-slots">{[0, 1, 2].map((index) => <div key={index}><ImagePlus aria-hidden="true" /><span>{index === 0 && tool.id === "image-retouch" ? "原图" : `方案 ${index + 1}`}</span></div>)}</div>
          : tool.result.kind === "slides" ? <div className="apex-slide-layout"><div className="apex-slide-thumbs">{[1, 2, 3].map((index) => <span key={index}>{String(index).padStart(2, "0")}</span>)}</div><div className="apex-slide-stage"><Presentation aria-hidden="true" /><span>幻灯片预览</span></div></div>
          : tool.result.kind === "table" ? <div className="apex-table-placeholder"><table><caption>结果表格占位</caption><thead><tr>{["项目", "分类", "数值", "备注"].map((label) => <th key={label}>{label}</th>)}</tr></thead><tbody>{[1, 2, 3].map((index) => <tr key={index}>{[1, 2, 3, 4].map((cell) => <td key={cell}>—</td>)}</tr>)}</tbody></table></div>
          : tool.result.kind === "website" ? <div className="apex-website-placeholder"><div className="apex-device-switch" role="group" aria-label="网站预览尺寸"><Button type="button" variant="ghost" aria-pressed={screen === "desktop"} onClick={() => setScreen("desktop")}><Monitor aria-hidden="true" />电脑</Button><Button type="button" variant="ghost" aria-pressed={screen === "mobile"} onClick={() => setScreen("mobile")}><Smartphone aria-hidden="true" />手机</Button></div><div className={`apex-website-frame${screen === "mobile" ? " is-mobile" : ""}`} aria-label={`${screen === "mobile" ? "手机" : "电脑"}布局占位`}><div /><Globe aria-hidden="true" /><span>网站布局预览</span><i /><i /></div></div>
          : tool.result.kind === "audio" ? <div className="apex-audio-placeholder"><Volume2 aria-hidden="true" /><div aria-hidden="true">{[12, 24, 36, 20, 40, 28, 14, 32, 44, 24, 16, 28, 38, 20, 12].map((height, index) => <i key={index} style={{ height }} />)}</div><span>配音生成后可试听</span></div>
          : tool.result.kind === "video" ? <div className="apex-video-placeholder"><Play aria-hidden="true" /><span>视频生成后可播放</span><div /></div>
          : <div className="apex-document-placeholder"><FileText aria-hidden="true" /><span>内容将在这里出现</span><i /><i /><i /></div>}
      </div>
      <div className="apex-output-explanation"><Icon aria-hidden="true" /><p>{tool.result.description}</p><div className="aurora-chips">{tool.result.items.map((item) => <span key={item}>{item}</span>)}</div><span className="apex-output-pending">生成后可使用对应的导出功能</span></div>
    </div>
  </section>;
}
