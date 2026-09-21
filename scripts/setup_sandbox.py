#!/usr/bin/env python3
"""
Configure and verify sandbox settings for Antigravity CLI (agy) and Claude Code CLI.
"""

import argparse
import json
import os
import sys

def get_repo_dir():
    # bro-bot repo dir
    script_dir = os.path.dirname(os.path.abspath(__file__))
    return os.path.dirname(script_dir)

def configure_sandbox():
    repo_dir = get_repo_dir()
    user_home = os.path.expanduser("~")
    deploy_projects = os.path.join(user_home, "projects")

    # 1. Antigravity CLI (agy) settings
    agy_paths = [
        os.path.join(user_home, ".gemini", "antigravity-cli", "settings.json"),
        os.path.join(user_home, ".gemini", "config", "settings.json"),
    ]
    for path in agy_paths:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        cfg = {}
        if os.path.exists(path):
            try:
                with open(path, "r", encoding="utf-8") as f:
                    cfg = json.load(f)
            except Exception:
                cfg = {}

        cfg["enableTerminalSandbox"] = True
        workspaces = cfg.setdefault("trustedWorkspaces", [])
        for ws in [deploy_projects, repo_dir]:
            if ws not in workspaces:
                workspaces.append(ws)

        with open(path, "w", encoding="utf-8") as f:
            json.dump(cfg, f, indent=2, ensure_ascii=False)
            f.write("\n")

    # 2. Claude Code CLI settings
    claude_path = os.path.join(user_home, ".claude", "settings.json")
    os.makedirs(os.path.dirname(claude_path), exist_ok=True)
    claude_cfg = {}
    if os.path.exists(claude_path):
        try:
            with open(claude_path, "r", encoding="utf-8") as f:
                claude_cfg = json.load(f)
        except Exception:
            claude_cfg = {}

    sb = claude_cfg.setdefault("sandbox", {})
    sb["enabled"] = True
    sb["autoAllowBashIfSandboxed"] = True
    sb["allowUnsandboxedCommands"] = False
    sb["enableWeakerNestedSandbox"] = True

    fs = sb.setdefault("filesystem", {})
    allow_write = fs.setdefault("allowWrite", [])
    for path in [deploy_projects, repo_dir, "/tmp"]:
        if path not in allow_write:
            allow_write.append(path)

    net = sb.setdefault("network", {})
    net["allowAllUnixSockets"] = True
    domains = net.setdefault("allowedDomains", [])
    for d in [
        "github.com",
        "*.github.com",
        "api.anthropic.com",
        "*.anthropic.com",
        "generativelanguage.googleapis.com",
    ]:
        if d not in domains:
            domains.append(d)

    with open(claude_path, "w", encoding="utf-8") as f:
        json.dump(claude_cfg, f, indent=2, ensure_ascii=False)
        f.write("\n")

def verify_sandbox():
    user_home = os.path.expanduser("~")
    errors = []

    agy_paths = [
        os.path.join(user_home, ".gemini", "antigravity-cli", "settings.json"),
        os.path.join(user_home, ".gemini", "config", "settings.json"),
    ]
    checked_agy = False
    for path in agy_paths:
        if os.path.exists(path):
            checked_agy = True
            try:
                with open(path, "r", encoding="utf-8") as f:
                    cfg = json.load(f)
                if not cfg.get("enableTerminalSandbox"):
                    errors.append(f"{path}: enableTerminalSandbox is not true")
            except Exception as e:
                errors.append(f"{path}: failed to read: {e}")

    if not checked_agy:
        errors.append("No Antigravity CLI configuration file found")

    claude_path = os.path.join(user_home, ".claude", "settings.json")
    if os.path.exists(claude_path):
        try:
            with open(claude_path, "r", encoding="utf-8") as f:
                claude_cfg = json.load(f)
            sb = claude_cfg.get("sandbox", {})
            if not sb.get("enabled"):
                errors.append(f"{claude_path}: sandbox.enabled is not true")
        except Exception as e:
            errors.append(f"{claude_path}: failed to read: {e}")
    else:
        errors.append(f"{claude_path} does not exist")

    if errors:
        for err in errors:
            print(f"⚠️ {err}", file=sys.stderr)
        return False
    return True

def main():
    parser = argparse.ArgumentParser(description="Manage agent sandbox settings")
    parser.add_argument("--verify", action="store_true", help="Verify current sandbox configuration")
    args = parser.parse_args()

    if args.verify:
        if not verify_sandbox():
            sys.exit(1)
    else:
        configure_sandbox()

if __name__ == "__main__":
    main()
