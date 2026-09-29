"""Durable, owner-scoped run storage with fenced SQLite leases."""

import asyncio
import hashlib
import json
import sqlite3
import time
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import TypeVar, cast
from uuid import uuid4

from pydantic import JsonValue


class StoreConflict(Exception):
    """A requested write conflicts with an existing record or live lease."""


class RunNotFound(Exception):
    """The run does not exist for this owner."""


class LeaseLost(Exception):
    """The caller no longer holds the live lease."""


@dataclass(frozen=True)
class StoredRun:
    run_id: str
    owner_id: str
    pack_id: str
    status: str
    state: dict[str, JsonValue]
    revision: int
    next_wake_at: float | None
    lease_token: str | None
    lease_until: float | None
    created_at: float
    updated_at: float
    cancel_requested: bool = False


_T = TypeVar("_T")
_SCHEMA_VERSION = 1


def _encode(value: JsonValue) -> str:
    return json.dumps(
        value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False
    )


def _decode_object(value: str) -> dict[str, JsonValue]:
    parsed = json.loads(value)
    if not isinstance(parsed, dict):
        raise ValueError("stored JSON must be an object")
    return cast(dict[str, JsonValue], parsed)


def _run(row: sqlite3.Row) -> StoredRun:
    return StoredRun(
        run_id=row["run_id"],
        owner_id=row["owner_id"],
        pack_id=row["pack_id"],
        status=row["status"],
        state=_decode_object(row["state_json"]),
        revision=row["revision"],
        next_wake_at=row["next_wake_at"],
        lease_token=row["lease_token"],
        lease_until=row["lease_until"],
        created_at=row["created_at"],
        updated_at=row["updated_at"],
        cancel_requested=bool(row["cancel_requested"]),
    )


