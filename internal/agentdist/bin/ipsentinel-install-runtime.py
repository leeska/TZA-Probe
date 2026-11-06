#!/usr/bin/env python3
"""Trusted local reference installer; NOT shipped as an executable package hook.

TZA may implement the same layout directly. The administrator provisions this
script independently and pins a signing key and exact approved archive hash.
No download, shell hook, service start, user creation or database restore occurs.
"""
from __future__ import annotations

import argparse
import base64
import contextlib
import fcntl
import hashlib
import io
import json
import os
import platform
import re
import shutil
import sqlite3
import stat
import subprocess
import tarfile
import tempfile
import tomllib
import uuid
import zipfile
from pathlib import Path, PurePosixPath

MAX_ARTIFACT = 1 << 30
MAX_EXPANDED = 2 << 30
MAX_FILES = 50000
DIGEST = re.compile(r"[a-f0-9]{64}")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def unique(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "Duplicate JSON key")
        result[key] = value
    return result


def decode(raw):
    return json.loads(raw, object_pairs_hook=unique, parse_constant=lambda value: require(False, "Non-finite JSON"))


def hash_file(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def canonical_hash(value):
    # This install subset contains only ASCII strings, bounded integers and bools.
    # Validate that restriction rather than pretending Python JSON implements JCS.
    def check(item):
        if isinstance(item, dict):
            for key, child in item.items():
                require(key.isascii(), "Non-ASCII install key")
                check(child)
        elif isinstance(item, list):
            for child in item:
                check(child)
        elif isinstance(item, str):
            require(item.isascii(), "Non-ASCII install value")
        else:
            require(item is None or type(item) is bool or type(item) is int and abs(item) <= 9007199254740991, "Unsupported canonical value")
    check(value)
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()


def read_regular(path, maximum, *, trusted=False):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as source:
        info = os.fstat(source.fileno())
        require(stat.S_ISREG(info.st_mode) and info.st_size <= maximum, "Input must be a bounded regular file")
        if trusted:
            require(info.st_uid == 0 and not info.st_mode & 0o022, "Pinned key descriptor is not root-owned or is writable by others")
        raw = source.read(maximum + 1)
        require(len(raw) <= maximum, "Input grew beyond its limit")
        return raw


def sync_dir(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def atomic_json(path, value):
    temporary = path.with_name("." + path.name + "-" + uuid.uuid4().hex)
    try:
        with temporary.open("x") as output:
            os.chmod(temporary, 0o600)
            json.dump(value, output, sort_keys=True, separators=(",", ":"))
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
        temporary.replace(path)
        sync_dir(path.parent)
    finally:
        temporary.unlink(missing_ok=True)


def trusted_directory(path):
    require(path.is_absolute(), "Install root must be absolute")
    for parent in reversed((path, *path.parents)):
        require(not parent.is_symlink(), "Install path cannot contain symlinks")
        if parent.exists():
            info = parent.stat()
            require(info.st_uid == 0 and not info.st_mode & 0o022 and stat.S_ISDIR(info.st_mode),
                    "Install path must be root-owned and not writable by other users")
        else:
            parent.mkdir(mode=0o755)
            parent.chmod(0o755)
    # The service user must be able to traverse this public, immutable runtime.
    path.chmod(0o755)


@contextlib.contextmanager
def operation_lock(root):
    trusted_directory(root)
    descriptor = os.open(root / ".install.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield
    finally:
        os.close(descriptor)


def verify_release(release, key_file, approved_digest):
    require(DIGEST.fullmatch(approved_digest), "Exact approved archive SHA-256 required")
    raw = read_regular(release / "archive.zip", 4 << 20)
    require(hashlib.sha256(raw).hexdigest() == approved_digest, "Archive differs from approved digest")
    signature = decode(read_regular(release / "signature.json", 4096))
    require(set(signature) == {"key_id", "package", "version", "sha256", "sig"}, "Invalid signature envelope")
    require(signature["package"] == "ipsentinel-runtime" and signature["sha256"] == approved_digest, "Signature identity mismatch")
    require(re.fullmatch(r"(0|[1-9][0-9]{0,4})\.(0|[1-9][0-9]{0,4})\.(0|[1-9][0-9]{0,4})", signature["version"]), "Invalid release version")
    key_id, encoded = read_regular(key_file, 1024, trusted=True).decode().strip().split(":", 1)
    require(re.fullmatch(r"[a-z0-9][a-z0-9.-]{0,31}", key_id) and key_id == signature["key_id"], "Signing key is not pinned")
    public_key = base64.b64decode(encoded, validate=True)
    signed = base64.b64decode(signature["sig"], validate=True)
    require(len(public_key) == 32 and len(signed) == 64, "Invalid Ed25519 key or signature size")
    with tempfile.TemporaryDirectory(prefix="ipsentinel-signature-") as temporary:
        temporary = Path(temporary)
        (temporary / "key.der").write_bytes(bytes.fromhex("302a300506032b6570032100") + public_key)
        (temporary / "signature").write_bytes(signed)
        (temporary / "message").write_bytes(
            f'tzapkg-sig-v1\nipsentinel-runtime\n{signature["version"]}\n{approved_digest}\n'.encode())
        result = subprocess.run(["openssl", "pkeyutl", "-verify", "-pubin", "-keyform", "DER",
                                 "-inkey", str(temporary / "key.der"), "-rawin",
                                 "-in", str(temporary / "message"), "-sigfile", str(temporary / "signature")],
                                capture_output=True, timeout=10)
        require(result.returncode == 0, "Release signature verification failed")
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        require(len(archive.infolist()) == 1 and archive.namelist() == ["executor.json"], "Declaration must contain only executor.json")
        require(archive.infolist()[0].file_size <= 1 << 20, "Declaration exceeds limit")
        manifest = decode(archive.read("executor.json"))
    require(manifest["format_version"] == 1 and manifest["id"] == "ipsentinel-runtime"
            and manifest["type"] == "executor" and manifest["version"] == signature["version"], "Invalid executor identity")
    descriptor = manifest["descriptor"]
    require(descriptor["executor_id"] == "ipsentinel" and descriptor["runtime_version"] == manifest["version"]
            and descriptor["contract_version"] == "1.0.0" and descriptor["protocols"] == ["ipsentinel.executor/1"]
            and descriptor["platforms"] == ["linux-amd64"], "Unsupported descriptor")
    require(manifest["compatibility"]["cc_format"] == 1, "Unsupported CC format")
    require(len(manifest["dependencies"]) == len(descriptor["runtime_artifacts"]) == 1, "Single platform distribution required")
    dependencies = manifest["dependencies"][0]
    runtime = descriptor["runtime_artifacts"][0]
    require(dependencies["platform"] == runtime["platform"] == "linux-amd64"
            and runtime["entrypoint"] == "ipsentinel-control"
            and canonical_hash(dependencies) == runtime["dependencies_sha256"], "Dependency set or entrypoint mismatch")
    require(canonical_hash(descriptor["profiles"]) == descriptor["catalog_sha256"], "Catalogue hash mismatch")
    kinds = [item["kind"] for item in dependencies["artifacts"]]
    require(sorted(kinds) == ["chromium", "libraries", "python", "wheels"], "Exactly four dependency kinds required")
    require(len({item["id"] for item in dependencies["artifacts"]}) == 4, "Duplicate dependency identity")
    for item in dependencies["artifacts"]:
        require(re.fullmatch(r"[a-z0-9-]{1,64}", item["id"]), "Invalid dependency identity")
        if item["kind"] == "python":
            require(item["version"] == manifest["compatibility"]["python"]
                    and re.fullmatch(r"3\.1[2-9]", item["version"]), "Unsupported Python ABI")
        if item["kind"] == "chromium":
            require(item["version"] == runtime["browser_revision"], "Browser revision mismatch")
    for item in [runtime, *dependencies["artifacts"]]:
        require(DIGEST.fullmatch(item["sha256"]) and type(item["size_bytes"]) is int
                and 0 < item["size_bytes"] <= MAX_ARTIFACT, "Invalid artifact reference")
    return manifest


def inspect_tar(path, kind, minor):
    result, size, names = [], 0, set()
    site = f"python/lib/python{minor}/site-packages/"
    with tarfile.open(path, "r:gz") as archive:
        for member in archive:
            parts = PurePosixPath(member.name).parts
            require(member.name and not member.name.startswith("/") and ".." not in parts
                    and PurePosixPath(member.name).as_posix() == member.name and "\\" not in member.name,
                    "Unsafe archive path")
            require(member.isfile() and member.name not in names, "Only unique regular archive members are accepted")
            require(not member.mode & 0o7000 and member.size >= 0, "Unsafe archive permissions")
            name = member.name
            if kind == "runtime":
                relative = name.removeprefix(site)
                allowed = name in {"bin/ipsentinel-control", "runtime.json", "catalog.json", "build-inputs.json"} or (
                    name.startswith(site) and (relative.startswith("ipsentinel/") or re.match(r"ipsentinel-[^/]+\.dist-info/", relative)))
            elif kind == "wheels":
                relative = name.removeprefix(site)
                allowed = name.startswith(site) and not relative.startswith(("ipsentinel/", "ipsentinel-"))
            elif kind == "python":
                allowed = (name == f"python/bin/python{minor}" or name.startswith(f"python/lib/python{minor}/")) and not name.startswith(site)
            else:
                allowed = name.startswith("browsers/chromium-")
            require(allowed, "Artifact contains a member outside its fixed layout")
            names.add(name)
            size += member.size
            require(size <= MAX_EXPANDED and len(names) <= MAX_FILES, "Expanded archive exceeds limit")
            result.append((member.name, member.size, bool(member.mode & 0o111)))
    return result


def check_host(lock):
    require(lock["format_version"] == 1 and lock["architecture"] == platform.machine(), "Host architecture mismatch")
    release = platform.freedesktop_os_release()
    require(release["ID"] == lock["distribution"] == "debian" and release["VERSION_ID"] == lock["distribution_version"] == "13",
            "Candidate only supports Debian 13")
    require(Path("/sys/fs/cgroup/cgroup.controllers").is_file(), "cgroup v2 required")
    version = subprocess.check_output(["systemctl", "--version"], text=True).split()[1]
    require(int(version) >= lock["minimum_systemd"], "systemd lacks delegated subgroup support")
    for item in lock["files"]:
        path = Path(item["path"])
        require(path.is_absolute() and path.is_file() and hash_file(path) == item["sha256"], "Host library digest mismatch: " + str(path))
    for item in lock["packages"]:
        version = subprocess.check_output(["dpkg-query", "-W", "-f=${Version}", item["name"]], text=True).strip()
        require(version == item["version"], "Host package version mismatch: " + item["name"])


def install(release, root, key_file, approved_digest):
    manifest = verify_release(release, key_file, approved_digest)
    references = [{"kind": "runtime", **manifest["descriptor"]["runtime_artifacts"][0]},
                  *manifest["dependencies"][0]["artifacts"]]
    minor = manifest["compatibility"]["python"]
    releases = root / "releases"
    trusted_directory(releases)
    destination = releases / approved_digest
    if destination.exists():
        verify_installed(destination)
        require(decode((destination / "installed.json").read_bytes())["archive_sha256"] == approved_digest, "Installed release identity mismatch")
        return {"state": "installed", "selected": False, "release": str(destination)}
    with tempfile.TemporaryDirectory(prefix=".pending-", dir=releases) as temporary:
        temporary = Path(temporary)
        blobs = temporary / ".blobs"
        blobs.mkdir()
        require(shutil.disk_usage(root).free >= sum(item["size_bytes"] for item in references) + (64 << 20),
                "Insufficient space to copy verified artifacts")
        members, seen, total, compressed = {}, set(), 0, 0
        # Private copies close verification/use races with the staging owner.
        for item in references:
            source_path = release / "artifacts" / (item["sha256"] + ".bin")
            path = blobs / (item["kind"] + ".bin")
            fd = os.open(source_path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            with os.fdopen(fd, "rb") as source, path.open("xb") as output:
                info = os.fstat(source.fileno())
                require(stat.S_ISREG(info.st_mode) and info.st_size == item["size_bytes"], "Artifact size mismatch")
                hasher, count = hashlib.sha256(), 0
                while data := source.read(1 << 20):
                    count += len(data)
                    require(count <= item["size_bytes"], "Artifact grew beyond declared size")
                    output.write(data)
                    hasher.update(data)
                require(count == item["size_bytes"] and hasher.hexdigest() == item["sha256"], "Artifact digest mismatch")
                output.flush()
                os.fsync(output.fileno())
            compressed += count
            if item["kind"] == "libraries":
                lock = decode(read_regular(path, 1 << 20))
                check_host(lock)
            else:
                members[item["kind"]] = inspect_tar(path, item["kind"], minor)
                for name, size, executable in members[item["kind"]]:
                    require(name not in seen, "Artifacts overlap")
                    seen.add(name)
                    total += size
                require(total <= MAX_EXPANDED and len(seen) <= MAX_FILES, "Combined release exceeds expansion budget")
        require(shutil.disk_usage(root).free >= total + (64 << 20), "Insufficient space to install safely")
        inventory = []
        for kind, entries in members.items():
            with tarfile.open(blobs / (kind + ".bin"), "r:gz") as archive:
                for member, (name, _, executable) in zip(archive, entries, strict=True):
                    target = temporary / name
                    target.parent.mkdir(parents=True, exist_ok=True)
                    with archive.extractfile(member) as source, target.open("xb") as output:
                        shutil.copyfileobj(source, output, 1 << 20)
                        output.flush()
                        os.fsync(output.fileno())
                    target.chmod(0o555 if executable else 0o444)
                    inventory.append({"path": name, "sha256": hash_file(target), "mode": target.stat().st_mode & 0o777})
        meta = decode((temporary / "runtime.json").read_bytes())
        require(meta["layout"] == "ipsentinel-runtime-layout/1" and meta["version"] == manifest["version"]
                and meta["python"] == minor and meta["platform"] == "linux-amd64"
                and meta["dependencies_sha256"] == references[0]["dependencies_sha256"]
                and meta["browser_revision"] == references[0]["browser_revision"]
                and meta["catalog_sha256"] == manifest["descriptor"]["catalog_sha256"]
                and canonical_hash(decode((temporary / "catalog.json").read_bytes())) == meta["catalog_sha256"],
                "Runtime metadata differs from signed declaration")
        require((temporary / "bin/ipsentinel-control").is_file()
                and (temporary / f"python/bin/python{minor}").is_file()
                and (temporary / f"browsers/chromium-{meta['browser_revision']}").is_dir(), "Required entry or runtime missing")
        shutil.rmtree(blobs)
        atomic_json(temporary / "host-lock.json", lock)
        inventory.append({"path": "host-lock.json", "sha256": hash_file(temporary / "host-lock.json"), "mode": 0o600})
        atomic_json(temporary / "installed.json", {"format_version": 1, "archive_sha256": approved_digest,
                    "manifest": manifest, "inventory": inventory})
        for directory in sorted((p for p in temporary.rglob("*") if p.is_dir()), reverse=True):
            directory.chmod(0o755)
            sync_dir(directory)
        temporary.chmod(0o755)
        sync_dir(temporary)
        temporary.rename(destination)
        sync_dir(releases)
    return {"state": "installed", "selected": False, "release": str(destination)}


def verify_installed(release):
    require(not release.is_symlink() and release.is_dir(), "Release must be a directory")
    receipt = decode(read_regular(release / "installed.json", 16 << 20))
    expected = {"installed.json"}
    for item in receipt["inventory"]:
        name = item["path"]
        require(not Path(name).is_absolute() and ".." not in Path(name).parts, "Invalid inventory path")
        path = release / name
        require(not path.is_symlink() and path.is_file() and hash_file(path) == item["sha256"]
                and path.stat().st_mode & 0o7777 == item["mode"], "Installed runtime changed: " + name)
        expected.add(name)
    for path in release.rglob("*"):
        require(not path.is_symlink(), "Installed symlink rejected")
        if path.is_file():
            require(path.relative_to(release).as_posix() in expected, "Unexpected installed file")
    check_host(decode((release / "host-lock.json").read_bytes()))
    return decode((release / "runtime.json").read_bytes())


@contextlib.contextmanager
def drained(state_dir):
    require(state_dir.is_absolute() and state_dir.is_dir() and not state_dir.is_symlink(), "Existing state directory required")
    descriptors = []
    try:
        for name in ("control-server.lock", "node.lock"):
            fd = os.open(state_dir / name, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
            descriptors.append(fd)
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            owner = state_dir.stat()
            os.fchown(fd, owner.st_uid, owner.st_gid)
        yield
    finally:
        for descriptor in descriptors:
            os.close(descriptor)


def select(root, approved_digest, state_dir, config, policy, unit):
    require(DIGEST.fullmatch(approved_digest), "Invalid release digest")
    require(re.fullmatch(r"ipsentinel-[A-Za-z0-9@_.-]+\.service", unit), "Only an IPSentinel service may be selected")
    active = subprocess.check_output(["systemctl", "show", "-p", "ActiveState", "--value", unit], text=True).strip()
    require(active in {"inactive", "failed"}, "Stop the executor service before selecting a runtime")
    release = root / "releases" / approved_digest
    meta = verify_installed(release)
    transaction_file = root / "selection.json"
    if transaction_file.exists():
        require(decode(read_regular(transaction_file, 16384))["state"] == "committed", "Unfinished selection requires explicit recover")
    with drained(state_dir):
        require(Path(tomllib.loads(read_regular(config, 1 << 20).decode())["state_dir"]).resolve() == state_dir.resolve(),
                "Config state directory differs from selected installation")
        approved_policy = tomllib.loads(read_regular(policy, 16384).decode())
        require(Path(approved_policy["state_dir"]).resolve() == state_dir.resolve()
                and Path(approved_policy["browser_path"]) == root / "current/browsers",
                "Policy must use this installation and its selected browser directory")
        database = state_dir / "control.sqlite3"
        schema = None
        if database.exists():
            require(not database.is_symlink(), "Database cannot be a symlink")
            with sqlite3.connect(database.as_uri() + "?mode=ro", uri=True) as db:
                schema = db.execute("PRAGMA user_version").fetchone()[0]
                require(meta["control_schema_min"] <= schema <= meta["control_schema_max"], "Database schema cannot be used by selected runtime; no automatic database rollback")
                require(db.execute("SELECT count(*) FROM native_runs WHERE state IN ('accepted','running','cancelling')").fetchone()[0] == 0, "Tasks have not drained")
        backup = root / "backups" / uuid.uuid4().hex
        backup.mkdir(parents=True, mode=0o700)
        for name in ("control.sqlite3", "history.sqlite3"):
            source = state_dir / name
            if source.exists():
                require(not source.is_symlink(), "Database cannot be a symlink")
                with sqlite3.connect(source.as_uri() + "?mode=ro", uri=True) as db, sqlite3.connect(backup / name) as target:
                    db.backup(target)
                    require(target.execute("PRAGMA quick_check").fetchone()[0] == "ok", "Database snapshot failed")
                os.chmod(backup / name, 0o600)
                with (backup / name).open("rb") as handle:
                    os.fsync(handle.fileno())
        for path, name in ((config, "config.toml"), (policy, "policy.toml")):
            content = read_regular(path, 1 << 20)
            with (backup / name).open("xb") as output:
                os.chmod(backup / name, 0o600)
                output.write(content)
                output.flush()
                os.fsync(output.fileno())
        sync_dir(backup)
        sync_dir(backup.parent)
        current = root / "current"
        require(not current.exists() or current.is_symlink(), "Current runtime must be a managed symlink")
        previous = os.readlink(current) if current.is_symlink() else None
        require(previous is None or re.fullmatch(r"releases/[a-f0-9]{64}", previous), "Current link is outside releases")
        transaction = {"state": "prepared", "previous": previous, "selected": "releases/" + approved_digest,
                       "backup": str(backup), "schema": schema}
        atomic_json(transaction_file, transaction)
        temporary = root / (".current-" + uuid.uuid4().hex)
        temporary.symlink_to(transaction["selected"])
        temporary.replace(current)
        sync_dir(root)
        transaction["state"] = "committed"
        atomic_json(transaction_file, transaction)
    return {"state": "selected", "activated": False, "release": approved_digest, "backup": str(backup)}


def recover(root):
    transaction = decode(read_regular(root / "selection.json", 16384))
    require(transaction["state"] == "prepared", "No interrupted selection")
    current = root / "current"
    selected = os.readlink(current) if current.is_symlink() else None
    require(selected in {transaction["previous"], transaction["selected"]}, "Selection link has changed externally")
    # Complete only the durable pointer receipt. Never launch or rewrite state.
    if selected is not None:
        require(re.fullmatch(r"releases/[a-f0-9]{64}", selected), "Invalid selected path")
        verify_installed(root / selected)
    transaction["state"] = "committed"
    transaction["selected"] = selected
    atomic_json(root / "selection.json", transaction)
    return {"state": "recovered", "activated": False, "selected": selected}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path("/opt/ipsentinel-runtime"))
    commands = parser.add_subparsers(dest="command", required=True)
    installer = commands.add_parser("install")
    installer.add_argument("--release", type=Path, required=True)
    installer.add_argument("--key-file", type=Path, required=True)
    installer.add_argument("--archive-sha256", required=True)
    selector = commands.add_parser("select")
    selector.add_argument("--archive-sha256", required=True)
    selector.add_argument("--state-dir", type=Path, required=True)
    selector.add_argument("--config", type=Path, required=True)
    selector.add_argument("--policy", type=Path, required=True)
    selector.add_argument("--unit", default="ipsentinel-runtime.service")
    commands.add_parser("recover")
    args = parser.parse_args()
    require(platform.system() == "Linux" and os.geteuid() == 0, "Trusted installer requires root on Linux")
    os.umask(0o077)
    with operation_lock(args.root):
        if args.command == "install":
            key = args.key_file.lstat()
            require(key.st_uid == 0 and not key.st_mode & 0o022 and stat.S_ISREG(key.st_mode), "Pinned key must be a root-owned regular file")
            result = install(args.release, args.root, args.key_file, args.archive_sha256)
        elif args.command == "select":
            result = select(args.root, args.archive_sha256, args.state_dir, args.config, args.policy, args.unit)
        else:
            result = recover(args.root)
    print(json.dumps(result))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError, TypeError, sqlite3.Error, subprocess.SubprocessError, tarfile.TarError, zipfile.BadZipFile) as error:
        print(json.dumps({"state": "rejected", "error": str(error)}))
        raise SystemExit(1)
