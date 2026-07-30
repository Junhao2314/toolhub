"""Guarded ToolHub runtime operations for Salt 3008.x minions.

Only root-owned bundles under the fixed staging directory are accepted. No
caller-provided filesystem root, executable, or Salt function is evaluated.
"""

import base64
import hashlib
import io
import json
import os
import pwd
import shutil
import stat
import tempfile
import time
import uuid
import zipfile

try:
    import tomllib
except ImportError:  # pragma: no cover - Salt 3008.x normally runs Python 3.11+
    tomllib = None

__virtualname__ = "toolhub"

STAGING_ROOT = "/var/cache/salt/minion/toolhub-staging"
BACKUP_ROOT = "/var/lib/toolhub/backups"
MAX_BUNDLE = 64 * 1024 * 1024
MAX_ARCHIVE = 20 * 1024 * 1024
MAX_FILE = 10 * 1024 * 1024
MAX_TOTAL = 256 * 1024 * 1024
MAX_FILES = 10000
RUNTIMES = {"claude", "codex", "hermes"}


def __virtual__():
    return __virtualname__


def _failure(code, message, retryable=False):
    return {"ok": False, "error": {"code": code, "message": message, "retryable": retryable}}


def _safe_bundle(bundle):
    if not isinstance(bundle, str) or not bundle.endswith(".json"):
        raise ValueError("invalid staged bundle")
    candidate = os.path.realpath(bundle)
    root = os.path.realpath(STAGING_ROOT)
    if os.path.dirname(candidate) != root:
        raise ValueError("staged bundle escapes ToolHub staging root")
    info = os.lstat(candidate)
    if not stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode) or info.st_size > MAX_BUNDLE:
        raise ValueError("staged bundle is not a safe regular file")
    return candidate


def _load_bundle(bundle):
    path = _safe_bundle(bundle)
    with open(path, "rb") as handle:
        value = json.load(handle)
    if not isinstance(value, dict):
        raise ValueError("staged bundle must contain an object")
    return path, value


def _managed_user(name):
    if not isinstance(name, str) or not name:
        raise ValueError("managed username is required")
    try:
        entry = pwd.getpwnam(name)
    except KeyError as exc:
        raise ValueError("managed user does not exist") from exc
    home = os.path.realpath(entry.pw_dir)
    if not home or home == "/" or not os.path.isdir(home) or os.path.islink(entry.pw_dir):
        raise ValueError("managed user home is unsafe")
    return entry, home


def _reject_symlink_components(root, target):
    root = os.path.abspath(root)
    target = os.path.abspath(target)
    if os.path.commonpath([root, target]) != root:
        raise ValueError("managed path escapes user home")
    current = root
    relative = os.path.relpath(target, root)
    for part in relative.split(os.sep):
        if part in {"", "."}:
            continue
        current = os.path.join(current, part)
        try:
            info = os.lstat(current)
        except FileNotFoundError:
            continue
        if stat.S_ISLNK(info.st_mode):
            raise ValueError("managed path contains a symlink")


def _target(bundle, write=False):
    target = bundle.get("target") or bundle.get("manifest", {}).get("target")
    if not isinstance(target, dict):
        raise ValueError("target is required")
    runtime = target.get("runtime")
    if runtime not in RUNTIMES:
        raise ValueError("unsupported runtime")
    if write and runtime == "hermes":
        raise PermissionError("Hermes is read-only")
    user, home = _managed_user(target.get("managedUsername"))
    # The Bridge resolves this independently through the fixed user.info call.
    expected_home = target.get("managedHome")
    if expected_home and expected_home != home:
        raise ValueError("managed user home changed after Salt capability check")
    root = os.path.join(home, ".%s" % runtime, "skills")
    if os.path.commonpath([home, os.path.abspath(root)]) != home:
        raise ValueError("runtime root escapes managed home")
    _reject_symlink_components(home, root)
    config_path = _mcp_path(home, runtime)
    if config_path:
        _reject_symlink_components(home, config_path)
    return target, user, home, root


