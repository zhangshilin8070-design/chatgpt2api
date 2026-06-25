#!/usr/bin/env python3
"""一次性把当前 APP 版本元数据 PUT 到后端。

在服务器上执行：
  CHATGPT2API_ADMIN_PASSWORD=<password> python3 seed-app-version.py
"""
from __future__ import annotations

import json
import os
import sys
import urllib.request

BASE = "https://images.deepfly.bond"
USERNAME = "admin"

METADATA = {
    "versionCode": 6,
    "versionName": "3.0.3",
    "downloadUrl": "https://images.deepfly.bond/download/zheye-v3.0.3-debug.apk",
    "releaseNotes": (
        "本次更新：\n"
        "- 固定 debug 签名 keystore：本次升级需先卸载旧版再安装；从 3.0.3 起后续版本可无缝覆盖升级\n"
        "- 修正预览页元数据尺寸（解码 PNG 头部读真实像素，不再被 Coil 容器缩放尺寸误导)"
    ),
    "minSupportedVersionCode": 1,
}


def post_json(url: str, body: dict, headers: dict | None = None) -> dict:
    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(url, data=data, method="POST")
    req.add_header("Content-Type", "application/json")
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    with urllib.request.urlopen(req) as resp:  # noqa: S310 trusted host
        return json.loads(resp.read().decode("utf-8"))


def put_json(url: str, body: dict, headers: dict) -> dict:
    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(url, data=data, method="PUT")
    req.add_header("Content-Type", "application/json")
    for k, v in headers.items():
        req.add_header(k, v)
    with urllib.request.urlopen(req) as resp:  # noqa: S310 trusted host
        return json.loads(resp.read().decode("utf-8"))


def get(url: str, headers: dict | None = None) -> tuple[int, str]:
    req = urllib.request.Request(url, method="GET")
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    with urllib.request.urlopen(req) as resp:  # noqa: S310 trusted host
        return resp.status, resp.read().decode("utf-8")


def main() -> int:
    password = os.environ.get("CHATGPT2API_ADMIN_PASSWORD")
    if not password:
        print("CHATGPT2API_ADMIN_PASSWORD not set", file=sys.stderr)
        return 2

    login = post_json(
        f"{BASE}/auth/login",
        {"username": USERNAME, "password": password},
    )
    token = login.get("token")
    if not token:
        print("login response missing token", file=sys.stderr)
        return 3
    print(f"logged in: token_len={len(token)}")

    saved = put_json(
        f"{BASE}/api/admin/app-version",
        METADATA,
        {"Authorization": f"Bearer {token}"},
    )
    print("saved:", json.dumps(saved, ensure_ascii=False, indent=2))

    status, body = get(f"{BASE}/api/app/latest-version")
    print(f"GET latest-version: {status}")
    print(body)
    return 0


if __name__ == "__main__":
    sys.exit(main())
