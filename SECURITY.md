# Security Policy

## Reporting a Vulnerability

Please do not report security vulnerabilities through public GitHub issues.

Instead, open a [GitHub private security advisory](../../security/advisories/new) or email the maintainer directly. Include as much detail as possible: steps to reproduce, potential impact, and any suggested fixes.

You can expect an acknowledgement within 48 hours and a fix or mitigation plan within 14 days for confirmed issues.

## Supported Versions

Only the latest release receives security fixes.

## Security Considerations

### Credentials

- Provide credentials via environment variables (`HDX_TOKEN`, `HDX_USERNAME`, `HDX_PASSWORD`) — never hardcode them in config files or commit them to source control.
- Prefer `HDX_TOKEN` over username/password; rotate tokens regularly.
- Ensure your process environment is not visible to other users on the host (e.g. avoid `ps aux` leaking env vars in shared environments).

### Prometheus Endpoint

The Prometheus metrics endpoint (default `:2112/metrics`) is unauthenticated. Restrict access to it:
- Bind to `localhost` or an internal interface only, or
- Place it behind a reverse proxy with authentication, or
- Use firewall/network policy to limit which hosts can reach the port.

### Container Hardening

When running via Docker:
- The container runs as a non-root user by default — do not override this with `--user root`.
- Pass credentials as environment variables via `--env-file` rather than baking them into the image.
- Avoid logging the full environment; `HDX_TOKEN` and `HDX_PASSWORD` must not appear in log output.
