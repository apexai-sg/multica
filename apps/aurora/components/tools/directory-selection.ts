import { toolCategories } from "./tool-definitions";
import type { DirectorySelection } from "./tool-directory";

export function readDirectorySelection(params: URLSearchParams): DirectorySelection {
  const category = toolCategories.find((item) => item.id === params.get("category"))?.id ?? "all";
  return { category, query: params.get("q") ?? "" };
}

export function directorySearch(selection: DirectorySelection): string {
  const params = new URLSearchParams();
  if (selection.category !== "all") params.set("category", selection.category);
  if (selection.query) params.set("q", selection.query);
  const query = params.toString();
  return query ? `?${query}` : "";
}
