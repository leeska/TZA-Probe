import hashlib
import base64
import json
import os
import platform
import stat
import sys
import tempfile
import contextlib
from pathlib import Path


def read_regular(path, limit):
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, "rb") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
            raise ValueError("Bounded regular input required")
        raw = source.read(limit + 1)
        if len(raw) > limit:
            raise ValueError("Input size changed")
        return raw


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


request = json.loads(sys.stdin.buffer.read(65537))
plan = request["plan"]
options = plan["options"]
if platform.system() != "Linux" or os.geteuid() != 0:
    raise ValueError("Privileged Linux operation required")
source = read_regular(options["reference_installer"], 128 << 10)
if digest(source) != "8df4220b5a6ada13e9c4f238849a4dc691fc3e1170b2645784d5c0bfdcd96640":
    raise ValueError("Trusted reference installer changed")
if options["action"] not in ("verify", "inspect-install"):
    key_id, public = request["public_key"].split(":", 1)
    if key_id != plan["signing_key_id"] or digest(base64.b64decode(public, validate=True)) != plan["signing_key_sha256"]:
        raise ValueError("Pinned signing key changed after approval")
namespace = {"__name__": "tza_pinned_reference"}
exec(compile(source, options["reference_installer"], "exec"), namespace)
root = Path(options["install_root"])
os.umask(0o077)
@contextlib.contextmanager
def inspection_lock():
    descriptor = os.open(root / ".install.lock", os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        namespace["fcntl"].flock(descriptor, namespace["fcntl"].LOCK_EX | namespace["fcntl"].LOCK_NB)
        yield
    finally:
        os.close(descriptor)


with (contextlib.nullcontext() if options["action"] == "verify" else inspection_lock() if options["action"] == "inspect-install" else namespace["operation_lock"](root)):
    current = root / "current"
    pointer = os.readlink(current) if current.is_symlink() else ""
    if current.exists() and not current.is_symlink():
        raise ValueError("Current pointer is unmanaged")
    if pointer != plan["expected_current"]:
        raise ValueError("Selected runtime changed after approval")
    selection = root / "selection.json"
    selection_digest = digest(read_regular(selection, 16384)) if selection.exists() else ""
    if selection_digest != plan["selection_sha256"]:
        raise ValueError("Selection transaction changed after approval")
    if options["action"] == "inspect-install":
        release = root / "releases" / plan["archive_sha256"]
        if not os.path.lexists(release):
            result = {"state": "not_applied", "release": ""}
        else:
            meta = namespace["verify_installed"](release)
            receipt = namespace["decode"](read_regular(release / "installed.json", 16 << 20))
            if meta["version"] != plan["version"] or receipt["archive_sha256"] != plan["archive_sha256"]:
                raise ValueError("Installed release differs from approved installation")
            result = {"state": "installed", "selected": False, "release": str(release)}
    elif options["action"] == "verify":
        if pointer != "releases/" + plan["archive_sha256"]:
            raise ValueError("Selected release differs from approved activation")
        meta = namespace["verify_installed"](root / pointer)
        if meta["version"] != plan["version"]:
            raise ValueError("Installed version differs from approved activation")
        result = {"state": "verified", "release": plan["archive_sha256"]}
    elif options["action"] == "install":
        with tempfile.TemporaryDirectory(prefix=".tza-key-", dir=root) as temporary:
            key = Path(temporary) / "key.pub"
            key.write_text(request["public_key"] + "\n")
            key.chmod(0o600)
            result = namespace["install"](
                Path(request["stage_path"]), root, key, plan["archive_sha256"]
            )
    elif options["action"] == "select":
        config = read_regular(options["config_path"], 1 << 20)
        policy = read_regular(options["policy_path"], 16384)
        if digest(config) != plan["config_sha256"] or digest(policy) != plan["policy_sha256"]:
            raise ValueError("Local configuration or policy changed after approval")
        with tempfile.TemporaryDirectory(prefix=".tza-config-", dir=root) as temporary:
            config_path, policy_path = Path(temporary) / "config.toml", Path(temporary) / "policy.toml"
            config_path.write_bytes(config)
            policy_path.write_bytes(policy)
            config_path.chmod(0o600)
            policy_path.chmod(0o600)
            result = namespace["select"](
                root, plan["archive_sha256"], Path(options["state_dir"]),
                config_path, policy_path, options["unit"]
            )
    else:
        raise ValueError("Unsupported fixed runtime operation")
print(json.dumps(result, separators=(",", ":")))
