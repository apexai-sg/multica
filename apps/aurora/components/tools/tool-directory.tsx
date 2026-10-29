"use client";

import { ArrowUpRight, Search, SearchX, Sparkles } from "lucide-react";
import { AppLink } from "@multica/views/navigation";
import { Button } from "@multica/ui/components/ui/button";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@multica/ui/components/ui/input-group";
import { apexTools, toolCategories, type ToolCategory, type ToolDefinition } from "./tool-definitions";

export type DirectorySelection = { category: ToolCategory | "all"; query: string };

export function ToolDirectory({ selection, onChange, toolHref, catalogHref }: {
  selection: DirectorySelection;
  onChange: (selection: DirectorySelection) => void;
  toolHref: (tool: ToolDefinition) => string;
  catalogHref?: string;
}) {
  const query = selection.query.trim().toLocaleLowerCase();
  const visible = apexTools.filter((tool) =>
    (selection.category === "all" || tool.category === selection.category) &&
    [tool.name, tool.description, ...tool.chips].join(" ").toLocaleLowerCase().includes(query),
  );
  return (
    <div className="aurora-page apex-directory">
      <section className="aurora-hero">
        <div>
          <span className="aurora-hero-label">你的 AI 创作工具箱</span>
          <h1>想做什么，<span>直接点什么。</span></h1>
          <p className="aurora-hero-description">图片、视频、文案和办公，每一件事都有专属工具人。</p>
          <div className="aurora-hero-stats">
            <span><strong>{apexTools.length}</strong>个工具方案</span>
            <span><strong>{toolCategories.length}</strong>类使用场景</span>
            <span><strong>全屏</strong>专属工作台</span>
          </div>
        </div>
        <div className="aurora-hero-orbit" aria-hidden="true">
          <div className="aurora-hero-center"><Sparkles /></div>
          {apexTools.slice(0, 4).map((tool, index) => <span key={tool.id} className={`aurora-orbit-avatar aurora-orbit-avatar--${index + 1}`}>
            <ToolAvatar tool={tool} />
          </span>)}
        </div>
      </section>
      <div className="apex-directory-heading">
        <div><span>AI TOOLS</span><h2>选择你要使用的功能</h2></div>
        <div className="apex-directory-links">
          <p role="status">{visible.length} 个工具{selection.category !== "all" || query ? "匹配" : "方案"}</p>
          {catalogHref ? <AppLink href={catalogHref}>现有工具库<ArrowUpRight aria-hidden="true" /></AppLink> : null}
        </div>
      </div>
      <div className="aurora-directory-toolbar">
        <div className="aurora-directory-filters" role="group" aria-label="工具分类">
          {[{ id: "all", name: "全部功能" }, ...toolCategories].map((category) => <Button key={category.id} type="button" variant="outline" className="aurora-directory-filter"
            aria-pressed={selection.category === category.id}
            onClick={() => onChange({ ...selection, category: category.id as DirectorySelection["category"] })}>{category.name}</Button>)}
        </div>
        <InputGroup className="apex-directory-search">
          <InputGroupAddon><Search aria-hidden="true" /></InputGroupAddon>
          <InputGroupInput type="search" aria-label="搜索工具" placeholder="搜索工具或想做的事"
            value={selection.query} onChange={(event) => onChange({ ...selection, query: event.target.value })} />
        </InputGroup>
      </div>
      {visible.length ? <ul className="aurora-card-grid">
        {visible.map((tool) => <li key={tool.id}>
          <AppLink href={toolHref(tool)} className={`aurora-card apex-tool-card${tool.id === "video-transcript" ? " apex-tool-card--featured" : ""}`} aria-label={`打开${tool.name}`}>
            <span className="aurora-card-top">
              <ToolAvatar tool={tool} large />
              <span className="aurora-card-name">
                <span className="aurora-card-kind">{toolCategories.find((category) => category.id === tool.category)?.name}</span>
                <span className="aurora-card-title">{tool.name}</span>
              </span>
              <span className="aurora-card-arrow" aria-hidden="true"><ArrowUpRight /></span>
            </span>
            <span className="aurora-card-description">{tool.description}</span>
            <span className="aurora-chips">{tool.chips.map((chip) => <span key={chip}>{chip}</span>)}</span>
            <span className="aurora-card-footer"><span>打开全屏工作台</span>{tool.added ? <span className="apex-new-label">本次新增</span> : <ArrowUpRight aria-hidden="true" />}</span>
          </AppLink>
        </li>)}
      </ul> : <div className="apex-directory-empty"><SearchX aria-hidden="true" /><h3>没有找到相关工具</h3><Button type="button" variant="outline" onClick={() => onChange({ category: "all", query: "" })}>查看全部工具</Button></div>}
    </div>
  );
}

function ToolAvatar({ tool, large = false }: { tool: ToolDefinition; large?: boolean }) {
  return <span aria-hidden="true" className={`aurora-avatar${large ? " aurora-avatar--lg" : ""}`}>
    {/* Bundled portraits belong to the frontend origin, not the upload API. */}
    {/* eslint-disable-next-line @next/next/no-img-element */}
    <img className="aurora-avatar-img" src={tool.avatar} alt="" />
  </span>;
}
