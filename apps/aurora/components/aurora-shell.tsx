"use client";

import { useEffect, useEffectEvent, useRef, useState } from "react";
import type { IconComponent } from "@multica/ui/icons";
import { Coins, History, Image, LogOut, Menu, Server, Sparkles, X } from "@multica/ui/icons";
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
 * The app's chrome: one navy column, five destinations, and the account.
 *
 * The account and wallet stay visible in the desktop sidebar. On compact
 * screens they live in the navigation sheet, with the account dialog nested
 * inside its focus scope.
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

  const shellRef = useRef<HTMLDivElement>(null);
  const closeAccountForResize = useEffectEvent(() => {
    setAccountOpen(false);
    return accountOpen;
  });

  useEffect(() => {
    // Match the stylesheet exactly; existing useIsMobile uses a different cutoff.
    const media = window.matchMedia("(max-width: 900px)");
    let focusFrame = 0;
    const sync = () => {
      cancelAnimationFrame(focusFrame);
      const interruptedAccount = closeAccountForResize();
      setCompact(media.matches);
      if (!media.matches) setNavOpen(false);
      if (interruptedAccount) {
        // The old opener is removed. Focus the new entry after modal cleanup.
        focusFrame = requestAnimationFrame(() => {
          shellRef.current?.querySelector<HTMLButtonElement>(
            media.matches ? ".aurora-menu-button" : ".aurora-more-button",
          )?.focus();
        });
      }
    };
    // Initial synchronization is not an interrupted account interaction.
    setCompact(media.matches);
    media.addEventListener("change", sync);
    return () => {
      media.removeEventListener("change", sync);
      cancelAnimationFrame(focusFrame);
    };
    // Effect Events read current state without resubscribing (not yet recognized by this lint version).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const workItems = [
    {
      href: routes.skills(),
      icon: Sparkles,
      label: t(($) => $.directory.title),
    },
    { href: routes.works(), icon: Image, label: t(($) => $.works.title) },
    { href: routes.history(), icon: History, label: t(($) => $.history.title) },
  ];
  const accountItems = [
    { href: routes.billing(), icon: Coins, label: t(($) => $.billing.title) },
  ];
  // The top bar names the current page, and the runtime owns a page of its own
  // even though it sits in the sidebar footer rather than a nav group.
  const contextItems = [
    ...workItems,
    ...accountItems,
    { href: routes.runtime(), label: t(($) => $.runtime.title) },
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
      aria-label="Apex AI"
      className="aurora-sidebar"
    >
      <div className="aurora-sidebar-top">
        <span className="aurora-brand">
          <span aria-hidden="true" className="aurora-brand-mark">
            A
          </span>
          <span className="aurora-brand-copy">
            <strong>Apex AI</strong>
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
        <AuroraAccountPanel
          creditsHref={routes.billing()}
          onOpenAccount={() => setAccountOpen(true)}
        />
        <AppLink
          href={routes.runtime()}
          className="aurora-nav-link"
          aria-current={selected(routes.runtime()) ? "page" : undefined}
        >
          <Server aria-hidden="true" />
          <span>{t(($) => $.runtime.title)}</span>
        </AppLink>
        <button type="button" className="aurora-nav-link" onClick={logout}>
          <LogOut aria-hidden="true" />
          {tLayout(($) => $.sidebar.log_out)}
        </button>
      </div>
    </nav>
  );

  const accountDialog = (
    <AuroraAccountDialog
      open={accountOpen}
      onOpenChange={setAccountOpen}
      creditsHref={routes.billing()}
    />
  );

  return (
    <div ref={shellRef} className="aurora-app">
      {compact ? null : sidebar}
      {compact ? null : accountDialog}
      <div className="aurora-main">
        <header className="aurora-topbar">
          {/* Scope the modal context to navigation, never to page dialogs. */}
          <Sheet open={compact && navOpen} onOpenChange={setNavOpen}>
            {compact ? (
              <SheetTrigger
                type="button"
                className="aurora-menu-button"
                aria-label={t(($) => $.shell.menu_open)}
              >
                <Menu aria-hidden="true" className="size-4" />
              </SheetTrigger>
            ) : null}
            {/* Keep the popup mounted through its exit lifecycle on resize. */}
            <SheetContent side="left" showCloseButton={false} className="aurora-nav-drawer">
              <SheetTitle className="sr-only">Apex AI</SheetTitle>
              {compact ? sidebar : null}
              {compact ? accountDialog : null}
            </SheetContent>
          </Sheet>
          <span className="aurora-topbar-brand">
            Apex <span>AI</span>
          </span>
          <div className="aurora-topbar-actions">
            {/* A generation can start from any page, so a runtime that is
                offline is surfaced here instead of only after a failed submit. */}
            <AuroraRuntimeChip href={routes.runtime()} />
            <span className="aurora-topbar-context">
              {contextItems.find((item) => selected(item.href))?.label ??
                t(($) => $.shell.brand_tagline)}
            </span>
          </div>
        </header>
        <main className="aurora-scroll">{children}</main>
      </div>
    </div>
  );
}

function NavLink({
  href,
  icon: Icon,
  label,
  selected,
}: {
  href: string;
  icon: IconComponent;
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
