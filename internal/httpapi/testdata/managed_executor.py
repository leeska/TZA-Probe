import json
import os
import sys
from pathlib import Path

from ipsentinel.config import Config
from ipsentinel.control_modes import adopt
from ipsentinel.control_protocol import ControlProtocol
from ipsentinel.control_server import UnixServer
from ipsentinel.control_state import ControlState
from ipsentinel.execution.manager import ExecutionManager
from ipsentinel.executor_policy import LocalPolicy
from ipsentinel.executor_store import ExecutorStore


class AdmissionOnlyRuntime:
    fingerprint = "c" * 64
    browser_version = "admission-fixture"

    def ready(self, policy):
        return True

    def start(self, *args):
        raise AssertionError("This fixture must never start a browser")


class AdmissionProtocol(ControlProtocol):
    def status(self):
        value = super().status()
        value["health"] = "degraded" if value["control"]["paused"] else "ready"
        return value


directory = Path(sys.argv[1])
target = {"controller_id": sys.argv[2], "node_id": sys.argv[3]}
native = json.loads(Path(sys.argv[4]).read_text())
config = Config(
    node_id="tza-managed-fixture",
    expected_ip="8.8.8.8",
    egress_url="https://api64.ipify.org",
    urls=("https://www.google.com",),
    allowed_origins=("https://api64.ipify.org", "https://www.google.com"),
    state_dir=str(directory / "state"),
    page_adapters=("google_home",),
)
browser = directory / "browsers"
browser.mkdir()
policy = LocalPolicy(
    Path(config.state_dir).resolve(),
    browser.resolve(),
    ("network.public_https", "browser.execute", "profile.persist", "geolocation.override"),
    ("ipify-public-v1", "google-home-neutral-v1", "google-home-ncr-v1"),
    ("8.8.8.8",),
    native["limits"],
    native["daily_budget"],
    (),
    True,
)
adopt(config, policy, **target)
runtime = AdmissionOnlyRuntime()
server = UnixServer(directory / "control.sock", config, {os.geteuid()})
server.policy, server.runtime = policy, runtime
manager = ExecutionManager(config, policy, runtime)

import ipsentinel.control_server as control_server

control_server.ControlProtocol = AdmissionProtocol


def finish_cancellation():
    with ControlState(Path(config.state_dir)) as state:
        active = ExecutorStore(state).active()
        if active is not None and active["state"] == "cancelling":
            manager.step(state, ExecutorStore(state))


server.service_actions = finish_cancellation
protocol = AdmissionProtocol(config, policy, runtime)
try:
    hello = protocol.hello()
    print(json.dumps({
        "target": target,
        "executor_instance_id": hello["status"]["executor_instance_id"],
        "descriptor_sha256": hello["descriptor_sha256"],
        "epoch": hello["status"]["control"]["epoch"],
        "mode": "managed",
    }), flush=True)
finally:
    protocol.close()
server.serve_forever(poll_interval=0.02)
