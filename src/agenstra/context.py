"""Keep complete tool data outside the model context; expose explicit partial views."""

import json
from typing import cast

from pydantic import JsonValue

from agenstra.contracts import Fact, FactView


def fact_view(fact: Fact, *, max_characters: int = 6_000) -> FactView:
    omitted: list[tuple[str | int, ...]] = []

    def encoded_length(value: JsonValue) -> int:
        return len(json.dumps(value, ensure_ascii=False, separators=(",", ":")))

    def visit(value: JsonValue, path: tuple[str | int, ...], budget: int) -> JsonValue:
        if budget < 4:
            omitted.append(path)
            return {} if isinstance(value, dict) else [] if isinstance(value, list) else None
        if encoded_length(value) <= budget:
            return value
        if isinstance(value, dict):
            result: dict[str, JsonValue] = {}
            remaining = budget - 2
            for index, (key, item) in enumerate(value.items()):
                overhead = len(json.dumps(key, ensure_ascii=False)) + 2
                slots = len(value) - index
                if remaining < overhead + 4:
                    break
                preview = visit(item, (*path, key), max(4, (remaining - overhead) // slots))
                cost = overhead + encoded_length(preview)
                if cost > remaining:
                    break
                result[key] = preview
                remaining -= cost
            if len(result) != len(value):
                omitted.append(path)
            return result
        if isinstance(value, list):
            remaining = budget - 2
            sample: list[JsonValue] = []
            for index, item in enumerate(value[:32]):
                slots = min(len(value), 32) - index
                if remaining < 5:
                    break
                preview = visit(item, (*path, index), max(4, (remaining - 1) // slots))
                cost = 1 + encoded_length(preview)
                if cost > remaining:
                    break
                sample.append(preview)
                remaining -= cost
            if len(sample) != len(value):
                omitted.append(path)
            return sample
        omitted.append(path)
        # Do not replace a partial string with an apparently complete quotation.
        return None

    value = cast(dict[str, JsonValue], visit(fact.value, (), max_characters))
    return FactView(**(fact.model_dump() | {"value": value, "omitted_paths": tuple(omitted)}))
