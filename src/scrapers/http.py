"""Shared HTTP behaviour for portal scrapers."""

from __future__ import annotations

import asyncio
import logging
import os
import random
import time
from datetime import datetime, timezone
from urllib.parse import urlsplit

import httpx

from .base import BaseScraper

LOGGER = logging.getLogger(__name__)

# Cloudflare Workers' free plan allows 100,000 requests per UTC day. Past that
# the Worker answers with errors, so the bot stops using it a bit earlier and
# falls back to direct requests until the counter resets at 00:00 UTC.
PROXY_DAILY_LIMIT_ENV = "JOB_PROXY_DAILY_LIMIT"
DEFAULT_PROXY_DAILY_LIMIT = 90_000


class _ProxyBudget:
    """Counts Worker requests per UTC day, shared by every scraper."""

    def __init__(self) -> None:
        self._day = ""
        self._count = 0
        self._warned = False

    def _roll(self) -> None:
        today = datetime.now(timezone.utc).strftime("%Y-%m-%d")
        if today != self._day:
            self._day = today
            self._count = 0
            self._warned = False

    @staticmethod
    def limit() -> int:
        try:
            return int(os.getenv(PROXY_DAILY_LIMIT_ENV, "").strip())
        except ValueError:
            return DEFAULT_PROXY_DAILY_LIMIT

    def available(self) -> bool:
        self._roll()
        limit = self.limit()
        if limit <= 0 or self._count < limit:
            return True
        if not self._warned:
            self._warned = True
            LOGGER.warning(
                "Proxy daily budget reached (%d requests); using direct "
                "requests until 00:00 UTC",
                self._count,
            )
        return False

    def spend(self) -> None:
        self._roll()
        self._count += 1

    @property
    def used(self) -> int:
        self._roll()
        return self._count


PROXY_BUDGET = _ProxyBudget()


