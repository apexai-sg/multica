// @vitest-environment node
import { describe, expect, it } from "vitest";

import {
  resolveBrowserApiBaseUrl,
  resolveBrowserWsUrl,
  resolveDevRemoteApiUrl,
  resolveRemoteApiUrl,
  resolveWebOrigin,
  runtimeRewriteDestination,
} from "./runtime-urls";

describe("resolveRemoteApiUrl", () => {
  it("prefers REMOTE_API_URL when explicitly configured", () => {
    expect(
      resolveRemoteApiUrl({
        REMOTE_API_URL: "http://backend:8080",
        NEXT_PUBLIC_API_URL: "http://localhost:19000",
        PORT: "18080",
      }),
    ).toBe("http://backend:8080");
  });

  it("uses NEXT_PUBLIC_API_URL when REMOTE_API_URL is unset", () => {
    expect(
      resolveRemoteApiUrl({ NEXT_PUBLIC_API_URL: "http://localhost:19000" }),
    ).toBe("http://localhost:19000");
  });

  it("does not infer a backend URL from port env vars", () => {
    expect(
      resolveRemoteApiUrl({
        BACKEND_PORT: "28080",
        API_PORT: "38080",
        SERVER_PORT: "48080",
        PORT: "3000",
      }),
    ).toBeUndefined();
  });

  it("ignores a relative public API URL", () => {
    expect(resolveRemoteApiUrl({ NEXT_PUBLIC_API_URL: "/api" })).toBeUndefined();
  });

  it("ignores whitespace-only or non-http values", () => {
    expect(
      resolveRemoteApiUrl({
        REMOTE_API_URL: "  ",
        NEXT_PUBLIC_API_URL: "ftp://api.example.com",
      }),
    ).toBeUndefined();
  });

  // The rewrite target already carries the full incoming pathname
  // (`/api/**`, `/uploads/**`, `/ws`), so a configured `/api` suffix would
  // double it and 404 every request.
  it("strips a trailing /api from the configured origin", () => {
    expect(
      resolveRemoteApiUrl({ REMOTE_API_URL: "http://backend:8080/api" }),
    ).toBe("http://backend:8080");
  });

  it("keeps a non-/api path prefix so a prefix-mounted backend still works", () => {
    expect(
      resolveRemoteApiUrl({ REMOTE_API_URL: "https://host.test/aurora" }),
    ).toBe("https://host.test/aurora");
  });
});

describe("browser runtime URLs", () => {
  it("exposes an absolute public API URL to the browser", () => {
    expect(
      resolveBrowserApiBaseUrl({ NEXT_PUBLIC_API_URL: "https://api.test" }),
    ).toBe("https://api.test");
  });

  it("rejects a relative public API URL so the browser stays same-origin", () => {
    expect(resolveBrowserApiBaseUrl({ NEXT_PUBLIC_API_URL: "/api" })).toBe(
      undefined,
    );
  });

  it("derives the websocket URL from the API origin", () => {
    expect(resolveBrowserWsUrl({ NEXT_PUBLIC_API_URL: "https://api.test" })).toBe(
      "wss://api.test/ws",
    );
  });

  it("derives the websocket URL once the /api suffix is stripped", () => {
    expect(
      resolveBrowserWsUrl({ NEXT_PUBLIC_API_URL: "https://api.test/api" }),
    ).toBe("wss://api.test/ws");
  });

  it("prefers an explicit websocket URL", () => {
    expect(
      resolveBrowserWsUrl({
        NEXT_PUBLIC_API_URL: "https://api.test",
        NEXT_PUBLIC_WS_URL: "wss://socket.test/ws",
      }),
    ).toBe("wss://socket.test/ws");
  });
});

describe("resolveWebOrigin", () => {
  it("returns the configured Multica web origin", () => {
    expect(
      resolveWebOrigin({ NEXT_PUBLIC_WEB_ORIGIN: "https://mca.example" }),
    ).toBe("https://mca.example");
  });

  it("strips a trailing slash so the issue path can be appended", () => {
    expect(
      resolveWebOrigin({ NEXT_PUBLIC_WEB_ORIGIN: "https://mca.example/" }),
    ).toBe("https://mca.example");
  });

  it("ignores an unset, relative or non-http value", () => {
    expect(resolveWebOrigin({})).toBeUndefined();
    expect(resolveWebOrigin({ NEXT_PUBLIC_WEB_ORIGIN: "" })).toBeUndefined();
    expect(
      resolveWebOrigin({ NEXT_PUBLIC_WEB_ORIGIN: "mca.example" }),
    ).toBeUndefined();
  });
});

describe("runtimeRewriteDestination", () => {
  const env = { REMOTE_API_URL: "http://backend:8080" };

  it("maps backend HTTP paths to the configured origin", () => {
    expect(runtimeRewriteDestination("/api/aurora/skills", env)).toBe(
      "http://backend:8080/api/aurora/skills",
    );
    expect(runtimeRewriteDestination("/v1/context", env)).toBe(
      "http://backend:8080/v1/context",
    );
    expect(runtimeRewriteDestination("/uploads/workspaces/a.png", env)).toBe(
      "http://backend:8080/uploads/workspaces/a.png",
    );
    expect(runtimeRewriteDestination("/ws", env)).toBe(
      "http://backend:8080/ws",
    );
  });

  it("maps the CLI health probe", () => {
    expect(runtimeRewriteDestination("/health", env)).toBe(
      "http://backend:8080/health",
    );
  });

  it("leaves this app's own auth callback alone", () => {
    // The browser has to reach the app's route, not the backend's handler.
    expect(runtimeRewriteDestination("/auth/callback", env)).toBeUndefined();
    expect(
      runtimeRewriteDestination("/auth/callback/extra", env),
    ).toBeUndefined();
    expect(runtimeRewriteDestination("/auth/login", env)).toBe(
      "http://backend:8080/auth/login",
    );
  });

  it("leaves app routes alone", () => {
    expect(runtimeRewriteDestination("/acme/skills", env)).toBeUndefined();
    expect(runtimeRewriteDestination("/login", env)).toBeUndefined();
    expect(runtimeRewriteDestination("/", env)).toBeUndefined();
  });

  it("rewrites nothing when no origin is configured", () => {
    expect(runtimeRewriteDestination("/api/aurora/skills", {})).toBeUndefined();
  });
});

describe("dev fallback", () => {
  it("assumes the conventional local backend port", () => {
    expect(resolveDevRemoteApiUrl({})).toBe("http://localhost:8080");
  });

  it("honors backend-specific port aliases", () => {
    expect(resolveDevRemoteApiUrl({ BACKEND_PORT: "28080" })).toBe(
      "http://localhost:28080",
    );
    expect(resolveDevRemoteApiUrl({ API_PORT: "38080" })).toBe(
      "http://localhost:38080",
    );
  });

  // Next writes process.env.PORT with this app's own listener port before it
  // evaluates next.config.ts; treating it as a backend port would point every
  // dev rewrite back at this app.
  it("ignores the frontend process PORT", () => {
    expect(resolveDevRemoteApiUrl({ PORT: "3001" })).toBe(
      "http://localhost:8080",
    );
  });

  it("prefers a configured origin over the fallback", () => {
    expect(
      resolveDevRemoteApiUrl({ REMOTE_API_URL: "http://backend:9000" }),
    ).toBe("http://backend:9000");
  });
});
