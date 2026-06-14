"""端到端 smoke：登录 -> /api/openai-accounts CRUD -> /api/profile 双桶 ->
/api/creation-tasks/image-generations(model=gpt-image-2) 任务 submit。

CHATGPT 通路在 chatgpt2api 进程外没有真实账号池，因此 image task 仅断言
入队成功 + bucket=bucket_a + resolved_model=gpt-image-2，不要求真正出图。
"""
from __future__ import annotations

import json
import os
import sys
import time
from urllib.parse import urlencode
from urllib.request import Request, urlopen
from urllib.error import HTTPError, URLError


BASE = os.environ.get("SMOKE_BASE", "http://127.0.0.1:13000")
ADMIN_USER = os.environ.get("SMOKE_ADMIN_USER", "admin")
ADMIN_PASS = os.environ.get("SMOKE_ADMIN_PASS", "smoke_password_123")


def http(method: str, path: str, body: dict | None = None, token: str | None = None,
         expected: int = 200) -> dict:
    url = path if path.startswith("http") else BASE + path
    headers = {"Content-Type": "application/json", "Accept": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    data = None
    if body is not None:
        data = json.dumps(body).encode("utf-8")
    req = Request(url, data=data, method=method, headers=headers)
    try:
        with urlopen(req, timeout=20) as resp:
            payload = resp.read().decode("utf-8")
            status = resp.status
    except HTTPError as e:
        payload = e.read().decode("utf-8", errors="replace")
        status = e.code
    if status != expected:
        raise RuntimeError(f"{method} {path} -> {status} (expect {expected}): {payload}")
    return json.loads(payload) if payload.strip() else {}


def assert_eq(label: str, got, want):
    if got != want:
        raise AssertionError(f"[{label}] got={got!r} want={want!r}")


def main() -> None:
    # --- wait for server
    for _ in range(60):
        try:
            urlopen(BASE + "/api/version", timeout=2)
            break
        except (HTTPError, URLError):
            time.sleep(0.25)
    else:
        # /api/version 不存在也没关系；只要端口开了即可
        pass

    print("[1] login as admin")
    login = http("POST", "/auth/login",
                 {"username": ADMIN_USER, "password": ADMIN_PASS})
    token = login["token"]
    assert_eq("login.role", login.get("role"), "admin")
    print(f"   role={login['role']} subject={login['subject_id']}")

    print("[2] /api/profile billing dual bucket on admin")
    profile = http("GET", "/api/profile", token=token)
    billing = profile.get("billing") or {}
    if "bucket_a" not in billing or "bucket_b" not in billing:
        raise AssertionError(f"profile.billing missing dual bucket: {billing}")
    print(f"   billing={billing}")

    print("[3] /api/openai-accounts CRUD")
    listed = http("GET", "/api/openai-accounts", token=token)
    assert_eq("initial_items_empty", listed["items"], [])

    bad = http("POST", "/api/openai-accounts",
               {"name": "missing-key", "base_url": "https://api.example.com",
                "allowed_models": ["gpt-image-2"]},
               token=token, expected=400)
    if "api_key is required" not in json.dumps(bad):
        raise AssertionError(f"missing api_key validation failed: {bad}")

    bad_url = http("POST", "/api/openai-accounts",
                   {"name": "bad-url", "api_key": "sk-test-1234",
                    "base_url": "ftp://nope", "allowed_models": ["gpt-image-2"]},
                   token=token, expected=400)
    if "base_url" not in json.dumps(bad_url):
        raise AssertionError(f"bad base_url validation failed: {bad_url}")

    create = http("POST", "/api/openai-accounts",
                  {"name": "primary", "api_key": "sk-test-secret-XYZ9",
                   "base_url": "https://api.example.com",
                   "allowed_models": ["gpt-image-2", "gemini-3.1-flash-image"],
                   "priority": 0, "concurrency": 1},
                  token=token)
    item = create["item"]
    account_id = item["id"]
    assert_eq("api_key_redacted", item["api_key"], "sk-***XYZ9")
    if "gpt-image-2" not in item["model_states"]:
        raise AssertionError(f"model_states missing gpt-image-2: {item}")
    print(f"   created id={account_id} api_key={item['api_key']}")

    patched = http("PATCH", f"/api/openai-accounts/{account_id}",
                   {"priority": 5, "allowed_models": ["gpt-image-2"]},
                   token=token)
    if "gemini-3.1-flash-image" in patched["item"]["model_states"]:
        raise AssertionError("model_states should drop gemini after allowed_models update")

    state_patch = http("PATCH",
                       f"/api/openai-accounts/{account_id}/model-states/gpt-image-2",
                       {"status": "禁用", "error_message": "smoke test"},
                       token=token)
    if state_patch["item"]["model_states"]["gpt-image-2"]["status"] != "禁用":
        raise AssertionError(f"model_states patch failed: {state_patch}")

    deleted = http("DELETE", f"/api/openai-accounts/{account_id}", token=token)
    if deleted["items"] != []:
        raise AssertionError(f"delete should leave list empty: {deleted}")

    print("[4] create normal user + verify dual-bucket profile billing")
    user_id_resp = http("POST", "/api/admin/users",
                        {"username": "smoke_user", "name": "Smoke User",
                         "password": "Password1234", "enabled": True},
                        token=token)
    user_id = user_id_resp["item"]["id"]
    print(f"   created user_id={user_id}")

    # 给 bucket_a 充值，确认能成功扣费提交一个 task
    http("POST", f"/api/admin/users/{user_id}/billing-adjustments",
         {"type": "increase_balance", "bucket": "bucket_a",
          "amount": 5, "reason": "smoke test bucket_a"},
         token=token)

    # bucket 必填校验
    no_bucket = http("POST", f"/api/admin/users/{user_id}/billing-adjustments",
                     {"type": "increase_balance", "amount": 1},
                     token=token, expected=400)
    if "bucket" not in json.dumps(no_bucket):
        raise AssertionError(f"missing bucket should 400 with bucket message: {no_bucket}")

    # 取个 user 的 API key，用于以 user 身份 submit creation-task
    user_key_resp = http("POST", f"/api/admin/users/{user_id}/reset-key",
                         {"name": "smoke"}, token=token)
    user_key = user_key_resp["key"]

    user_billing = http("GET", "/api/profile", token=user_key).get("billing") or {}
    if user_billing.get("bucket_a", {}).get("available") != 5:
        raise AssertionError(f"user bucket_a should be 5 after adjust: {user_billing}")
    if user_billing.get("bucket_b", {}).get("available") != 0:
        raise AssertionError(f"user bucket_b should be 0: {user_billing}")
    print(f"   user billing={user_billing}")

    print("[5] /api/creation-tasks/image-generations rejects illegal model")
    bad_model = http("POST", "/api/creation-tasks/image-generations",
                     {"client_task_id": "smoke-bad-model", "prompt": "ignored",
                      "model": "dall-e-3"},
                     token=user_key, expected=400)
    if "is not a billable image model" not in json.dumps(bad_model):
        raise AssertionError(f"bad model should be rejected: {bad_model}")

    print("[6] /api/creation-tasks/image-generations accepts gpt-image-2 + writes bucket")
    task = http("POST", "/api/creation-tasks/image-generations",
                {"client_task_id": "smoke-task-1", "prompt": "a smoke test",
                 "model": "gpt-image-2"},
                token=user_key)
    assert_eq("task.bucket", task.get("bucket"), "bucket_a")
    assert_eq("task.resolved_model", task.get("resolved_model"), "gpt-image-2")
    if task.get("upstream_kind"):
        raise AssertionError(f"upstream_kind should be empty before exec: {task}")
    print(f"   task={{{','.join(f'{k}={v}' for k,v in task.items() if k in ('id','status','bucket','resolved_model'))}}}")

    print("[7] cleanup smoke user")
    http("DELETE", f"/api/admin/users/{user_id}", token=token)

    print("\nAll smoke checks passed.")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(f"\nSMOKE FAILED: {e}", file=sys.stderr)
        sys.exit(1)
