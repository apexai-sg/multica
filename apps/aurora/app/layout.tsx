import type { Metadata, Viewport } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import { cn } from "@multica/ui/lib/utils";
import { RESOURCES } from "@multica/views/locales";
import { ThemeProvider } from "@/components/theme-provider";
import { AuroraProviders } from "@/components/aurora-providers";
import { getRequestLocale } from "@multica/nextjs/request-locale";
import { HTML_LANG } from "@multica/core/i18n/html-lang";
import {
  resolveBrowserApiBaseUrl,
  resolveBrowserWsUrl,
} from "@/config/runtime-urls";
import "./globals.css";

// Geist is the design system's Latin UI face (DESIGN.md typography.fontFamily).
// globals.css composes the full --font-sans stack around this variable.
const geistSans = Geist({
  subsets: ["latin"],
  style: ["normal", "italic"],
  variable: "--font-geist-sans",
});
// Mono has no explicit CJK fallback: CJK characters in a mono context are
// inherently non-aligned with the grid (Chinese is proportional), so listing CJK
// fonts here would falsely signal alignment guarantees.
const geistMono = Geist_Mono({
  subsets: ["latin"],
  variable: "--font-mono",
  fallback: [
    "ui-monospace",
    "SFMono-Regular",
    "Menlo",
    "Consolas",
    "monospace",
  ],
});

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  themeColor: "#f6f4ef",
};

export const metadata: Metadata = {
  title: "Apex AI · 创作中心",
  robots: { index: false, follow: false },
};

export default async function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const locale = await getRequestLocale();
  // Server and client must render from the same (locale, resources) pair or
  // hydration mismatches — so only the active locale's bundle is sent.
  const resources = { [locale]: RESOURCES[locale] };
  const apiBaseUrl = resolveBrowserApiBaseUrl(process.env);
  const wsUrl = resolveBrowserWsUrl(process.env);

  return (
    <html
      lang={HTML_LANG[locale]}
      suppressHydrationWarning
      className={cn(
        "antialiased font-sans h-full",
        geistSans.variable,
        geistMono.variable,
      )}
    >
      <body className="aurora-theme h-full overflow-hidden">
        {/* DESIGN.md defines one light canvas, including portalled UI. */}
        <ThemeProvider forcedTheme="light">
          <AuroraProviders
            locale={locale}
            resources={resources}
            apiBaseUrl={apiBaseUrl}
            wsUrl={wsUrl}
          >
            {children}
          </AuroraProviders>
        </ThemeProvider>
      </body>
    </html>
  );
}
