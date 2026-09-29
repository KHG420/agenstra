import json

import pytest

from enterprise_agent.openapi import import_openapi
from enterprise_agent.rest import RestManifest


def test_bearer_import_requires_explicit_environment_and_preserves_existing_name(tmp_path):
    source = {
        "openapi": "3.0.3",
        "info": {"title": "Records", "version": "1"},
        "components": {"securitySchemes": {"RecordsAuth": {"type": "http", "scheme": "bearer"}}},
        "security": [{"RecordsAuth": []}],
        "paths": {
            "/records": {
                "get": {
                    "operationId": "getRecords",
                    "responses": {
                        "200": {"content": {"application/json": {"schema": {"type": "object"}}}}
                    },
                }
            }
        },
    }
    spec = tmp_path / "openapi.json"
    spec.write_text(json.dumps(source))
    kwargs = dict(name="records", base_url_env="RECORDS_URL", operations=["getRecords"])
    with pytest.raises(ValueError, match="explicit --token-env"):
        import_openapi(spec, **kwargs)
    result = import_openapi(spec, token_env="RECORDS_TOKEN", **kwargs)
    assert result["token_env"] == "RECORDS_TOKEN"
    RestManifest.model_validate(result)
    source["components"]["securitySchemes"]["RecordsAuth"] = {
        "type": "oauth2",
        "flows": {},
    }
    spec.write_text(json.dumps(source))
    with pytest.raises(ValueError, match="unsupported security scheme"):
        import_openapi(spec, token_env="RECORDS_TOKEN", **kwargs)
