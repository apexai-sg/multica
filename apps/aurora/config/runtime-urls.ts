/**
 * Where this app's own origin proxies to, and what the browser should treat as
 * the API/WebSocket origin.
 *
 * Deliberately mirrors `apps/web/config/runtime-urls.ts` minus the parts only
 * the marketing site needs (docs upstream, official-host special cases). The
 * upstream rules are load-bearing and easy to break, so they are copied rather
 * than re-derived — see the comments on `stripApiPathSuffix` and
 * `tryDeriveWsUrl` for the regressions behind them.
 */
type RuntimeEnv = Record<string, string | undefined>;

function cleanUrl(raw: string | undefined): string | undefined {
  const value = raw?.trim();
  if (!value) return undefined;
  return value.replace(/\/+$/, "");
}

function cleanHttpUrl(raw: string | undefined): string | undefined {
  const value = cleanUrl(raw);
  if (!value) return undefined;

  try {
    const url = new URL(value);
    if (url.protocol === "http:" || url.protocol === "https:") return value;
  } catch {
    return undefined;
  }

  return undefined;
}

// The API base names the backend ORIGIN, never its `/api` endpoint: every
// caller already carries its own prefix (`packages/core/api/client.ts` sends
// `/api/**`, avatars resolve `/uploads/**`, realtime connects `/ws`), and the
// backend serves all three at the root. A base ending in `/api` therefore
// yields `/api/api/**` requests and 404s every upload — the most common
// self-hosting mistake. Strip that one suffix instead of honouring it; any
// other path is preserved because a reverse proxy may legitimately mount the
// whole backend under a prefix such as `https://host/multica`.
function stripApiPathSuffix(value: string): string {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    return value;
  }
  const pathname = url.pathname.replace(/\/+$/, "");
  if (!pathname.endsWith("/api")) return value;
  url.pathname = pathname.slice(0, -"/api".length);
  url.search = "";
  url.hash = "";
  return url.toString().replace(/\/+$/, "");
}

function cleanApiBaseUrl(raw: string | undefined): string | undefined {
  const value = cleanHttpUrl(raw);
  if (!value) return undefined;
  return stripApiPathSuffix(value);
}

function appendPath(baseUrl: string, path: string): string {
  return `${baseUrl}${path.startsWith("/") ? path : `/${path}`}`;
}

/** The backend origin a server-side rewrite should target, or undefined. */
export function resolveRemoteApiUrl(env: RuntimeEnv): string | undefined {
  const explicitRemote = cleanApiBaseUrl(env.REMOTE_API_URL);
  if (explicitRemote) return explicitRemote;

  return cleanApiBaseUrl(env.NEXT_PUBLIC_API_URL);
}

// Dev-only fallback: `next dev` runs on a developer machine, where the
// conventional localhost backend port is safe to assume when nothing is
// configured. Builds and the runtime proxy keep the strict resolver so a
// prebuilt image never guesses an origin.
export function resolveDevRemoteApiUrl(env: RuntimeEnv): string {
  const configured = resolveRemoteApiUrl(env);
  if (configured) return configured;
  // Next writes process.env.PORT with the frontend listener port before it
  // evaluates next.config.ts. Treating that generic variable as a backend port
  // would make every dev rewrite point back at this app itself, so only the
  // backend-specific aliases are safe fallbacks here.
  const backendPort =
    env.BACKEND_PORT?.trim() ||
    env.API_PORT?.trim() ||
    env.SERVER_PORT?.trim() ||
    "8080";
  return `http://localhost:${backendPort}`;
}

// Same strictness as the server-side resolver above: returning undefined makes
// the browser fall back to same-origin relative paths, which is what an unset
// value already does. A relative or non-http value used to pass through
// untouched and became the XHR base.
export function resolveBrowserApiBaseUrl(env: RuntimeEnv): string | undefined {
  return cleanApiBaseUrl(env.NEXT_PUBLIC_API_URL);
}