def _mcp_path(home, runtime):
    if runtime == "claude":
        return os.path.join(home, ".claude.json")
    if runtime == "codex":
        return os.path.join(home, ".codex", "config.toml")
    return None


def _protected(name):
    lowered = name.strip().lower()
    return not lowered or lowered in {".", "..", ".system"} or lowered.startswith(".") or lowered.startswith("toolhub-")


def _tree_hash(root):
    digest = hashlib.sha256()
    files = 0
    total = 0
    if not os.path.exists(root):
        return digest.hexdigest()
    root_real = os.path.realpath(root)
    if root_real != os.path.abspath(root) or not os.path.isdir(root):
        raise ValueError("runtime root must be a real directory")
    for current, dirs, names in os.walk(root, followlinks=False):
        dirs.sort()
        names.sort()
        for name in list(dirs):
            if os.path.islink(os.path.join(current, name)):
                raise ValueError("symlink in runtime root")
        for name in names:
            path = os.path.join(current, name)
            info = os.lstat(path)
            if not stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode):
                raise ValueError("unsafe file type in runtime root")
            files += 1
            total += info.st_size
            if files > MAX_FILES or total > MAX_TOTAL or info.st_size > MAX_FILE:
                raise ValueError("runtime root exceeds safety limits")
            digest.update(os.path.relpath(path, root).replace(os.sep, "/").encode("utf-8"))
            digest.update(b"\0")
            with open(path, "rb") as handle:
                for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                    digest.update(chunk)
    return digest.hexdigest()


def _content_hash(root):
    digest = hashlib.sha256()
    entries = []
    for current, dirs, names in os.walk(root, followlinks=False):
        dirs.sort()
        names.sort()
        for name in dirs:
            if os.path.islink(os.path.join(current, name)):
                raise ValueError("symlink in Skill directory")
        for name in names:
            path = os.path.join(current, name)
            info = os.lstat(path)
            if not stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode):
                raise ValueError("unsafe file type in Skill directory")
            entries.append((os.path.relpath(path, root).replace(os.sep, "/"), info.st_mode & 0o777, path, info.st_size))
    for name, mode, path, size in sorted(entries):
        encoded = name.encode("utf-8")
        digest.update((str(len(encoded)) + ":").encode())
        digest.update(encoded)
        digest.update((":" + format(mode, "04o") + ":" + str(size) + ":").encode())
        with open(path, "rb") as handle:
            for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                digest.update(chunk)
    return digest.hexdigest()


def _scan_root(root):
    members = []
    if not os.path.exists(root):
        return {"targetRevision": _tree_hash(root), "members": members}
    for name in sorted(os.listdir(root)):
        path = os.path.join(root, name)
        protected = _protected(name)
        try:
            info = os.lstat(path)
            if not stat.S_ISDIR(info.st_mode) or stat.S_ISLNK(info.st_mode):
                protected = True
            content_hash = "" if protected else _content_hash(path)
            if not content_hash:
                protected = True
        except (OSError, ValueError):
            protected, content_hash = True, ""
        members.append({"id": "skill:" + name, "kind": "skill", "name": name,
                        "contentHash": content_hash, "protected": protected, "scope": "user"})
    return {"targetRevision": _tree_hash(root), "members": members}


def _json_hash(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def _read_mcp(home, runtime):
    path = _mcp_path(home, runtime)
    if not path or not os.path.exists(path):
        return {}, b""
    _reject_symlink_components(home, path)
    info = os.lstat(path)
    if not stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode) or info.st_size > 4 * 1024 * 1024:
        raise ValueError("runtime MCP config is not a safe regular file")
    with open(path, "rb") as handle:
        body = handle.read()
    if runtime == "claude":
        root = json.loads(body or b"{}")
        if not isinstance(root, dict):
            raise ValueError("Claude user configuration is not an object")
        servers = root.get("mcpServers", {})
    elif runtime == "codex":
        if tomllib is None:
            raise ValueError("Python tomllib is required for Codex MCP management")
        root = tomllib.loads(body.decode("utf-8")) if body.strip() else {}
        servers = root.get("mcp_servers", {})
    else:
        servers = {}
    if not isinstance(servers, dict):
        raise ValueError("runtime user MCP container is invalid")
    return servers, body


