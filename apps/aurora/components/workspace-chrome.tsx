"use client";

import type { ReactNode } from "react";
import { useNavigation } from "@multica/views/navigation";
import { auroraRoutes } from "@/lib/routes";
import { AuroraShell } from "./aurora-shell";
import { ToolFrame } from "./tools/tool-frame";
import { directorySearch, readDirectorySelection } from "./tools/directory-selection";

export function WorkspaceChrome({ slug, userName, children }: {
  slug: string;
  userName?: string;
  children: ReactNode;
}) {
  const { pathname, searchParams } = useNavigation();
  const routes = auroraRoutes(slug);
  if (pathname.startsWith(`${routes.tools()}/`)) {
    const search = directorySearch(readDirectorySelection(searchParams));
    return <ToolFrame directoryHref={routes.skills() + search} userName={userName}>{children}</ToolFrame>;
  }
  return <AuroraShell slug={slug}>{children}</AuroraShell>;
}