class HttpScraper(BaseScraper):
    """Base class with retry/backoff helpers shared by every scraper.

    Subclasses only need to implement :meth:`fetch_jobs`; they may override the
    retry knobs below to match the portal's rate limits.
    """

    SEARCH_RETRIES = 2
    DETAIL_RETRIES = 2
    RETRY_BASE_SECONDS = 3.0
    RETRY_AFTER_CAP = 15.0
    REQUEST_TIMEOUT_SECONDS = 20.0
    CONNECT_TIMEOUT_SECONDS = 5.0
    TRANSPORT_FAILURE_LIMIT = 3
    # After the direct path fails in "fallback" mode, go straight to the proxy
    # for this long, then probe the direct path again.
    DIRECT_COOLDOWN_SECONDS = 15 * 60

    # Outbound proxy (see worker/job-sniper-proxy.js).
    #   "off"      -> never use the proxy
    #   "fallback" -> try direct first, use the proxy when the direct path fails
    #   "always"   -> send every request through the proxy
    PROXY_MODE = "off"
    PROXY_HOSTS: tuple[str, ...] = ()
    PROXY_URL_ENV = "JOB_PROXY_URL"
    PROXY_TOKEN_ENV = "JOB_PROXY_TOKEN"
    PROXY_MODE_ENV = "JOB_PROXY_MODE"
    PROXY_LEGACY_URL_ENV = "CHILETRABAJOS_PROXY_URL"
    PROXY_LEGACY_TOKEN_ENV = "CHILETRABAJOS_PROXY_TOKEN"

    def _request_timeout(self) -> httpx.Timeout:
        return httpx.Timeout(
            self.REQUEST_TIMEOUT_SECONDS,
            connect=self.CONNECT_TIMEOUT_SECONDS,
        )

    def _reset_transport_health(self) -> None:
        """Start a scrape run with a closed circuit breaker."""
        self._transport_failures = 0
        self._circuit_open = False

    def _circuit_is_open(self) -> bool:
        return bool(getattr(self, "_circuit_open", False))

    @classmethod
    def _env(cls, name: str) -> str:
        return os.getenv(name, "").strip()

    def _proxy_base(self) -> str:
        base = self._env(self.PROXY_URL_ENV) or self._env(
            self.PROXY_LEGACY_URL_ENV
        )
        return base.rstrip("/")

    def _proxy_token(self) -> str:
        return self._env(self.PROXY_TOKEN_ENV) or self._env(
            self.PROXY_LEGACY_TOKEN_ENV
        )

    def _proxy_mode(self) -> str:
        """Resolve the proxy mode for this scraper."""
        if not self._proxy_base() or not self._proxy_token() or not self.PROXY_HOSTS:
            return "off"
        override = self._env(self.PROXY_MODE_ENV).lower()
        if override in {"off", "fallback", "always"}:
            return override
        return self.PROXY_MODE

    def _proxy_request(self, url: str) -> tuple[str, dict[str, str]]:
        """Return the proxy URL and headers for ``url``, or ``("", {})``.

        Only hosts listed in :attr:`PROXY_HOSTS` are routed through the Worker.
        """
        parts = urlsplit(url)
        if not parts.hostname or parts.hostname not in self.PROXY_HOSTS:
            return "", {}
        target = f"{self._proxy_base()}{parts.path or '/'}"
        if parts.query:
            target = f"{target}?{parts.query}"
        return target, {
            "X-Proxy-Token": self._proxy_token(),
            "X-Target-Host": parts.hostname,
        }

    def _request_targets(
        self, url: str
    ) -> list[tuple[str, dict[str, str]]]:
        """Return the ordered transports to try: direct and/or proxy."""
        mode = self._proxy_mode()
        if mode == "off":
            return [(url, {})]
        proxy_url, proxy_headers = self._proxy_request(url)
        if not proxy_url or not PROXY_BUDGET.available():
            return [(url, {})]
        if mode == "always":
            return [(proxy_url, proxy_headers)]
        if self._direct_cooling_down():
            return [(proxy_url, proxy_headers)]
        return [(url, {}), (proxy_url, proxy_headers)]

    def _direct_cooling_down(self) -> bool:
        return time.monotonic() < getattr(self, "_direct_down_until", 0.0)

    def _note_direct_result(
        self, response: httpx.Response | None, url: str
    ) -> None:
        """Track whether the direct path works so it can recover on its own."""
        if not self._proxy_worthy(response):
            if getattr(self, "_direct_down_until", 0.0):
                LOGGER.info("%s direct transport recovered", self.source)
            self._direct_down_until = 0.0
            return
        if not self._direct_cooling_down():
            LOGGER.warning(
                "%s direct transport failed (%s) for %s; using the proxy for "
                "%d min before probing direct again",
                self.source,
                "no response" if response is None else response.status_code,
                urlsplit(url).path,
                self.DIRECT_COOLDOWN_SECONDS // 60,
            )
        self._direct_down_until = time.monotonic() + self.DIRECT_COOLDOWN_SECONDS

    @staticmethod
    def _proxy_worthy(response: httpx.Response | None) -> bool:
        """Whether a failed direct response should be retried via the proxy."""
        if response is None:
            return True
        return response.status_code in (403, 429, 999) or response.status_code >= 500

    def _retry_delay(self, response: httpx.Response | None, attempt: int) -> float:
        if response is not None:
            retry_after = response.headers.get("Retry-After", "")
            try:
                return min(max(float(retry_after), 1.0), self.RETRY_AFTER_CAP)
            except (TypeError, ValueError):
                pass
        delay = self.RETRY_BASE_SECONDS * (2**attempt)
        return delay + random.uniform(0.0, 1.5)

    @staticmethod
    def _is_authwall(response: httpx.Response) -> bool:
        final_url = str(response.url).lower()
        return "authwall" in final_url or "/login" in final_url

    async def _request_with_retry(
        self,
        client: httpx.AsyncClient,
        url: str,
        *,
        params: dict[str, str | int] | None = None,
        headers: dict[str, str] | None = None,
        retries: int | None = None,
        label: str = "request",
    ) -> httpx.Response | None:
        """Fetch ``url`` retrying rate limits, server errors and network errors.

        With a proxy configured, ``fallback`` retries the request through the
        Worker after the direct attempt fails and ``always`` uses it directly.
        """
        attempts = self.SEARCH_RETRIES if retries is None else retries
        targets = self._request_targets(url)
        last: httpx.Response | None = None

        for index, (target_url, extra_headers) in enumerate(targets):
            is_proxy = bool(extra_headers)
            if index > 0:
                # The proxy is a different network path, so a circuit opened by
                # the direct attempt must not block it.
                self._reset_transport_health()
                LOGGER.warning(
                    "%s %s failed on the direct transport; retrying via proxy",
                    self.source,
                    label,
                )
            request_headers = (
                {**(headers or {}), **extra_headers} if extra_headers else headers
            )
            response = await self._request_attempt(
                client,
                target_url,
                params=params,
                headers=request_headers,
                # A 429 through the same proxy IP will not improve by retrying
                # it, so the proxy gets a single attempt.
                attempts=0 if is_proxy else attempts,
                label=label,
                # With a proxy to fall back on, a connection that cannot even
                # be opened (blocked IP) is not worth retrying.
                fail_fast_connect=not is_proxy and len(targets) > 1,
            )
            if not is_proxy and self._proxy_mode() == "fallback":
                self._note_direct_result(response, target_url)
            if response is not None:
                last = response
                if not self._proxy_worthy(response):
                    return response
            if index + 1 >= len(targets):
                break

        return last

    async def _request_attempt(
        self,
        client: httpx.AsyncClient,
        url: str,
        *,
        params: dict[str, str | int] | None,
        headers: dict[str, str] | None,
        attempts: int,
        label: str,
        fail_fast_connect: bool = False,
    ) -> httpx.Response | None:
        """Run one transport's retry loop."""
        if self._circuit_is_open():
            return None

        for attempt in range(attempts + 1):
            try:
                if headers and "X-Proxy-Token" in headers:
                    PROXY_BUDGET.spend()
                response = await client.get(url, params=params, headers=headers)
            except (httpx.ConnectTimeout, httpx.ConnectError) as exc:
                if fail_fast_connect:
                    LOGGER.warning(
                        "%s %s could not connect directly: %r",
                        self.source,
                        label,
                        exc,
                    )
                    return None
                failures = getattr(self, "_transport_failures", 0) + 1
                self._transport_failures = failures
                if failures >= self.TRANSPORT_FAILURE_LIMIT:
                    self._circuit_open = True
                    LOGGER.warning(
                        "%s circuit open after %d consecutive transport "
                        "failures (last: %r)",
                        self.source,
                        failures,
                        exc,
                    )
                    return None
                if attempt >= attempts:
                    LOGGER.warning(
                        "%s %s failed after %d attempts: %r",
                        self.source,
                        label,
                        attempt + 1,
                        exc,
                    )
                    return None
                await asyncio.sleep(self._retry_delay(None, attempt))
                continue
            except httpx.TransportError as exc:
                failures = getattr(self, "_transport_failures", 0) + 1
                self._transport_failures = failures
                if failures >= self.TRANSPORT_FAILURE_LIMIT:
                    if not self._circuit_is_open():
                        LOGGER.warning(
                            "%s circuit open after %d consecutive transport "
                            "failures (last: %r); skipping remaining requests "
                            "for this run",
                            self.source,
                            failures,
                            exc,
                        )
                    self._circuit_open = True
                    return None
                if attempt >= attempts:
                    LOGGER.warning(
                        "%s %s failed after %d attempts: %r",
                        self.source,
                        label,
                        attempt + 1,
                        exc,
                    )
                    return None
                delay = self._retry_delay(None, attempt)
                LOGGER.warning(
                    "%s %s error: %r; retrying in %.1fs (%d/%d)",
                    self.source,
                    label,
                    exc,
                    delay,
                    attempt + 1,
                    attempts,
                )
                await asyncio.sleep(delay)
                continue
            except httpx.HTTPError as exc:
                if attempt >= attempts:
                    LOGGER.warning(
                        "%s %s failed after %d attempts: %r",
                        self.source,
                        label,
                        attempt + 1,
                        exc,
                    )
                    return None
                delay = self._retry_delay(None, attempt)
                LOGGER.warning(
                    "%s %s error: %r; retrying in %.1fs (%d/%d)",
                    self.source,
                    label,
                    exc,
                    delay,
                    attempt + 1,
                    attempts,
                )
                await asyncio.sleep(delay)
                continue

            self._transport_failures = 0

            if response.status_code == 429:
                if attempt >= attempts:
                    LOGGER.warning(
                        "%s %s hit 429 after %d attempts",
                        self.source,
                        label,
                        attempt + 1,
                    )
                    return None
                delay = self._retry_delay(response, attempt)
                LOGGER.warning(
                    "%s %s hit 429; retrying in %.1fs (%d/%d)",
                    self.source,
                    label,
                    delay,
                    attempt + 1,
                    attempts,
                )
                await asyncio.sleep(delay)
                continue

            if response.status_code >= 500:
                if attempt >= attempts:
                    LOGGER.warning(
                        "%s %s returned %s after %d attempts",
                        self.source,
                        label,
                        response.status_code,
                        attempt + 1,
                    )
                    return None
                delay = self._retry_delay(response, attempt)
                LOGGER.warning(
                    "%s %s returned %s; retrying in %.1fs (%d/%d)",
                    self.source,
                    label,
                    response.status_code,
                    delay,
                    attempt + 1,
                    attempts,
                )
                await asyncio.sleep(delay)
                continue

            return response

        return None
