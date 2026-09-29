"""Keep complete tool data outside the model context; expose explicit partial views."""

import json
from typing import cast

from pydantic import JsonValue

from agenstra.contracts import Fact, FactView


def fact_view(fact: Fact, *, max_characters: int = 6_000) -> FactView:
    omitted: list[tuple[str | int, ...]] = []
    remaining = max_characters

    def visit(value: JsonValue, path: tuple[str | int, ...]) -> JsonValue:
        nonlocal remaining
        if remaining <= 0:
            omitted.append(path)
            return {} if isinstance(value, dict) else [] if isinstance(value, list) else None
        serialized = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
        if len(serialized) <= remaining:
            remaining -= len(serialized)
            return value
        if isinstance(value, dict):
            result: dict[str, JsonValue] = {}
            for key, item in value.items():
                remaining -= len(json.dumps(key, ensure_ascii=False)) + 3
                if remaining <= 0:
                    omitted.append(path)
                    break
                result[key] = visit(item, (*path, key))
            return result
        if isinstance(value, list):
            remaining -= 2
            sample = []
            for index, item in enumerate(value[:3]):
                if remaining <= 0:
                    break
                remaining -= 1
                sample.append(visit(item, (*path, index)))
            if len(sample) != len(value):
                omitted.append(path)
            return sample
        omitted.append(path)
        # Do not replace a partial string with an apparently complete quotation.
        return None

    value = cast(dict[str, JsonValue], visit(fact.value, ()))
    return FactView(**(fact.model_dump() | {"value": value, "omitted_paths": tuple(omitted)}))