class SQLiteStore:
    """Each operation uses a fresh connection; writes use a short IMMEDIATE transaction."""

    def __init__(self, path: str | Path, *, clock: Callable[[], float] = time.time) -> None:
        self.path = Path(path)
        if str(self.path) == ":memory:":
            raise ValueError("SQLiteStore requires a disk path")
        self.clock = clock

    def _connect(self) -> sqlite3.Connection:
        connection = sqlite3.connect(self.path, timeout=30, isolation_level=None)
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA foreign_keys=ON")
        connection.execute("PRAGMA busy_timeout=30000")
        return connection

    def _read(self, action: Callable[[sqlite3.Connection], _T]) -> _T:
        connection = self._connect()
        try:
            return action(connection)
        finally:
            connection.close()

    def _write(self, action: Callable[[sqlite3.Connection], _T]) -> _T:
        connection = self._connect()
        try:
            connection.execute("BEGIN IMMEDIATE")
            try:
                result = action(connection)
                connection.execute("COMMIT")
                return result
            except BaseException:
                connection.execute("ROLLBACK")
                raise
        finally:
            connection.close()

    async def initialize(self) -> None:
        await asyncio.to_thread(self._initialize)

    def _initialize(self) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        connection = self._connect()
        try:
            deadline = time.monotonic() + 30
            while True:
                try:
                    mode = connection.execute("PRAGMA journal_mode=WAL").fetchone()[0]
                    if str(mode).lower() != "wal":
                        raise RuntimeError("SQLite store requires WAL journal mode")
                    break
                except sqlite3.OperationalError as error:
                    if "locked" not in str(error).lower() or time.monotonic() >= deadline:
                        raise
                    time.sleep(0.01)
            connection.execute("BEGIN IMMEDIATE")
            try:
                version = int(connection.execute("PRAGMA user_version").fetchone()[0])
                if version > _SCHEMA_VERSION:
                    raise RuntimeError(f"unsupported SQLite store schema version {version}")
                if version == 0:
                    schema = """
                        CREATE TABLE IF NOT EXISTS runs (
                            run_id TEXT PRIMARY KEY,
                            owner_id TEXT NOT NULL,
                            pack_id TEXT NOT NULL,
                            status TEXT NOT NULL,
                            state_json TEXT NOT NULL,
                            revision INTEGER NOT NULL,
                            next_wake_at REAL,
                            lease_token TEXT,
                            lease_until REAL,
                            created_at REAL NOT NULL,
                            updated_at REAL NOT NULL,
                            cancel_requested INTEGER NOT NULL DEFAULT 0
                        );
                        CREATE INDEX IF NOT EXISTS runs_due
                            ON runs(status, next_wake_at, lease_until);
                        CREATE INDEX IF NOT EXISTS runs_owner ON runs(owner_id, created_at);
                        CREATE TABLE IF NOT EXISTS invocations (
                            run_id TEXT NOT NULL REFERENCES runs(run_id),
                            invocation_id TEXT NOT NULL,
                            payload_json TEXT NOT NULL,
                            PRIMARY KEY (run_id, invocation_id)
                        );
                        CREATE TABLE IF NOT EXISTS artifacts (
                            run_id TEXT NOT NULL REFERENCES runs(run_id),
                            artifact_id TEXT NOT NULL,
                            payload_json TEXT NOT NULL,
                            sha256 TEXT NOT NULL,
                            PRIMARY KEY (run_id, artifact_id)
                        );
                        CREATE TABLE IF NOT EXISTS events (
                            sequence INTEGER PRIMARY KEY AUTOINCREMENT,
                            run_id TEXT NOT NULL REFERENCES runs(run_id),
                            created_at REAL NOT NULL,
                            event_json TEXT NOT NULL
                        );
                        CREATE INDEX IF NOT EXISTS events_run ON events(run_id, sequence);
                        """
                    for statement in schema.split(";"):
                        if statement.strip():
                            connection.execute(statement)
                    connection.execute(f"PRAGMA user_version={_SCHEMA_VERSION}")
                connection.execute("COMMIT")
            except BaseException:
                if connection.in_transaction:
                    connection.execute("ROLLBACK")
                raise
        finally:
            connection.close()

    @staticmethod
    def _owned(connection: sqlite3.Connection, run_id: str, owner_id: str) -> sqlite3.Row:
        row = connection.execute(
            "SELECT * FROM runs WHERE run_id=? AND owner_id=?", (run_id, owner_id)
        ).fetchone()
        if row is None:
            raise RunNotFound(run_id)
        return cast(sqlite3.Row, row)

    def _leased(
        self, connection: sqlite3.Connection, run_id: str, owner_id: str, lease_token: str
    ) -> sqlite3.Row:
        row = self._owned(connection, run_id, owner_id)
        if row["lease_token"] != lease_token or row["lease_until"] is None:
            raise LeaseLost(run_id)
        if row["lease_until"] <= self.clock():
            raise LeaseLost(run_id)
        return row

    async def create_run(
        self,
        *,
        owner_id: str,
        pack_id: str,
        state: dict[str, JsonValue],
        run_id: str | None = None,
    ) -> StoredRun:
        actual_id = str(uuid4()) if run_id is None else run_id
        if not actual_id:
            raise ValueError("run_id must be nonempty")
        payload = _encode(state)

        def action(connection: sqlite3.Connection) -> StoredRun:
            now = self.clock()
            try:
                connection.execute(
                    "INSERT INTO runs (run_id, owner_id, pack_id, status, state_json, "
                    "revision, created_at, updated_at) VALUES (?, ?, ?, 'queued', ?, 0, ?, ?)",
                    (actual_id, owner_id, pack_id, payload, now, now),
                )
            except sqlite3.IntegrityError as error:
                raise StoreConflict(f"run already exists: {actual_id}") from error
            return _run(self._owned(connection, actual_id, owner_id))

        return await asyncio.to_thread(self._write, action)

    async def get_run(self, run_id: str, *, owner_id: str) -> StoredRun:
        return await asyncio.to_thread(
            self._read, lambda db: _run(self._owned(db, run_id, owner_id))
        )

    async def claim(self, run_id: str, *, owner_id: str, lease_seconds: float = 60) -> StoredRun:
        if lease_seconds <= 0:
            raise ValueError("lease_seconds must be positive")

        def action(connection: sqlite3.Connection) -> StoredRun:
            row = self._owned(connection, run_id, owner_id)
            now = self.clock()
            if row["lease_until"] is not None and row["lease_until"] > now:
                raise StoreConflict(f"run has a live lease: {run_id}")
            connection.execute(
                "UPDATE runs SET lease_token=?, lease_until=?, updated_at=? WHERE run_id=?",
                (str(uuid4()), now + lease_seconds, now, run_id),
            )
            return _run(self._owned(connection, run_id, owner_id))

        return await asyncio.to_thread(self._write, action)

    async def renew(
        self, run_id: str, *, owner_id: str, lease_token: str, lease_seconds: float = 60
    ) -> None:
        if lease_seconds <= 0:
            raise ValueError("lease_seconds must be positive")

        def action(connection: sqlite3.Connection) -> None:
            self._leased(connection, run_id, owner_id, lease_token)
            now = self.clock()
            connection.execute(
                "UPDATE runs SET lease_until=?, updated_at=? WHERE run_id=?",
                (now + lease_seconds, now, run_id),
            )

        await asyncio.to_thread(self._write, action)

    async def release(self, run_id: str, *, owner_id: str, lease_token: str) -> None:
        def action(connection: sqlite3.Connection) -> None:
            self._leased(connection, run_id, owner_id, lease_token)
            connection.execute(
                "UPDATE runs SET lease_token=NULL, lease_until=NULL, updated_at=? WHERE run_id=?",
                (self.clock(), run_id),
            )

        await asyncio.to_thread(self._write, action)

    async def request_cancel(self, run_id: str, *, owner_id: str) -> StoredRun:
        def action(connection: sqlite3.Connection) -> StoredRun:
            row = self._owned(connection, run_id, owner_id)
            if not row["cancel_requested"]:
                now = self.clock()
                connection.execute(
                    "UPDATE runs SET cancel_requested=1, updated_at=? WHERE run_id=?",
                    (now, run_id),
                )
                connection.execute(
                    "INSERT INTO events (run_id, created_at, event_json) VALUES (?, ?, ?)",
                    (run_id, now, _encode({"kind": "cancel_requested"})),
                )
            return _run(self._owned(connection, run_id, owner_id))

        return await asyncio.to_thread(self._write, action)

    async def checkpoint(
        self,
        run_id: str,
        *,
        owner_id: str,
        lease_token: str,
        state: dict[str, JsonValue],
        status: str,
        next_wake_at: float | None = None,
        invocations: Sequence[dict[str, JsonValue]] = (),
        artifacts: Mapping[str, dict[str, JsonValue]] | None = None,
        events: Sequence[dict[str, JsonValue]] = (),
    ) -> StoredRun:
        state_json = _encode(state)

        def action(connection: sqlite3.Connection) -> StoredRun:
            self._leased(connection, run_id, owner_id, lease_token)
            now = self.clock()
            connection.execute(
                "UPDATE runs SET state_json=?, status=?, next_wake_at=?, revision=revision+1, "
                "updated_at=? WHERE run_id=?",
                (state_json, status, next_wake_at, now, run_id),
            )
            for invocation in invocations:
                invocation_id = invocation.get("invocation_id")
                if not isinstance(invocation_id, str) or not invocation_id:
                    raise ValueError("invocation_id must be a nonempty string")
                connection.execute(
                    "INSERT INTO invocations VALUES (?, ?, ?) "
                    "ON CONFLICT(run_id, invocation_id) "
                    "DO UPDATE SET payload_json=excluded.payload_json",
                    (run_id, invocation_id, _encode(invocation)),
                )
            for artifact_id, artifact in (artifacts or {}).items():
                payload = _encode(artifact)
                digest = hashlib.sha256(payload.encode("utf-8")).hexdigest()
                prior = connection.execute(
                    "SELECT sha256 FROM artifacts WHERE run_id=? AND artifact_id=?",
                    (run_id, artifact_id),
                ).fetchone()
                if prior is not None:
                    if prior["sha256"] != digest:
                        raise StoreConflict(f"artifact is immutable: {artifact_id}")
                    continue
                connection.execute(
                    "INSERT INTO artifacts VALUES (?, ?, ?, ?)",
                    (run_id, artifact_id, payload, digest),
                )
            for event in events:
                connection.execute(
                    "INSERT INTO events (run_id, created_at, event_json) VALUES (?, ?, ?)",
                    (run_id, now, _encode(event)),
                )
            return _run(self._owned(connection, run_id, owner_id))

        return await asyncio.to_thread(self._write, action)

    async def get_invocation(
        self, run_id: str, invocation_id: str, *, owner_id: str
    ) -> dict[str, JsonValue] | None:
        def action(connection: sqlite3.Connection) -> dict[str, JsonValue] | None:
            self._owned(connection, run_id, owner_id)
            row = connection.execute(
                "SELECT payload_json FROM invocations WHERE run_id=? AND invocation_id=?",
                (run_id, invocation_id),
            ).fetchone()
            return None if row is None else _decode_object(row["payload_json"])

        return await asyncio.to_thread(self._read, action)

    async def list_invocations(
        self, run_id: str, *, owner_id: str
    ) -> tuple[dict[str, JsonValue], ...]:
        def action(connection: sqlite3.Connection) -> tuple[dict[str, JsonValue], ...]:
            self._owned(connection, run_id, owner_id)
            rows = connection.execute(
                "SELECT payload_json FROM invocations WHERE run_id=? ORDER BY rowid", (run_id,)
            ).fetchall()
            return tuple(_decode_object(row["payload_json"]) for row in rows)

        return await asyncio.to_thread(self._read, action)

    async def get_artifact(
        self, run_id: str, artifact_id: str, *, owner_id: str
    ) -> dict[str, JsonValue]:
        def action(connection: sqlite3.Connection) -> dict[str, JsonValue]:
            self._owned(connection, run_id, owner_id)
            row = connection.execute(
                "SELECT payload_json FROM artifacts WHERE run_id=? AND artifact_id=?",
                (run_id, artifact_id),
            ).fetchone()
            if row is None:
                raise KeyError(artifact_id)
            return _decode_object(row["payload_json"])

        return await asyncio.to_thread(self._read, action)

    async def list_events(
        self, run_id: str, *, owner_id: str, after: int = 0, limit: int = 100
    ) -> tuple[dict[str, JsonValue], ...]:
        def action(connection: sqlite3.Connection) -> tuple[dict[str, JsonValue], ...]:
            self._owned(connection, run_id, owner_id)
            rows = connection.execute(
                "SELECT sequence, created_at, event_json FROM events "
                "WHERE run_id=? AND sequence>? ORDER BY sequence LIMIT ?",
                (run_id, after, limit),
            ).fetchall()
            return tuple(
                {
                    "sequence": row["sequence"],
                    "created_at": row["created_at"],
                    "event": _decode_object(row["event_json"]),
                }
                for row in rows
            )

        return await asyncio.to_thread(self._read, action)

    async def due_runs(self, *, limit: int = 100) -> tuple[StoredRun, ...]:
        def action(connection: sqlite3.Connection) -> tuple[StoredRun, ...]:
            now = self.clock()
            rows = connection.execute(
                "SELECT * FROM runs WHERE (lease_until IS NULL OR lease_until<=?) AND "
                "(status IN ('queued', 'running') OR "
                "(cancel_requested=1 AND status NOT IN ('completed', 'failed', 'cancelled')) OR "
                "(status='waiting' AND next_wake_at IS NOT NULL AND next_wake_at<=?)) "
                "ORDER BY created_at, run_id LIMIT ?",
                (now, now, limit),
            ).fetchall()
            return tuple(_run(row) for row in rows)

        return await asyncio.to_thread(self._read, action)

    async def list_runs(self, *, owner_id: str, limit: int = 100) -> tuple[StoredRun, ...]:
        def action(connection: sqlite3.Connection) -> tuple[StoredRun, ...]:
            rows = connection.execute(
                "SELECT * FROM runs WHERE owner_id=? ORDER BY created_at DESC, run_id LIMIT ?",
                (owner_id, limit),
            ).fetchall()
            return tuple(_run(row) for row in rows)

        return await asyncio.to_thread(self._read, action)
