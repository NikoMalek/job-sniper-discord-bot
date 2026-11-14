/**
 * Job Sniper outbound proxy for Cloudflare Workers (free plan).
 *
 * The bot uses this Worker when it cannot reach a portal directly, or as a
 * fallback after a portal rate-limits the Railway IP. It only forwards GET
 * requests to hosts listed in ALLOWED_HOSTS, so it cannot be abused as an
 * open proxy.
 *
 * Setup (Cloudflare dashboard -> Workers & Pages -> Create -> Worker):
 *   1. Paste this file as the Worker code.
 *   2. Settings -> Variables and Secrets -> add a Secret named PROXY_TOKEN
 *      with a long random value.
 *   3. Optional: add a variable ALLOWED_HOSTS with a comma-separated list of
 *      hosts (defaults to ChileTrabajos + LinkedIn).
 *   4. Deploy. The Worker URL is https://<name>.<subdomain>.workers.dev.
 *   5. In the bot set:
 *        JOB_PROXY_URL=https://<name>.<subdomain>.workers.dev
 *        JOB_PROXY_TOKEN=<the same random value>
 *
 * Requests must send:
 *   X-Proxy-Token: <PROXY_TOKEN>
 *   X-Target-Host: www.chiletrabajos.cl  (must be in ALLOWED_HOSTS)
 * The path and query string are forwarded unchanged.
 */

const ORIGIN_SCHEME = "https";
const DEFAULT_ALLOWED_HOSTS =
  "www.chiletrabajos.cl,www.linkedin.com,cl.linkedin.com";
const UPSTREAM_TIMEOUT_MS = 25_000;
const HOST_PATTERN = /^[a-z0-9.-]+$/;
const FORWARDED_HEADERS = ["user-agent", "accept", "accept-language"];

function text(status, message) {
  return new Response(message + "\n", {
    status,
    headers: {
      "content-type": "text/plain; charset=utf-8",
      "cache-control": "no-store",
    },
  });
}

function allowedHosts(env) {
  const raw = env.ALLOWED_HOSTS || DEFAULT_ALLOWED_HOSTS;
  return raw
    .split(",")
    .map((host) => host.trim().toLowerCase())
    .filter(Boolean);
}

export default {
  async fetch(request, env) {
    if (request.method !== "GET") {
      return text(405, "method not allowed");
    }

    const url = new URL(request.url);

    // Deployment probe: `curl https://<worker>/__health` should print "ok".
    if (url.pathname === "/__health") {
      return text(200, "ok");
    }

    const expected = (env.PROXY_TOKEN || "").trim();
    if (!expected) {
      return text(500, "PROXY_TOKEN is not configured");
    }
    if (request.headers.get("x-proxy-token") !== expected) {
      return text(403, "forbidden");
    }

    const host = (request.headers.get("x-target-host") || "").trim().toLowerCase();
    if (!HOST_PATTERN.test(host)) {
      return text(400, "invalid X-Target-Host");
    }
    if (!allowedHosts(env).includes(host)) {
      return text(403, "host not allowed: " + host);
    }

    const target = ORIGIN_SCHEME + "://" + host + url.pathname + url.search;

    const headers = new Headers();
    for (const name of FORWARDED_HEADERS) {
      const value = request.headers.get(name);
      if (value) {
        headers.set(name, value);
      }
    }

    let upstream;
    try {
      upstream = await fetch(target, {
        method: "GET",
        headers,
        redirect: "follow",
        signal: AbortSignal.timeout(UPSTREAM_TIMEOUT_MS),
      });
    } catch (error) {
      return text(502, "upstream error: " + (error && error.message));
    }

    // Cloudflare already decompressed the body, so content-encoding and
    // content-length from the origin must not be forwarded.
    const responseHeaders = new Headers();
    const contentType = upstream.headers.get("content-type");
    if (contentType) {
      responseHeaders.set("content-type", contentType);
    }
    responseHeaders.set("cache-control", "no-store");

    return new Response(upstream.body, {
      status: upstream.status,
      headers: responseHeaders,
    });
  },
};
