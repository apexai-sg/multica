"use client";

import { useNavigation } from "@multica/views/navigation";
import { auroraRoutes } from "@/lib/routes";
import { ToolDirectory } from "./tool-directory";
import { directorySearch, readDirectorySelection } from "./directory-selection";

export function WorkspaceToolDirectory({ slug }: { slug: string }) {
  const navigation = useNavigation();
  const routes = auroraRoutes(slug);
  const selection = readDirectorySelection(navigation.searchParams);
  const search = directorySearch(selection);
  return <ToolDirectory
    selection={selection}
    onChange={(next) => navigation.replace(routes.skills() + directorySearch(next))}
    toolHref={(tool) => routes.tool(tool.id) + search}
    catalogHref={routes.catalog()}
  />;
}
