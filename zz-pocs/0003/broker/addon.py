"""mitmproxy addon for zz-pocs/0003.

Loads a policy.json at startup and enforces a simple host allowlist at
flow-request time. Any denied request is short-circuited with a canned
403 response (configurable via the policy file).

Invoked by run.ts via:
    mitmdump -s broker/addon.py --set policy_file=broker/policy.json ...

For v1, the real broker plugin replaces all of this — this is the minimum
to validate the composition.
"""

import json
from pathlib import Path

from mitmproxy import ctx, http


class ClownBrokerPolicy:
    def __init__(self) -> None:
        self.allow_hosts: set[str] = set()
        self.deny_all_others: bool = True
        self.deny_status: int = 403
        self.deny_body: bytes = b"denied by clown-broker"
        self.total_requests: int = 0
        self.denied_requests: int = 0

    def load(self, loader) -> None:
        loader.add_option(
            name="policy_file",
            typespec=str,
            default="",
            help="Path to clown broker policy JSON file.",
        )

    def configure(self, updates) -> None:
        if "policy_file" in updates and ctx.options.policy_file:
            path = Path(ctx.options.policy_file)
            ctx.log.info(f"clown-broker: loading policy from {path}")
            data = json.loads(path.read_text())
            self.allow_hosts = set(data.get("allow_hosts", []))
            self.deny_all_others = bool(data.get("deny_all_others", True))
            deny = data.get("deny_response", {}) or {}
            self.deny_status = int(deny.get("status", 403))
            self.deny_body = str(deny.get("body", "denied by clown-broker")).encode(
                "utf-8"
            )
            ctx.log.info(
                f"clown-broker: allow_hosts={sorted(self.allow_hosts)} "
                f"deny_all_others={self.deny_all_others}"
            )

    def request(self, flow: http.HTTPFlow) -> None:
        self.total_requests += 1
        host = flow.request.pretty_host
        if host in self.allow_hosts:
            ctx.log.info(f"clown-broker: ALLOW {flow.request.method} {host}{flow.request.path}")
            return
        if self.deny_all_others:
            self.denied_requests += 1
            ctx.log.warn(
                f"clown-broker: DENY {flow.request.method} {host}{flow.request.path}"
            )
            flow.response = http.Response.make(
                self.deny_status,
                self.deny_body,
                {"Content-Type": "text/plain", "X-Clown-Broker": "denied"},
            )

    def done(self) -> None:
        ctx.log.info(
            f"clown-broker: totals: requests={self.total_requests} "
            f"denied={self.denied_requests}"
        )


addons = [ClownBrokerPolicy()]