export function resolveBrowserWsUrl(env: RuntimeEnv): string | undefined {
  const explicit = cleanUrl(env.NEXT_PUBLIC_WS_URL);
  if (explicit) return explicit;

  const apiUrl = resolveBrowserApiBaseUrl(env);
  return apiUrl ? tryDeriveWsUrl(apiUrl) : undefined;
}

/**
 * The Multica web app whose issue detail Aurora opens after submit, or
 * undefined. Unset means the issue renders inside Aurora at the same path, so
 * the fallback stays an empty value rather than a guessed origin. Only an
 * http(s) origin is usable for a cross-app hand-off.
 */
export function resolveWebOrigin(env: RuntimeEnv): string | undefined {
  return cleanHttpUrl(env.NEXT_PUBLIC_WEB_ORIGIN);
}

// Paths the backend serves as a tree: anything beneath them belongs to it too.
const BACKEND_PATH_TREES = ["/v1", "/api", "/uploads"];
// Endpoints it serves at exactly one path — `/ws` for the realtime handshake,
// `/health` for `multica setup self-host`, which probes `{server-url}/health`
// and treats any non-200 as "Server not reachable". The backend serves it, but a
// same-origin reverse proxy that forwards everything here would leave the probe
// 404ing at the Next.js router, so the exact path is proxied like /ws.
const BACKEND_EXACT_PATHS = ["/ws", "/health"];

/**
 * Whether this path belongs to the backend surface rather than to this app.
 *
 * One list, because two callers depend on the same answer and disagreeing about
 * it is a bug in both directions: `runtimeRewriteDestination` sends these paths
 * upstream, and `proxy.ts` must not let its reserved-slug redirect claim them —
 * `api`, `v1`, `ws`, `health` and `uploads` are all reserved slugs, so a
 * redirect that only looked at the first segment would answer the API, uploads
 * and the realtime handshake with a 307 to the app root.
 */
export function isBackendSurfacePath(pathname: string): boolean {
  if (isBackendAuthPath(pathname)) return true;
  if (BACKEND_EXACT_PATHS.includes(pathname)) return true;
  return BACKEND_PATH_TREES.some(
    (root) => pathname === root || pathname.startsWith(`${root}/`),
  );
}

/**
 * The upstream origin for a same-origin path the Next.js server should proxy
 * at request time (production). Returns undefined when nothing is configured
 * or the path is served by the app itself.
 */
export function runtimeRewriteDestination(
  pathname: string,
  env: RuntimeEnv,
): string | undefined {
  const remoteApiUrl = resolveRemoteApiUrl(env);
  if (!remoteApiUrl) return undefined;
  if (!isBackendSurfacePath(pathname)) return undefined;

  return appendPath(remoteApiUrl, pathname);
}

// `/auth/callback` is this app's own OAuth landing page — the browser has to
// reach the route, not the backend's handler.
function isBackendAuthPath(pathname: string): boolean {
  if (pathname === "/auth/callback") return false;
  if (pathname.startsWith("/auth/callback/")) return false;
  return pathname === "/auth" || pathname.startsWith("/auth/");
}

// `/ws` is appended to the api base's PATH, not to its origin, and that is
// deliberate: the base is whatever prefix the backend is mounted under, so HTTP
// (`<base>/api/**`) and realtime (`<base>/ws`) must share it or a
// prefix-mounted deployment would break in one direction while working in the
// other.
function tryDeriveWsUrl(apiUrl: string): string | undefined {
  let url: URL;
  try {
    url = new URL(apiUrl);
  } catch {
    return undefined;
  }
  if (url.protocol === "https:") url.protocol = "wss:";
  else if (url.protocol === "http:") url.protocol = "ws:";
  else return undefined;
  url.pathname = appendPath(url.pathname.replace(/\/+$/, ""), "/ws");
  url.search = "";
  url.hash = "";
  return url.toString().replace(/\/$/, "");
}
