# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/), and this project adheres to
[Semantic Versioning](https://semver.org/).

## [1.0.1]

### Added
- Configurable maintenance response body (inline `maintenanceResponseBody` or a
  mounted `maintenanceResponseFilePath`) with `maintenanceContentType`.
- `retryAfterSeconds` sets a `Retry-After` header on the maintenance response.
- `maintenanceCacheControl` (default `no-store`) sets `Cache-Control` on the
  maintenance response so a recovered service is not masked by a cached page.

## [1.0.0]

First stable release.

### Added
- Background-polled maintenance state and IP whitelist from a configurable HTTP
  API — the request path never calls the API.
- IP whitelist with CIDR ranges, `*` wildcard, and canonical IPv6 matching;
  optional `trustedProxies` gate on the `Cf-Connecting-Ip` header.
- Per-environment routing: domain suffix → endpoint/secret, each independently
  cached (longest-suffix match, `""` fallback).
- Secret-gated status requests via `secretHeader` (top-level or per-environment).
- Skip rules for hosts, path prefixes, static assets, and HTML documents.
- CORS handling for preflight and blocked responses with an optional origin
  allow-list.
- Resilient refresh: exponential backoff with jitter and last-known-good cache.
- Zero external dependencies; loads under Traefik's Yaegi runtime.