def _scan_mcp(home, runtime):
    servers, _ = _read_mcp(home, runtime)
    members = []
    for name in sorted(servers):
        value = servers[name]
        protected = _protected(name) or not isinstance(value, dict)
        members.append({"id": "mcp:" + name, "kind": "mcp", "name": name,
                        "contentHash": "" if protected else _json_hash(value),
                        "protected": protected, "scope": "user"})
    return members


def _scan_target(root, home, runtime):
    skills = _scan_root(root)
    mcp_members = _scan_mcp(home, runtime)
    _, config_body = _read_mcp(home, runtime)
    revision = hashlib.sha256((skills["targetRevision"] + "\n").encode() + config_body).hexdigest()
    return {"targetRevision": revision, "members": skills["members"] + mcp_members}


def scan(bundle):
    path = None
    try:
        path, value = _load_bundle(bundle)
        target, _, home, root = _target(value, write=False)
        return {"ok": True, **_scan_target(root, home, target["runtime"])}
    except Exception as exc:
        return _failure("invalid_request", str(exc))
    finally:
        _cleanup_bundle(path)


def preflight(bundle):
    path = None
    try:
        path, value = _load_bundle(bundle)
        target, _, home, root = _target(value, write=True)
        manifest = value.get("manifest") or {}
        current = _scan_target(root, home, target["runtime"])
        desired = {"skill:" + item["slug"]: item for item in manifest.get("skills", [])}
        desired.update({"mcp:" + item["name"]: item for item in manifest.get("mcpServers", [])})
        diff = {"add": [], "replace": [], "delete": [], "excluded": []}
        for member in current["members"]:
            item = {"kind": member["kind"], "name": member["name"]}
            identity = member["kind"] + ":" + member["name"]
            if member["protected"]:
                item["reason"] = "protected"
                diff["excluded"].append(item)
            elif identity not in desired:
                diff["delete"].append(item)
            else:
                wanted = desired.pop(identity)
                # Secret values are intentionally absent from preflight. A
                # present MCP member is conservatively listed as replace.
                differs = member["kind"] == "mcp" or wanted["contentHash"] != member["contentHash"]
                if differs:
                    item["memberId"] = wanted["memberId"]
                    diff["replace"].append(item)
        for identity, item in desired.items():
            kind = identity.split(":", 1)[0]
            diff["add"].append({"kind": kind, "name": item.get("slug", item.get("name")), "memberId": item["memberId"]})
        for items in diff.values():
            items.sort(key=lambda item: (item.get("kind", ""), item.get("name", "")))
        manifest_hash = hashlib.sha256(json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        return {"ok": True, "targetRevision": current["targetRevision"], "manifestHash": manifest_hash, "diff": diff}
    except PermissionError as exc:
        return _failure("hermes_read_only", str(exc))
    except Exception as exc:
        return _failure("invalid_request", str(exc))
    finally:
        _cleanup_bundle(path)


def _decode_artifacts(bundle):
    result = {}
    for item in bundle.get("artifacts", []):
        archive = base64.b64decode(item.get("archive", ""), validate=True)
        if not archive or len(archive) > MAX_ARCHIVE:
            raise ValueError("Skill archive exceeds safety limit")
        result[item["versionId"]] = (item["sha256"], archive)
    return result


def _resolved_mcp(manifest, secret_values):
    result = {}
    if not isinstance(secret_values, dict):
        raise ValueError("secretValues must be an object")
    for server in manifest.get("mcpServers", []):
        name = server.get("name")
        if _protected(name) or "/" in name or "\\" in name:
            raise ValueError("invalid or protected MCP server name")
        value = {}
        transport = server.get("transport")
        if transport == "stdio":
            value["command"] = server.get("command")
            value["args"] = server.get("args", [])
        elif transport in {"http", "sse"}:
            value["url"] = server.get("url")
            if transport == "sse":
                value["transport"] = "sse"
        else:
            raise ValueError("unsupported MCP transport")
        env = {}
        for key, reference in (server.get("envRefs") or {}).items():
            if reference not in secret_values:
                raise ValueError("missing MCP environment secret")
            env[key] = secret_values[reference]
        headers = {}
        for key, reference in (server.get("headerRefs") or {}).items():
            if reference not in secret_values:
                raise ValueError("missing MCP header secret")
            headers[key] = secret_values[reference]
        if env:
            value["env"] = env
        if headers:
            value["headers" if manifest["target"]["runtime"] == "claude" else "http_headers"] = headers
        if manifest["target"]["runtime"] == "claude" and transport in {"http", "sse"}:
            value["type"] = transport
        result[name] = value
    return result


def _mcp_drift(home, runtime, desired):
    actual, _ = _read_mcp(home, runtime)
    for name, wanted in desired.items():
        if name not in actual or not isinstance(actual[name], dict) or _json_hash(actual[name]) != _json_hash(wanted):
            return True
    return False


def _atomic_write(path, body, uid, gid):
    os.makedirs(os.path.dirname(path), mode=0o700, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=".toolhub-write-", dir=os.path.dirname(path))
    try:
        os.fchmod(descriptor, 0o600)
        os.write(descriptor, body)
        os.fsync(descriptor)
        os.close(descriptor)
        descriptor = None
        os.chown(temporary, uid, gid)
        os.rename(temporary, path)
        temporary = None
    finally:
        if descriptor is not None:
            os.close(descriptor)
        if temporary and os.path.exists(temporary):
            os.unlink(temporary)


def _write_claude_mcp(path, desired, preserve_unmanaged, uid, gid):
    root = {}
    if os.path.exists(path):
        with open(path, "rb") as handle:
            root = json.load(handle)
    if not isinstance(root, dict):
        raise ValueError("Claude user configuration is not an object")
    servers = root.get("mcpServers", {})
    if not isinstance(servers, dict):
        raise ValueError("Claude user mcpServers is not an object")
    next_servers = {}
    for name, value in servers.items():
        if preserve_unmanaged or _protected(name):
            next_servers[name] = value
    next_servers.update(desired)
    root["mcpServers"] = next_servers
    _atomic_write(path, (json.dumps(root, indent=2, sort_keys=True) + "\n").encode(), uid, gid)


def _codex_section_name(header):
    prefix = "mcp_servers."
    if not header.startswith(prefix):
        return None
    name = header[len(prefix):].split(".", 1)[0]
    if not name or any(char not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-" for char in name):
        return None
    return name


def _toml_string(value):
    return json.dumps(str(value), ensure_ascii=True)


def _render_codex_server(name, value):
    lines = ["[mcp_servers.%s]" % name]
    for key in ("command", "url", "transport"):
        if key in value:
            lines.append("%s = %s" % (key, _toml_string(value[key])))
    if "args" in value:
        lines.append("args = [%s]" % ", ".join(_toml_string(item) for item in value["args"]))
    for table in ("env", "http_headers"):
        if value.get(table):
            lines.append("")
            lines.append("[mcp_servers.%s.%s]" % (name, table))
            for key in sorted(value[table]):
                lines.append("%s = %s" % (_toml_string(key), _toml_string(value[table][key])))
    return "\n".join(lines) + "\n"


def _write_codex_mcp(path, desired, preserve_unmanaged, uid, gid):
    body = b""
    if os.path.exists(path):
        with open(path, "rb") as handle:
            body = handle.read()
    text = body.decode("utf-8")
    if tomllib is None:
        raise ValueError("Python tomllib is required for Codex MCP management")
    parsed = tomllib.loads(text) if text.strip() else {}
    servers = parsed.get("mcp_servers", {})
    if not isinstance(servers, dict):
        raise ValueError("Codex mcp_servers is not a table")
    lines = text.splitlines(keepends=True)
    retained = []
    index = 0
    while index < len(lines):
        stripped = lines[index].strip()
        if stripped.startswith("[") and stripped.endswith("]"):
            header = stripped[1:-1].strip()
            name = _codex_section_name(header)
            if name is not None:
                end = index + 1
                while end < len(lines):
                    candidate = lines[end].strip()
                    if candidate.startswith("[") and candidate.endswith("]"):
                        next_name = _codex_section_name(candidate[1:-1].strip())
                        if next_name != name:
                            break
                    end += 1
                if not preserve_unmanaged and not _protected(name):
                    index = end
                    continue
                if preserve_unmanaged and name in desired:
                    index = end
                    continue
        retained.append(lines[index])
        index += 1
    rendered = "".join(retained).rstrip() + "\n"
    for name in sorted(desired):
        rendered += "\n" + _render_codex_server(name, desired[name])
    _atomic_write(path, rendered.encode(), uid, gid)


def _write_mcp(home, runtime, desired, preserve_unmanaged, user):
    path = _mcp_path(home, runtime)
    if path is None:
        if desired:
            raise ValueError("runtime does not support managed MCP")
        return
    _reject_symlink_components(home, path)
    if runtime == "claude":
        _write_claude_mcp(path, desired, preserve_unmanaged, user.pw_uid, user.pw_gid)
    else:
        _write_codex_mcp(path, desired, preserve_unmanaged, user.pw_uid, user.pw_gid)


def _safe_extract(target, expected_hash, archive, uid, gid):
    if not isinstance(expected_hash, str) or len(expected_hash) != 64:
        raise ValueError("invalid artifact hash")
    os.makedirs(target, mode=0o755)
    total = 0
    files = 0
    with zipfile.ZipFile(io.BytesIO(archive)) as package:
        for item in package.infolist():
            if item.is_dir():
                continue
            name = item.filename
            normalized = os.path.normpath(name)
            mode = item.external_attr >> 16
            if (not name or name.startswith(("/", "\\")) or normalized in {".", ".."}
                    or normalized.startswith(".." + os.sep) or "\\" in name
                    or stat.S_ISLNK(mode)):
                raise ValueError("unsafe archive path")
            files += 1
            total += item.file_size
            if files > 2000 or total > 50 * 1024 * 1024 or item.file_size > MAX_FILE:
                raise ValueError("archive exceeds extraction limits")
            destination = os.path.abspath(os.path.join(target, normalized))
            if os.path.commonpath([os.path.abspath(target), destination]) != os.path.abspath(target):
                raise ValueError("archive path escapes Skill root")
            os.makedirs(os.path.dirname(destination), mode=0o755, exist_ok=True)
            with package.open(item) as source, open(destination, "xb") as output:
                shutil.copyfileobj(source, output, length=1024 * 1024)
            os.chmod(destination, mode & 0o777 or 0o644)
    for current, _, names in os.walk(target):
        os.chown(current, uid, gid)
        for name in names:
            os.chown(os.path.join(current, name), uid, gid)


def _copy_entry(source, destination):
    info = os.lstat(source)
    if stat.S_ISLNK(info.st_mode) or not (stat.S_ISDIR(info.st_mode) or stat.S_ISREG(info.st_mode)):
        raise ValueError("unsafe protected entry")
    if stat.S_ISDIR(info.st_mode):
        shutil.copytree(source, destination, symlinks=False)
    else:
        shutil.copy2(source, destination, follow_symlinks=False)


def _backup(root, home, target, revision, operation_id):
    backup_id = str(uuid.uuid4())
    destination = os.path.join(BACKUP_ROOT, target["id"], backup_id)
    os.makedirs(os.path.dirname(destination), mode=0o700, exist_ok=True)
    if os.path.exists(root):
        shutil.copytree(root, os.path.join(destination, "skills"), symlinks=False)
    else:
        os.makedirs(os.path.join(destination, "skills"), mode=0o700)
    config_path = _mcp_path(home, target["runtime"])
    if config_path and os.path.exists(config_path):
        _reject_symlink_components(home, config_path)
        shutil.copy2(config_path, os.path.join(destination, "mcp-config"), follow_symlinks=False)
    return {"id": backup_id, "targetId": target["id"], "nodeKind": target.get("nodeKind", "salt"),
            "saltMinionId": target.get("saltMinionId", ""), "runtime": target["runtime"],
            "sourceOperationId": operation_id, "revision": revision,
            "createdAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}, destination


def _replace(root, stage):
    old = os.path.join(os.path.dirname(root), ".toolhub-old-" + str(uuid.uuid4()))
    existed = os.path.exists(root)
    if existed:
        os.rename(root, old)
    try:
        os.rename(stage, root)
    except Exception:
        if existed:
            os.rename(old, root)
        raise
    if existed:
        shutil.rmtree(old)


def _mirror(root, manifest, artifacts, user, preserve_unmanaged):
    parent = os.path.dirname(root)
    os.makedirs(parent, mode=0o755, exist_ok=True)
    stage = tempfile.mkdtemp(prefix=".toolhub-stage-", dir=parent)
    try:
        if os.path.exists(root):
            for name in os.listdir(root):
                if preserve_unmanaged or _protected(name):
                    _copy_entry(os.path.join(root, name), os.path.join(stage, name))
        desired = set()
        for skill in manifest.get("skills", []):
            name = skill.get("slug")
            if _protected(name) or "/" in name or "\\" in name:
                raise ValueError("invalid or protected Skill slug")
            desired.add(name)
            version = skill.get("versionId")
            if version not in artifacts or artifacts[version][0] != skill.get("sha256"):
                raise ValueError("missing or mismatched Skill artifact")
            destination = os.path.join(stage, name)
            if os.path.exists(destination):
                shutil.rmtree(destination)
            _safe_extract(destination, skill["sha256"], artifacts[version][1], user.pw_uid, user.pw_gid)
        if not preserve_unmanaged:
            for name in os.listdir(stage):
                if not _protected(name) and name not in desired:
                    path = os.path.join(stage, name)
                    shutil.rmtree(path) if os.path.isdir(path) else os.unlink(path)
        _tree_hash(stage)
        _replace(root, stage)
        stage = None
    finally:
        if stage and os.path.exists(stage):
            shutil.rmtree(stage)


def _restore_backup(root, home, runtime, backup_path, user):
    skills_source = os.path.join(backup_path, "skills")
    stage = tempfile.mkdtemp(prefix=".toolhub-restore-", dir=os.path.dirname(root))
    shutil.rmtree(stage)
    shutil.copytree(skills_source, stage, symlinks=False)
    _replace(root, stage)
    config_path = _mcp_path(home, runtime)
    config_source = os.path.join(backup_path, "mcp-config")
    if config_path:
        _reject_symlink_components(home, config_path)
        if os.path.exists(config_source):
            with open(config_source, "rb") as handle:
                _atomic_write(config_path, handle.read(), user.pw_uid, user.pw_gid)
        elif os.path.exists(config_path):
            os.unlink(config_path)


def apply(bundle):
    return _write(bundle, preserve_unmanaged=False)


def reconcile(bundle):
    return _write(bundle, preserve_unmanaged=True)


def _write(bundle, preserve_unmanaged):
    path = None
    try:
        path, value = _load_bundle(bundle)
        target, user, home, root = _target(value, write=True)
        manifest = value.get("manifest") or {}
        current = _scan_target(root, home, target["runtime"])
        expected = value.get("expectedRevision")
        if expected and expected != current["targetRevision"]:
            return _failure("revision_conflict", "target changed before commit")
        current_by_name = {item["name"]: item for item in current["members"] if item["kind"] == "skill"}
        desired_mcp = _resolved_mcp(manifest, value.get("secretValues", {}))
        drift = any((not current_by_name.get(skill["slug"])
                     or current_by_name[skill["slug"]]["protected"]
                     or current_by_name[skill["slug"]]["contentHash"] != skill["contentHash"])
                    for skill in manifest.get("skills", [])) or _mcp_drift(home, target["runtime"], desired_mcp)
        if preserve_unmanaged and not drift:
            return {"ok": True, "status": "succeeded", "health": "healthy",
                    "targetRevision": current["targetRevision"], "repaired": False}
        backup, backup_path = _backup(root, home, target, current["targetRevision"], value.get("operationId", ""))
        try:
            _mirror(root, manifest, _decode_artifacts(value), user, preserve_unmanaged)
            _write_mcp(home, target["runtime"], desired_mcp, preserve_unmanaged, user)
        except Exception:
            _restore_backup(root, home, target["runtime"], backup_path, user)
            raise
        after = _scan_target(root, home, target["runtime"])
        return {"ok": True, "status": "succeeded", "health": "healthy",
                "targetRevision": after["targetRevision"], "backup": backup,
                "backupId": backup["id"], "repaired": preserve_unmanaged}
    except PermissionError as exc:
        return _failure("hermes_read_only", str(exc))
    except Exception as exc:
        return _failure("atomic_write_failed", str(exc))
    finally:
        _cleanup_bundle(path)


def restore(bundle):
    path = None
    try:
        path, value = _load_bundle(bundle)
        target, user, home, root = _target(value, write=True)
        backup_id = value.get("backupId")
        if not isinstance(backup_id, str) or not backup_id:
            raise ValueError("backup id is required")
        source = os.path.realpath(os.path.join(BACKUP_ROOT, target["id"], backup_id))
        expected_root = os.path.realpath(os.path.join(BACKUP_ROOT, target["id"]))
        if os.path.dirname(source) != expected_root or not os.path.isdir(source):
            raise ValueError("backup does not exist")
        current = _scan_target(root, home, target["runtime"])
        expected_revision = value.get("expectedRevision")
        if expected_revision != current["targetRevision"]:
            return _failure("revision_conflict", "target changed before restore")
        manifest = value.get("manifest") or {}
        desired_mcp = _resolved_mcp(manifest, value.get("secretValues", {}))
        recovery, recovery_path = _backup(root, home, target, current["targetRevision"], value.get("operationId", ""))
        try:
            _restore_backup(root, home, target["runtime"], source, user)
            after = _scan_target(root, home, target["runtime"])
            if not _snapshot_matches(after["members"], manifest, desired_mcp):
                raise ValueError("restored managed content does not match the pinned backup manifest")
        except Exception:
            _restore_backup(root, home, target["runtime"], recovery_path, user)
            raise
        return {"ok": True, "status": "succeeded", "health": "healthy",
                "targetRevision": after["targetRevision"], "backup": recovery,
                "backupId": recovery["id"]}
    except Exception as exc:
        return _failure("backup_failed", str(exc))
    finally:
        _cleanup_bundle(path)


def _snapshot_matches(members, manifest, desired_mcp):
    actual = {(item.get("kind"), item.get("name")): item for item in members}
    for skill in manifest.get("skills", []):
        item = actual.get(("skill", skill.get("slug")))
        if not item or item.get("protected") or item.get("contentHash") != skill.get("contentHash"):
            return False
    for name, value in desired_mcp.items():
        item = actual.get(("mcp", name))
        if not item or item.get("protected") or item.get("contentHash") != _json_hash(value):
            return False
    return True


def remove_backup(target_id, backup_id):
    try:
        if not isinstance(target_id, str) or not isinstance(backup_id, str):
            raise ValueError("backup identifiers are required")
        root = os.path.realpath(os.path.join(BACKUP_ROOT, target_id))
        candidate = os.path.realpath(os.path.join(root, backup_id))
        if os.path.dirname(candidate) != root:
            raise ValueError("backup path escapes target catalog")
        shutil.rmtree(candidate)
        return {"ok": True}
    except FileNotFoundError:
        return {"ok": True}
    except Exception as exc:
        return _failure("backup_failed", str(exc))


def cleanup_bundle(bundle):
    try:
        path = _safe_bundle(bundle)
        os.unlink(path)
        return {"ok": True}
    except FileNotFoundError:
        return {"ok": True}
    except Exception as exc:
        return _failure("invalid_request", str(exc))


def _cleanup_bundle(path):
    if path:
        try:
            os.unlink(path)
        except FileNotFoundError:
            pass
