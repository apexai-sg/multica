"use client";

import { useState } from "react";
import { Copy, Download, FileText, MessageSquareText, ScanText, Sparkles } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { Textarea } from "@multica/ui/components/ui/textarea";

type TranscriptKind = "speech" | "screen";
type Segment = { start: number; end: number; text: string };

// Deliberately unrelated to selected local files; every display/export labels this as a sample.
const sample: Record<TranscriptKind, Segment[]> = {
  speech: [
    { start: 0, end: 4, text: "今天分享三个让产品照片更好看的小技巧。" },
    { start: 4, end: 9, text: "第一，找一扇有自然光的窗户，让光线从侧面照过来。" },
    { start: 9, end: 14, text: "第二，背景保持简单，让产品成为画面的重点。" },
    { start: 14, end: 20, text: "第三，拍一张整体图，再补几张细节图，就可以开始整理了。" },
  ],
  screen: [
    { start: 0, end: 4, text: "产品拍照的 3 个小技巧" },
    { start: 4, end: 9, text: "01 自然侧光" },
    { start: 9, end: 14, text: "02 简洁背景" },
    { start: 14, end: 20, text: "03 整体＋细节" },
  ],
};
const labels: Record<TranscriptKind, string> = { speech: "人物说话", screen: "画面文字" };
const shortTime = (seconds: number) => `${String(Math.floor(seconds / 60)).padStart(2, "0")}:${String(seconds % 60).padStart(2, "0")}`;
const srtTime = (seconds: number) => `${String(Math.floor(seconds / 3600)).padStart(2, "0")}:${shortTime(seconds % 3600)},000`;

export function TranscriptResult() {
  const [kind, setKind] = useState<TranscriptKind>("speech");
  const [segments, setSegments] = useState<Record<TranscriptKind, Segment[]> | null>(null);
  const [timestamps, setTimestamps] = useState(true);
  const [status, setStatus] = useState("");
  const rows = segments?.[kind] ?? [];
  const hasText = rows.some((row) => row.text.trim());

  function plainText() {
    return rows.filter((row) => row.text.trim()).map((row) => `${timestamps ? `[${shortTime(row.start)}–${shortTime(row.end)}] ` : ""}${row.text}`).join("\n\n");
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(plainText());
      setStatus(`已复制${labels[kind]}的示例文字。`);
    } catch {
      setStatus("复制未成功，请在文字框中选中文字后手动复制。");
    }
  }

  function download(format: "txt" | "srt") {
    const contents = format === "txt"
      ? `Apex AI 内置示例 · ${labels[kind]}（非上传视频的提取结果）\n\n${plainText()}`
      : rows.filter((row) => row.text.trim()).map((row, index) => `${index + 1}\n${srtTime(row.start)} --> ${srtTime(row.end)}\n${row.text}`).join("\n\n") + "\n";
    const url = URL.createObjectURL(new Blob(["\uFEFF", contents], { type: "text/plain;charset=utf-8" }));
    const link = document.createElement("a");
    link.href = url;
    link.download = `Apex-内置示例-${labels[kind]}.${format}`;
    document.body.append(link);
    link.click();
    link.remove();
    // Give the browser time to consume the local download URL.
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    setStatus(`已导出${labels[kind]}的 ${format.toUpperCase()} 示例。`);
  }

  return <section className="apex-transcript-result" aria-labelledby="transcript-title">
    <header className="apex-result-heading">
      <div><span className="apex-result-eyebrow">YOUR CONTENT</span><h2 id="transcript-title">提取结果</h2></div>
      <Button type="button" variant="outline" onClick={() => { setSegments(structuredClone(sample)); setStatus(""); }}><Sparkles aria-hidden="true" />查看示例结果</Button>
    </header>
    {segments ? <p className="apex-sample-note">内置示例 · 以下文字与上传的视频无关，可试用编辑、复制和导出。</p> : null}
    <Tabs value={kind} onValueChange={(value) => { if (value === "speech" || value === "screen") { setKind(value); setStatus(""); } }}>
      <div className="apex-transcript-toolbar">
        <TabsList aria-label="提取结果类型">
          <TabsTrigger value="speech"><MessageSquareText aria-hidden="true" />人物说话</TabsTrigger>
          <TabsTrigger value="screen"><ScanText aria-hidden="true" />画面文字</TabsTrigger>
        </TabsList>
        <label className="apex-timestamp-toggle"><input type="checkbox" checked={timestamps} onChange={(event) => setTimestamps(event.target.checked)} />显示时间点</label>
      </div>
      {(["speech", "screen"] as const).map((tab) => <TabsContent value={tab} key={tab}>
        {segments ? <div className="apex-transcript-lines">
          {segments[tab].map((row, index) => <div className="apex-transcript-line" key={`${tab}-${index}`}>
            {timestamps ? <span className="apex-transcript-time">{shortTime(row.start)}<span>— {shortTime(row.end)}</span></span> : null}
            <Textarea aria-label={`${labels[tab]}第 ${index + 1} 段`} value={row.text} rows={2} onChange={(event) => {
              setSegments({ ...segments, [tab]: segments[tab].map((item, rowIndex) => rowIndex === index ? { ...item, text: event.target.value } : item) });
              setStatus("");
            }} />
          </div>)}
        </div> : <div className="apex-transcript-empty">
          {tab === "speech" ? <MessageSquareText aria-hidden="true" /> : <ScanText aria-hidden="true" />}
          <h3>{tab === "speech" ? "人物说的话，会按段出现在这里" : "视频里的标题和字幕，会出现在这里"}</h3>
          <p>可以先查看示例，体验编辑和导出的页面效果。</p>
        </div>}
      </TabsContent>)}
    </Tabs>
    <footer className="apex-transcript-actions">
      <span><FileText aria-hidden="true" />{segments ? "可直接编辑上方示例文字" : "文字可编辑 · 复制 · 导出"}</span>
      <div>
        <Button type="button" variant="outline" disabled={!hasText} onClick={() => void copy()}><Copy aria-hidden="true" />复制文字</Button>
        <Button type="button" variant="outline" disabled={!hasText} onClick={() => download("txt")}><Download aria-hidden="true" />导出 TXT 示例</Button>
        <Button type="button" variant="outline" disabled={!hasText} onClick={() => download("srt")}><Download aria-hidden="true" />导出 SRT 示例</Button>
      </div>
    </footer>
    {status ? <p className="apex-copy-status" role="status">{status}</p> : null}
  </section>;
}
