"""OpenAI-compatible JSON decision adapter; the runtime does not depend on a model vendor."""

import httpx
from pydantic import ValidationError

from agenstra.contracts import DECISION_ADAPTER, ContextPacket, Decision


class ModelDecisionError(RuntimeError):
    def __init__(self, code: str) -> None:
        super().__init__(code)
        self.code = code


class HttpJsonDecisionModel:
    def __init__(
        self,
        *,
        model: str,
        base_url: str,
        api_key: str,
        timeout_seconds: float = 30,
        client: httpx.AsyncClient | None = None,
    ) -> None:
        if not model or not base_url or not api_key:
            raise ValueError("model, base_url, and api_key are required")
        self._model = model
        self._url = base_url.rstrip("/") + "/chat/completions"
        self._api_key = api_key
        self._timeout_seconds = timeout_seconds
        self._client = client or httpx.AsyncClient(follow_redirects=False)
        self._owns_client = client is None

    async def decide(self, *, context: ContextPacket, system_prompt: str) -> Decision:
        try:
            response = await self._client.post(
                self._url,
                headers={"Authorization": f"Bearer {self._api_key}"},
                json={
                    "model": self._model,
                    "response_format": {"type": "json_object"},
                    "messages": [
                        {"role": "system", "content": system_prompt},
                        {
                            "role": "user",
                            "content": context.model_dump_json(by_alias=True),
                        },
                    ],
                },
                timeout=self._timeout_seconds,
                follow_redirects=False,
            )
        except httpx.RequestError as exc:
            raise ModelDecisionError("model_unavailable") from exc
        if response.status_code >= 400:
            raise ModelDecisionError("model_http_error")
        try:
            envelope = response.json()
            content = envelope["choices"][0]["message"]["content"]
            if not isinstance(content, str):
                raise ValueError("model content must be a JSON string")
            # Do not repair malformed decisions: the model must cross this typed boundary.
            return DECISION_ADAPTER.validate_json(content)
        except (KeyError, IndexError, TypeError, ValueError, ValidationError) as exc:
            raise ModelDecisionError("model_decision_invalid") from exc

    async def aclose(self) -> None:
        if self._owns_client:
            await self._client.aclose()
