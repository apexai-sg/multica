"use client";

import { useEffect, useState } from "react";
import type { LucideIcon } from "lucide-react";
import {
  Coins,
  History,
  Images,
  LogOut,
  Menu,
  Server,
  Sparkles,
  X,
} from "lucide-react";
import {
  AuroraAccountDialog,
  AuroraAccountPanel,
  AuroraRuntimeChip,
} from "@multica/views/aurora";
import { AppLink, useNavigation } from "@multica/views/navigation";
import { useT } from "@multica/views/i18n";
import { useLogout } from "@multica/views/auth";
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetTitle,
  SheetTrigger,
} from "@multica/ui/components/ui/sheet";
import { auroraRoutes } from "@/lib/routes";

/**
 * The app's chrome: one navy column, five destinations, one account action.
 *
 * A destination is a link, never a button — the shell is the only navigator in
 * Aurora, and a plain anchor keeps middle-click, "copy link" and the browser's
 * own history semantics working. Labels come from the `aurora` namespace so the
 * nav and the page it opens always name the same thing.
 *
 * At 900px and below, the column lives in a modal Sheet. The primitive owns
 * initial focus, Tab containment, Escape, outside dismissal and focus return
 * to the SheetTrigger. Only one copy of the navigation is mounted.
 */
export function AuroraShell({
  slug,
  children,
}: {
  slug: string;
  children: React.ReactNode;
}) {
  const { t } = useT("aurora");
  const { t: tLayout } = useT("layout");
  const { pathname } = useNavigation();
  const logout = useLogout();
  const routes = auroraRoutes(slug);
  const [navOpen, setNavOpen] = useState(false);
  const [compact, setCompact] = useState(false);
  const [accountOpen, setAccountOpen] = useState(false);

  useEffect(() => {
    // Match the stylesheet exactly; existing useIsMobile uses a different cutoff.
    const media = window.matchMedia("(max-width: 900px)");
    const sync = () => {
      setCompact(media.matches);
      if (!media.matches) setNavOpen(false);
    };
    sync();
    media.addEventListener("change", sync);
    return () => media.removeEventListener("change", sync);
  }, []);

  const workItems = [
    {
      href: routes.skills(),
      icon: Sparkles,
      label: t(($) => $.directory.title),
    },
    { href: routes.works(), icon: Images, label: t(($) => $.works.title) },
    { href: routes.history(), icon: History, label: t(($) => $.history.title) },
  ];
  const accountItems = [
    { href: routes.runtime(), icon: Server, label: t(($) => $.runtime.title) },
    { href: routes.billing(), icon: Coins, label: t(($) => $.billing.title) },
  ];
  const worksHref = routes.works();
  // Trailing slashes are the same destination, and Next keeps the URL the user
  // typed. Comparing raw strings would drop the selected state for `…/works/`.
  const current = pathname.replace(/\/+$/, "");
  const selected = (href: string) =>
    current === href ||
    // The works destination owns the per-generation detail route below it
    // (…/works/{generationId}), so it stays selected while one is open; every
    // other destination matches its own path exactly.
    (href === worksHref && current.startsWith(worksHref + "/"));

  // A committed route closes both overlays: each one covers the page it just
  // opened. `pathname` is the dependency because the adapter reports the route
  // the app landed on, not the click that started it.
  useEffect(() => {
    setNavOpen(false);
    setAccountOpen(false);
  }, [pathname]);

  const sidebar = (
    <nav
      id="aurora-nav"
      aria-label="Aurora"
      className="aurora-sidebar"
    >
      <div className="aurora-sidebar-top">
        <span className="aurora-brand">
          <span aria-hidden="true" className="aurora-brand-mark">
            A
          </span>
          <span className="aurora-brand-copy">
            <strong>Aurora</strong>
            <small>{t(($) => $.shell.brand_tagline)}</small>
          </span>
        </span>
        {compact ? (
          <SheetClose
            type="button"
            className="aurora-sidebar-close"
            aria-label={t(($) => $.shell.menu_close)}
          >
            <X aria-hidden="true" className="size-4" />
          </SheetClose>
        ) : null}
      </div>

      <AuroraAccountPanel
        creditsHref={routes.billing()}
        onOpenAccount={() => setAccountOpen(true)}
      />

      <div className="aurora-nav-groups">
        <div className="aurora-nav-group">
          <p>{t(($) => $.shell.nav_group_work)}</p>
          <ul>
            {workItems.map((item) => (
              <NavLink
                key={item.href}
                {...item}
                selected={selected(item.href)}
              />
            ))}
          </ul>
        </div>
        <div className="aurora-nav-group">
          <p>{t(($) => $.shell.nav_group_account)}</p>
          <ul>
            {accountItems.map((item) => (
              <NavLink
                key={item.href}
                {...item}
                selected={selected(item.href)}
              />
            ))}
          </ul>
        </div>
      </div>

      <div className="aurora-sidebar-footer">
        <button type="button" className="aurora-nav-link" onClick={logout}>
          <LogOut aria-hidden="true" />
          {tLayout(($) => $.sidebar.log_out)}
        </button>
      </div>
    </nav>
  );

  return (
    <Sheet open={compact && navOpen} onOpenChange={setNavOpen}>
      <div className="aurora-app">
        {/* Keep the primitive mounted through its exit lifecycle on resize;
            removing the popup early leaves its internal mounted state behind. */}
        <SheetContent side="left" showCloseButton={false} className="aurora-nav-drawer">
          <SheetTitle className="sr-only">Aurora</SheetTitle>
          {compact ? sidebar : null}
        </SheetContent>
        {compact ? null : sidebar}

        <div className="aurora-main">
          <header className="aurora-topbar">
            {compact ? (
              <SheetTrigger
                type="button"
                className="aurora-menu-button"
                aria-label={t(($) => $.shell.menu_open)}
              >
                <Menu aria-hidden="true" className="size-4" />
              </SheetTrigger>
            ) : null}
            <span className="aurora-topbar-brand">
              Aurora <span>AI</span>
            </span>
            <div className="aurora-topbar-actions">
              {/* The runtime state belongs in the chrome: every page here starts a
                  generation, and a node that is offline is the reason the next
                  one will not. */}
              <AuroraRuntimeChip href={routes.runtime()} />
            </div>
          </header>
          <main className="aurora-scroll">{children}</main>
        </div>

        <AuroraAccountDialog
          open={accountOpen}
          onOpenChange={setAccountOpen}
          creditsHref={routes.billing()}
        />
      </div>
    </Sheet>
  );
}

function NavLink({
  href,
  icon: Icon,
  label,
  selected,
}: {
  href: string;
  icon: LucideIcon;
  label: string;
  selected: boolean;
}) {
  return (
    <li>
      <AppLink
        href={href}
        aria-current={selected ? "page" : undefined}
        className="aurora-nav-link"
      >
        <Icon aria-hidden="true" />
        <span className="truncate">{label}</span>
      </AppLink>
    </li>
  );
}
