"""Immutable capability releases and live activation for a single-node deployment."""

from __future__ import annotations

import asyncio
import errno
import hashlib
import json
import re
import shutil
import sqlite3
import tempfile
import time
from pathlib import Path
from typing import Any, cast

from pydantic import JsonValue

from agenstra.contracts import PackManifest
from agenstra.packs import load_pack
from agenstra.rest import RestManifest, load_rest_pack
from agenstra.skills import load_skill_files

_ID = re.compile(r"[A-Za-z][A-Za-z0-9_.-]{0,127}\Z")
_DIGEST = re.compile(r"[a-f0-9]{64}\Z")
_MAX_MANIFEST = 1_000_000
_MAX_SKILLS = 1_000_000


class RegistryError(ValueError):
    """Invalid package, missing release, or conflicting management write."""

    def __init__(self, code: str) -> None:
        super().__init__(code)
        self.code = code


def _canonical(value: object) -> bytes:
    return json.dumps(
        value, sort_keys=True, ensure_ascii=False, separators=(",", ":"), allow_nan=False
    ).encode()


def _identifier(value: str) -> str:
    if not _ID.fullmatch(value) or value in {".", ".."}:
        raise RegistryError("invalid_pack_id")
    return value


def _skill_path(value: str) -> Path:
    if (
        not value
        or value.startswith("/")
        or "\\" in value
        or any(part in {"", ".", ".."} for part in value.split("/"))
        or value.split("/")[0] == "pack.json"
    ):
        raise RegistryError("invalid_skill_path")
    return Path(value)


def _skill_entries(manifest: dict[str, Any]) -> dict[str, str]:
    entries = manifest.get("skills", [])
    if not isinstance(entries, list):
        raise RegistryError("invalid_skills")
    result: dict[str, str] = {}
    for entry in entries:
        if not isinstance(entry, dict) or not isinstance(entry.get("path"), str):
            raise RegistryError("invalid_skills")
        path = entry["path"]
        _skill_path(path)
        if path in result:
            raise RegistryError("duplicate_skill_path")
        result[path] = str(entry.get("sha256", ""))
    paths = set(result)
    if any(
        ancestor.as_posix() in paths
        for path in paths
        for ancestor in Path(path).parents
        if ancestor != Path(".")
    ):
        raise RegistryError("skill_path_conflict")
    return result


def _summary(manifest: dict[str, Any]) -> list[dict[str, str]]:
    schema = manifest.get("schema")
    if schema == "agenstra.rest-pack.v2":
        items = manifest.get("capabilities", [])
        return [{"name": item["name"], "effect": item["effect"]} for item in items]
    if schema == "agenstra.mcp-pack.v1":
        items = manifest.get("tools", [])
        return [{"name": item["name"], "effect": item["effect"]} for item in items]
    if schema == "agenstra.capability-pack.v1":
        items = manifest.get("capabilities", [])
        return [{"name": item["name"], "effect": "compute"} for item in items]
    raise RegistryError("unsupported_pack_schema")


