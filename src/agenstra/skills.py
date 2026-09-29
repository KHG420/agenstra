"""Trusted, content-pinned skill files shared by transport adapters."""

import hashlib
from collections.abc import Iterable
from pathlib import Path

from pydantic import Field

from agenstra.providers import Skill, SkillDescription


class SkillFile(SkillDescription):
    path: str
    sha256: str = Field(pattern=r"^[a-f0-9]{64}$")


def load_skill_files(entries: Iterable[SkillFile], directory: Path) -> dict[str, Skill]:
    root = directory.resolve()
    result: dict[str, Skill] = {}
    for item in entries:
        path = (root / item.path).resolve()
        if not path.is_relative_to(root) or item.name in result or not path.is_file():
            raise ValueError("invalid skill path or duplicate name")
        content = path.read_bytes()
        if hashlib.sha256(content).hexdigest() != item.sha256:
            raise ValueError(f"skill content changed: {item.name}")
        result[item.name] = Skill(
            description=SkillDescription(name=item.name, description=item.description),
            content=content.decode("utf-8"),
        )
    return result