class CapabilityRegistry:
    def __init__(self, database_path: str | Path, package_dir: str | Path) -> None:
        self.database_path = Path(database_path)
        self.package_dir = Path(package_dir)
        if self.database_path.resolve() == self.package_dir.resolve():
            raise ValueError("registry database and package directory must differ")

    def _connect(self) -> sqlite3.Connection:
        db = sqlite3.connect(self.database_path, timeout=30, isolation_level=None)
        db.row_factory = sqlite3.Row
        db.execute("PRAGMA busy_timeout=30000")
        db.execute("PRAGMA foreign_keys=ON")
        return db

    def _read(self, action: Any) -> Any:
        db = self._connect()
        try:
            return action(db)
        finally:
            db.close()

    def _write(self, action: Any) -> Any:
        db = self._connect()
        try:
            db.execute("BEGIN IMMEDIATE")
            try:
                result = action(db)
                db.execute("COMMIT")
                return result
            except BaseException:
                db.execute("ROLLBACK")
                raise
        finally:
            db.close()

    async def initialize(self) -> None:
        await asyncio.to_thread(self._initialize)

    def _initialize(self) -> None:
        self.database_path.parent.mkdir(parents=True, exist_ok=True)
        self.package_dir.mkdir(parents=True, exist_ok=True)
        db = self._connect()
        try:
            db.execute("PRAGMA journal_mode=WAL")
            db.execute("BEGIN IMMEDIATE")
            try:
                for statement in (
                    "CREATE TABLE IF NOT EXISTS releases ("
                    "pack_id TEXT NOT NULL, digest TEXT NOT NULL, version TEXT NOT NULL, "
                    "manifest_json TEXT NOT NULL, capabilities_json TEXT NOT NULL, "
                    "created_at REAL NOT NULL, PRIMARY KEY(pack_id, digest), "
                    "UNIQUE(pack_id, version))",
                    "CREATE TABLE IF NOT EXISTS active ("
                    "pack_id TEXT PRIMARY KEY, digest TEXT NOT NULL, revision INTEGER NOT NULL, "
                    "FOREIGN KEY(pack_id, digest) REFERENCES releases(pack_id, digest))",
                    "CREATE TABLE IF NOT EXISTS bindings ("
                    "owner_id TEXT NOT NULL, pack_id TEXT NOT NULL, config_json TEXT NOT NULL, "
                    "enabled INTEGER NOT NULL, updated_at REAL NOT NULL, "
                    "PRIMARY KEY(owner_id, pack_id))",
                    "CREATE TABLE IF NOT EXISTS audit ("
                    "sequence INTEGER PRIMARY KEY AUTOINCREMENT, created_at REAL NOT NULL, "
                    "action TEXT NOT NULL, pack_id TEXT NOT NULL, detail_json TEXT NOT NULL)",
                ):
                    db.execute(statement)
                db.execute("COMMIT")
            except BaseException:
                db.execute("ROLLBACK")
                raise
        finally:
            db.close()

    @staticmethod
    def _audit(
        db: sqlite3.Connection, action: str, pack_id: str, detail: dict[str, object]
    ) -> None:
        db.execute(
            "INSERT INTO audit(created_at, action, pack_id, detail_json) VALUES(?,?,?,?)",
            (time.time(), action, pack_id, _canonical(detail).decode()),
        )

    def _staging(self, manifest: dict[str, Any], skills: dict[str, str]) -> Path:
        try:
            raw = _canonical(manifest)
        except (TypeError, ValueError) as error:
            raise RegistryError("invalid_capability_pack") from error
        if len(raw) > _MAX_MANIFEST or sum(len(x.encode()) for x in skills.values()) > _MAX_SKILLS:
            raise RegistryError("package_too_large")
        entries = _skill_entries(manifest)
        if set(skills) != set(entries):
            raise RegistryError("skill_files_mismatch")
        stage = Path(tempfile.mkdtemp(prefix=".draft-", dir=self.package_dir))
        try:
            (stage / "pack.json").write_bytes(raw)
            for name, content in skills.items():
                target = stage / _skill_path(name)
                target.parent.mkdir(parents=True, exist_ok=True)
                data = content.encode("utf-8")
                if hashlib.sha256(data).hexdigest() != entries[name]:
                    raise RegistryError("skill_digest_mismatch")
                target.write_bytes(data)
            return stage
        except BaseException:
            shutil.rmtree(stage)
            raise

    async def _validate(self, stage: Path, pack_id: str, version: str) -> list[dict[str, str]]:
        manifest = cast(dict[str, Any], json.loads((stage / "pack.json").read_text()))
        if manifest.get("name") != pack_id:
            raise RegistryError("pack_name_mismatch")
        if (
            not version
            or len(version) > 40
            or ("version" in manifest and manifest["version"] != version)
        ):
            raise RegistryError("pack_version_mismatch")
        schema = manifest.get("schema")
        try:
            if schema == "agenstra.rest-pack.v2":
                parsed = RestManifest.model_validate(manifest)
                environment = {parsed.base_url_env: "https://validation.invalid"}
                if parsed.token_env:
                    environment[parsed.token_env] = "validation-token"
                environment.update(dict.fromkeys(parsed.headers_env.values(), "validation-header"))
                pack = load_rest_pack(stage / "pack.json", environment=environment)
                await pack.aclose()
            elif schema == "agenstra.mcp-pack.v1":
                try:
                    from agenstra.mcp import McpPackManifest
                except ModuleNotFoundError as error:
                    raise RegistryError("mcp_extra_required") from error
                parsed_mcp = McpPackManifest.model_validate(manifest)
                skills = load_skill_files(parsed_mcp.skills, stage)
                names = [item.name for item in parsed_mcp.tools]
                if len(names) != len(set(names)) or any(
                    not set(item.skills).issubset(skills) for item in parsed_mcp.tools
                ):
                    raise RegistryError("invalid_capability_catalog")
            elif schema == "agenstra.capability-pack.v1":
                parsed_legacy = PackManifest.model_validate(manifest)
                environment = {
                    item.url_env: "https://validation.invalid"
                    for item in parsed_legacy.capabilities
                }
                environment.update(
                    {
                        item.token_env: "validation-token"
                        for item in parsed_legacy.capabilities
                        if item.token_env
                    }
                )
                pack_legacy = load_pack(stage / "pack.json", environment=environment)
                await pack_legacy.aclose()
            else:
                raise RegistryError("unsupported_pack_schema")
        except RegistryError:
            raise
        except (ValueError, TypeError, OSError) as error:
            raise RegistryError("invalid_capability_pack") from error
        return _summary(manifest)

    async def validate(
        self, pack_id: str, version: str, manifest: dict[str, Any], skills: dict[str, str]
    ) -> list[dict[str, str]]:
        _identifier(pack_id)
        stage = self._staging(manifest, skills)
        try:
            return await self._validate(stage, pack_id, version)
        finally:
            shutil.rmtree(stage)

    async def publish(
        self, pack_id: str, version: str, manifest: dict[str, Any], skills: dict[str, str]
    ) -> dict[str, Any]:
        _identifier(pack_id)
        stage = self._staging(manifest, skills)
        try:
            capabilities = await self._validate(stage, pack_id, version)
            digest = hashlib.sha256(
                _canonical(
                    {"pack_id": pack_id, "version": version, "manifest": manifest, "skills": skills}
                )
            ).hexdigest()

            def previous(db: sqlite3.Connection) -> str | None:
                row = db.execute(
                    "SELECT digest FROM releases WHERE pack_id=? AND version=?", (pack_id, version)
                ).fetchone()
                return str(row["digest"]) if row else None

            prior = cast(str | None, await asyncio.to_thread(self._read, previous))
            if prior is not None and prior != digest:
                raise RegistryError("version_already_published")
            parent = self.package_dir / pack_id
            if parent.is_symlink():
                raise RegistryError("release_path_conflict")
            parent.mkdir(exist_ok=True)
            if parent.is_symlink():
                raise RegistryError("release_path_conflict")
            target = parent / digest
            try:
                stage.rename(target)
            except OSError as error:
                if error.errno not in {errno.EEXIST, errno.ENOTEMPTY} or not target.is_dir():
                    raise
                expected = {
                    path.relative_to(stage): path.read_bytes()
                    for path in stage.rglob("*")
                    if path.is_file()
                }
                found = {
                    path.relative_to(target): path.read_bytes()
                    for path in target.rglob("*")
                    if path.is_file() and not path.is_symlink()
                }
                if (
                    target.is_symlink()
                    or any(path.is_symlink() for path in target.rglob("*"))
                    or found != expected
                ):
                    raise RegistryError("release_path_conflict") from None
            now = time.time()

            def action(db: sqlite3.Connection) -> dict[str, Any]:
                existing = db.execute(
                    "SELECT digest, created_at FROM releases WHERE pack_id=? AND version=?",
                    (pack_id, version),
                ).fetchone()
                if existing is not None and existing["digest"] != digest:
                    raise RegistryError("version_already_published")
                if existing is None:
                    db.execute(
                        "INSERT INTO releases VALUES(?,?,?,?,?,?)",
                        (
                            pack_id,
                            digest,
                            version,
                            _canonical(manifest).decode(),
                            _canonical(capabilities).decode(),
                            now,
                        ),
                    )
                    self._audit(db, "publish", pack_id, {"version": version, "digest": digest})
                return {
                    "pack_id": pack_id,
                    "version": version,
                    "digest": digest,
                    "capabilities": capabilities,
                    "created_at": existing["created_at"] if existing else now,
                }

            return await asyncio.to_thread(self._write, action)
        finally:
            if stage.exists():
                shutil.rmtree(stage)

    async def list_packs(self) -> list[dict[str, Any]]:
        def action(db: sqlite3.Connection) -> list[dict[str, Any]]:
            rows = db.execute(
                "SELECT r.pack_id, r.version, r.digest, r.capabilities_json, r.created_at, "
                "a.digest AS active_digest, a.revision FROM releases r "
                "LEFT JOIN active a ON a.pack_id=r.pack_id "
                "ORDER BY r.pack_id, r.created_at DESC"
            ).fetchall()
            return [
                {
                    "pack_id": row["pack_id"],
                    "version": row["version"],
                    "digest": row["digest"],
                    "capabilities": json.loads(row["capabilities_json"]),
                    "created_at": row["created_at"],
                    "active": row["digest"] == row["active_digest"],
                    "revision": row["revision"] or 0,
                }
                for row in rows
            ]

        return cast(list[dict[str, Any]], await asyncio.to_thread(self._read, action))

    async def active_release(self, pack_id: str) -> str | None:
        _identifier(pack_id)

        def action(db: sqlite3.Connection) -> str | None:
            row = db.execute("SELECT digest FROM active WHERE pack_id=?", (pack_id,)).fetchone()
            return str(row["digest"]) if row else None

        return cast(str | None, await asyncio.to_thread(self._read, action))

    async def activate(
        self, pack_id: str, digest: str, *, expected_revision: int | None = None
    ) -> dict[str, Any]:
        _identifier(pack_id)
        if not _DIGEST.fullmatch(digest):
            raise RegistryError("invalid_release_id")
        if expected_revision is not None and expected_revision < 0:
            raise RegistryError("invalid_revision")
        await self.release_path(pack_id, digest)

        def action(db: sqlite3.Connection) -> dict[str, Any]:
            release = db.execute(
                "SELECT version, capabilities_json FROM releases WHERE pack_id=? AND digest=?",
                (pack_id, digest),
            ).fetchone()
            if release is None:
                raise RegistryError("release_not_found")
            names = {item["name"] for item in json.loads(release["capabilities_json"])}
            bindings = db.execute(
                "SELECT config_json FROM bindings WHERE pack_id=? AND enabled=1", (pack_id,)
            ).fetchall()
            for binding in bindings:
                config = json.loads(binding["config_json"])
                allowed = set(config.get("granted_capabilities", [])) | set(
                    config.get("approval_capabilities", [])
                )
                if allowed - names:
                    raise RegistryError("binding_capability_missing")
            current = db.execute(
                "SELECT digest, revision FROM active WHERE pack_id=?", (pack_id,)
            ).fetchone()
            revision = int(current["revision"]) if current else 0
            if expected_revision is not None and revision != expected_revision:
                raise RegistryError("revision_conflict")
            if current is None or current["digest"] != digest:
                revision += 1
                db.execute(
                    "INSERT INTO active(pack_id,digest,revision) VALUES(?,?,?) "
                    "ON CONFLICT(pack_id) DO UPDATE SET digest=excluded.digest,"
                    "revision=excluded.revision",
                    (pack_id, digest, revision),
                )
                self._audit(db, "activate", pack_id, {"digest": digest, "revision": revision})
            return {
                "pack_id": pack_id,
                "version": release["version"],
                "digest": digest,
                "revision": revision,
            }

        return cast(dict[str, Any], await asyncio.to_thread(self._write, action))

    async def release_path(self, pack_id: str, digest: str) -> Path:
        _identifier(pack_id)
        if not _DIGEST.fullmatch(digest):
            raise RegistryError("invalid_release_id")

        def action(db: sqlite3.Connection) -> tuple[str, dict[str, Any]]:
            row = db.execute(
                "SELECT version, manifest_json FROM releases WHERE pack_id=? AND digest=?",
                (pack_id, digest),
            ).fetchone()
            if row is None:
                raise RegistryError("release_not_found")
            return str(row["version"]), cast(dict[str, Any], json.loads(row["manifest_json"]))

        version, manifest = cast(
            tuple[str, dict[str, Any]], await asyncio.to_thread(self._read, action)
        )
        parent = self.package_dir / pack_id
        directory = parent / digest
        path = directory / "pack.json"
        try:
            if parent.is_symlink() or directory.is_symlink() or path.is_symlink():
                raise RegistryError("release_tampered")
            stored = cast(dict[str, Any], json.loads(path.read_text(encoding="utf-8")))
            entries = _skill_entries(stored)
            expected_files = {Path("pack.json"), *(_skill_path(name) for name in entries)}
            expected_dirs = {
                ancestor
                for file in expected_files
                for ancestor in file.parents
                if ancestor != Path(".")
            }
            actual_entries = {entry.relative_to(directory): entry for entry in directory.rglob("*")}
            if set(actual_entries) != expected_files | expected_dirs or any(
                entry.is_symlink()
                or (relative in expected_files and not entry.is_file())
                or (relative in expected_dirs and not entry.is_dir())
                for relative, entry in actual_entries.items()
            ):
                raise RegistryError("release_tampered")
            skills: dict[str, str] = {}
            for name in entries:
                source = directory / _skill_path(name)
                skills[name] = source.read_text(encoding="utf-8")
            actual_digest = hashlib.sha256(
                _canonical(
                    {"pack_id": pack_id, "version": version, "manifest": stored, "skills": skills}
                )
            ).hexdigest()
            if stored != manifest or actual_digest != digest:
                raise RegistryError("release_tampered")
        except (OSError, UnicodeError, ValueError, TypeError) as error:
            raise RegistryError("release_tampered") from error
        return path

    async def put_binding(self, owner_id: str, pack_id: str, config: dict[str, JsonValue]) -> None:
        _identifier(pack_id)
        if not owner_id or len(owner_id) > 128:
            raise RegistryError("invalid_owner_id")

        def action(db: sqlite3.Connection) -> None:
            active = db.execute(
                "SELECT r.capabilities_json FROM active a JOIN releases r "
                "ON r.pack_id=a.pack_id AND r.digest=a.digest WHERE a.pack_id=?",
                (pack_id,),
            ).fetchone()
            if active is None:
                raise RegistryError("release_not_active")
            names = {item["name"] for item in json.loads(active["capabilities_json"])}
            granted = set(cast(list[str], config.get("granted_capabilities", [])))
            approvals = set(cast(list[str], config.get("approval_capabilities", [])))
            if (granted | approvals) - names:
                raise RegistryError("binding_capability_missing")
            db.execute(
                "INSERT INTO bindings VALUES(?,?,?,?,?) ON CONFLICT(owner_id,pack_id) "
                "DO UPDATE SET config_json=excluded.config_json,enabled=1,"
                "updated_at=excluded.updated_at",
                (owner_id, pack_id, _canonical(config).decode(), 1, time.time()),
            )
            self._audit(db, "bind", pack_id, {"owner_id": owner_id})

        await asyncio.to_thread(self._write, action)

    async def disable_binding(self, owner_id: str, pack_id: str) -> None:
        _identifier(pack_id)

        def action(db: sqlite3.Connection) -> None:
            row = db.execute(
                "SELECT 1 FROM bindings WHERE owner_id=? AND pack_id=?", (owner_id, pack_id)
            ).fetchone()
            if row is None:
                raise RegistryError("binding_not_found")
            db.execute(
                "UPDATE bindings SET enabled=0,updated_at=? WHERE owner_id=? AND pack_id=?",
                (time.time(), owner_id, pack_id),
            )
            self._audit(db, "disable_binding", pack_id, {"owner_id": owner_id})

        await asyncio.to_thread(self._write, action)

    async def binding(
        self, owner_id: str, pack_id: str
    ) -> tuple[bool, dict[str, JsonValue] | None]:
        _identifier(pack_id)

        def action(db: sqlite3.Connection) -> tuple[bool, dict[str, JsonValue] | None]:
            row = db.execute(
                "SELECT config_json,enabled FROM bindings WHERE owner_id=? AND pack_id=?",
                (owner_id, pack_id),
            ).fetchone()
            if row is None:
                return False, None
            if not row["enabled"]:
                return True, None
            return True, cast(dict[str, JsonValue], json.loads(row["config_json"]))

        return cast(
            tuple[bool, dict[str, JsonValue] | None], await asyncio.to_thread(self._read, action)
        )

    async def list_bindings(self) -> list[dict[str, Any]]:
        def action(db: sqlite3.Connection) -> list[dict[str, Any]]:
            rows = db.execute(
                "SELECT owner_id,pack_id,config_json,enabled,updated_at FROM bindings "
                "ORDER BY owner_id,pack_id"
            ).fetchall()
            return [
                {
                    "owner_id": row["owner_id"],
                    "pack_id": row["pack_id"],
                    "config": json.loads(row["config_json"]),
                    "enabled": bool(row["enabled"]),
                    "updated_at": row["updated_at"],
                }
                for row in rows
            ]

        return cast(list[dict[str, Any]], await asyncio.to_thread(self._read, action))

    async def audit(self, *, limit: int = 100) -> list[dict[str, Any]]:
        if limit < 1 or limit > 1000:
            raise RegistryError("invalid_limit")

        def action(db: sqlite3.Connection) -> list[dict[str, Any]]:
            rows = db.execute(
                "SELECT sequence,created_at,action,pack_id,detail_json FROM audit "
                "ORDER BY sequence DESC LIMIT ?",
                (limit,),
            ).fetchall()
            return [
                {
                    "sequence": row["sequence"],
                    "created_at": row["created_at"],
                    "action": row["action"],
                    "pack_id": row["pack_id"],
                    "detail": json.loads(row["detail_json"]),
                }
                for row in rows
            ]

        return cast(list[dict[str, Any]], await asyncio.to_thread(self._read, action))
