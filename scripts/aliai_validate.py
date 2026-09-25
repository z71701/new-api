#!/usr/bin/env python3
"""ALIAI repository governance validator.

Validates:
  1. .github/aliai-governance.yml exists, is valid YAML, has required keys.
  2. docs/STATUS.md exists and contains required section headers.
  3. docs/handoffs/README.md exists.
  4. .github/PULL_REQUEST_TEMPLATE.md exists.
  5. Every docs/handoffs/*.yaml (excluding TEMPLATE) matches schema v1.0.

Usage:
  python scripts/aliai_validate.py <repo-root>
  python scripts/aliai_validate.py --self-test   # run built-in fixture tests
"""
from __future__ import annotations

import sys
from pathlib import Path

try:
    import yaml
except ImportError:
    print("FAIL: PyYAML is required (pip install pyyaml)")
    sys.exit(1)

HANDOFF_REQUIRED = [
    "schema_version", "producer_repo", "producer_commit", "artifact", "version",
    "change_type", "compatibility", "contract_refs", "tests",
    "runtime_switches", "downstream_actions", "blockers",
]
CHANGE_TYPES = {"feature", "bugfix", "contract_change", "deprecation", "release", "docs", "infra"}
COMPAT = {"none", "backward", "breaking", "conditional"}
TEST_STATUS = {"passed", "failed", "blocked", "not_run"}

STATUS_REQUIRED_SECTIONS = [
    "当前阶段", "本仓权威事实", "版本与提交",
    "开放项", "跨仓引用", "命名陷阱",
]
GOVERNANCE_REQUIRED = ["schema_version", "repo", "status", "handoffs", "validator", "cross_repo_rules"]


def fail(errors: list[str], msg: str) -> None:
    errors.append(msg)


def validate_governance(root: Path, errors: list[str]) -> dict:
    p = root / ".github" / "aliai-governance.yml"
    if not p.exists():
        fail(errors, f"missing {p.relative_to(root)}")
        return {}
    try:
        data = yaml.safe_load(p.read_text(encoding="utf-8"))
    except yaml.YAMLError as e:
        fail(errors, f"{p.name}: invalid YAML: {e}")
        return {}
    if not isinstance(data, dict):
        fail(errors, f"{p.name}: must be a mapping")
        return {}
    for k in GOVERNANCE_REQUIRED:
        if k not in data:
            fail(errors, f"{p.name}: missing key '{k}'")
    repo = data.get("repo", {})
    if not isinstance(repo, dict) or "role" not in repo:
        fail(errors, f"{p.name}: repo.role missing")
    elif repo["role"] not in {"backend", "client", "deploy"}:
        fail(errors, f"{p.name}: repo.role must be backend/client/deploy, got {repo['role']}")
    return data


def validate_status(root: Path, errors: list[str]) -> None:
    p = root / "docs" / "STATUS.md"
    if not p.exists():
        fail(errors, "missing docs/STATUS.md")
        return
    text = p.read_text(encoding="utf-8")
    for sec in STATUS_REQUIRED_SECTIONS:
        if sec not in text:
            fail(errors, f"docs/STATUS.md: missing required section '{sec}'")


def validate_handoff_file(p: Path, errors: list[str]) -> None:
    try:
        data = yaml.safe_load(p.read_text(encoding="utf-8"))
    except yaml.YAMLError as e:
        fail(errors, f"{p.name}: invalid YAML: {e}")
        return
    if not isinstance(data, dict):
        fail(errors, f"{p.name}: must be a mapping")
        return
    for k in HANDOFF_REQUIRED:
        if k not in data:
            fail(errors, f"{p.name}: missing required field '{k}'")
    if data.get("schema_version") != "1.0":
        fail(errors, f"{p.name}: schema_version must be '1.0'")
    if data.get("change_type") not in CHANGE_TYPES:
        fail(errors, f"{p.name}: change_type must be one of {sorted(CHANGE_TYPES)}")
    if data.get("compatibility") not in COMPAT:
        fail(errors, f"{p.name}: compatibility must be one of {sorted(COMPAT)}")
    pc = data.get("producer_commit", "")
    if not (isinstance(pc, str) and len(pc) == 40 and all(c in "0123456789abcdef" for c in pc)):
        fail(errors, f"{p.name}: producer_commit must be 40-char lowercase hex SHA")
    crefs = data.get("contract_refs")
    if not isinstance(crefs, list) or not crefs:
        fail(errors, f"{p.name}: contract_refs must be a non-empty list")
    else:
        for i, ref in enumerate(crefs):
            if not isinstance(ref, dict) or "url" not in ref:
                fail(errors, f"{p.name}: contract_refs[{i}] must have 'url'")
            else:
                url = str(ref.get("url", ""))
                if "github.com" in url:
                    import re as _re
                    m = _re.search(r"/blob/([^/]+)/", url)
                    if not m:
                        fail(errors, f"{p.name}: contract_refs[{i}].url must be a pinned /blob/<sha>/ URL")
                    else:
                        ref_part = m.group(1)
                        if not (len(ref_part) == 40 and all(c in "0123456789abcdef" for c in ref_part)):
                            fail(errors, f"{p.name}: contract_refs[{i}].url must pin a 40-char commit SHA, got '{ref_part}'")
    tests = data.get("tests")
    if not isinstance(tests, list):
        fail(errors, f"{p.name}: tests must be a list")
    else:
        for i, t in enumerate(tests):
            if isinstance(t, dict) and t.get("status") not in TEST_STATUS:
                fail(errors, f"{p.name}: tests[{i}].status must be one of {sorted(TEST_STATUS)}")
    if not isinstance(data.get("blockers"), list):
        fail(errors, f"{p.name}: blockers must be a list")


def validate_handoffs(root: Path, errors: list[str]) -> None:
    hdir = root / "docs" / "handoffs"
    if not hdir.is_dir():
        fail(errors, "missing docs/handoffs/ directory")
        return
    if not (hdir / "README.md").exists():
        fail(errors, "missing docs/handoffs/README.md")
    if not (hdir / "TEMPLATE.yaml").exists():
        fail(errors, "missing docs/handoffs/TEMPLATE.yaml")
    for p in sorted(hdir.glob("*.yaml")):
        if p.name == "TEMPLATE.yaml":
            continue
        validate_handoff_file(p, errors)


def validate_pr_template(root: Path, errors: list[str]) -> None:
    p = root / ".github" / "PULL_REQUEST_TEMPLATE.md"
    if not p.exists():
        fail(errors, "missing .github/PULL_REQUEST_TEMPLATE.md")
        return
    text = p.read_text(encoding="utf-8")
    for needle in ["aliai-cross-repo-coordination", "STATUS.md", "handoff"]:
        if needle not in text:
            fail(errors, f"PULL_REQUEST_TEMPLATE.md: must mention '{needle}'")


def validate_repo(root: Path) -> tuple[bool, list[str]]:
    errors: list[str] = []
    validate_governance(root, errors)
    validate_status(root, errors)
    validate_handoffs(root, errors)
    validate_pr_template(root, errors)
    return (len(errors) == 0, errors)


def self_test() -> int:
    """Run validator against built-in valid/invalid fixtures."""
    import tempfile, shutil
    tmp = Path(tempfile.mkdtemp(prefix="aliai-validate-"))
    try:
        # Build a minimal valid repo
        (tmp / ".github").mkdir()
        (tmp / "docs" / "handoffs").mkdir(parents=True)
        (tmp / "scripts").mkdir()
        gov = {
            "schema_version": "1.0",
            "repo": {"name": "o/r", "role": "backend", "authoritative_fields": ["api"]},
            "status": {"doc": "docs/STATUS.md", "last_verified_commit": "a" * 40},
            "handoffs": {"dir": "docs/handoffs", "readme": "docs/handoffs/README.md",
                         "schema_version": "1.0", "required_fields": HANDOFF_REQUIRED},
            "pr_template": ".github/PULL_REQUEST_TEMPLATE.md",
            "validator": {"script": "scripts/aliai_validate.py", "ci_workflow": ".github/workflows/x.yml"},
            "upstream_producers": [], "downstream_consumers": [],
            "cross_repo_rules": {"declare_other_repo_dynamic_state": False,
                                 "references_must_be_url_plus_commit": True,
                                 "independent_pr_per_repo": True, "preserve_source_sha": True},
            "naming_traps": [{"note": "x"}],
        }
        (tmp / ".github" / "aliai-governance.yml").write_text(yaml.safe_dump(gov), encoding="utf-8")
        (tmp / "docs" / "STATUS.md").write_text(
            "# s\n\n## 当前阶段\nx\n## 本仓权威事实\nx\n## 版本与提交\nx\n"
            "## 开放项\nx\n## 跨仓引用\nx\n## 命名陷阱\nx\n", encoding="utf-8")
        (tmp / "docs" / "handoffs" / "README.md").write_text("# handoffs\n", encoding="utf-8")
        (tmp / "docs" / "handoffs" / "TEMPLATE.yaml").write_text("schema_version: \"1.0\"\n", encoding="utf-8")
        (tmp / ".github" / "PULL_REQUEST_TEMPLATE.md").write_text(
            "aliai-cross-repo-coordination STATUS.md handoff\n", encoding="utf-8")
        valid_handoff = {
            "schema_version": "1.0", "producer_repo": "o/r",
            "producer_commit": "b" * 40, "artifact": "a", "version": "v1",
            "change_type": "contract_change", "compatibility": "backward",
            "summary": "s", "created_at": "2026-09-25",
            "contract_refs": [{"url": "https://github.com/o/r/blob/" + "c" * 40 + "/x.md", "kind": "api"}],
            "tests": [{"name": "t", "status": "passed", "evidence_url": ""}],
            "runtime_switches": {"turnstile_check": False},
            "downstream_actions": [{"repo": "o/c", "action": "x", "required": True}],
            "blockers": [],
        }
        (tmp / "docs" / "handoffs" / "2026-09-25-valid.yaml").write_text(
            yaml.safe_dump(valid_handoff), encoding="utf-8")
        ok, errs = validate_repo(tmp)
        print(f"[self-test] valid repo: {'PASS' if ok else 'FAIL'}")
        for e in errs:
            print(f"  unexpected error: {e}")

        # Invalid handoff: missing fields, bad SHA, floating URL
        invalid_handoff = {"schema_version": "1.0", "producer_repo": "o/r",
                           "producer_commit": "short", "artifact": "a", "version": "v1",
                           "change_type": "bogus", "compatibility": "backward",
                           "contract_refs": [{"url": "https://github.com/o/r/blob/main/x.md"}],
                           "tests": [], "runtime_switches": {}, "downstream_actions": [], "blockers": []}
        (tmp / "docs" / "handoffs" / "2026-09-25-invalid.yaml").write_text(
            yaml.safe_dump(invalid_handoff), encoding="utf-8")
        ok2, errs2 = validate_repo(tmp)
        print(f"[self-test] invalid handoff detected: {'PASS' if not ok2 else 'FAIL'} "
              f"({len(errs2)} errors)")
        expected_substrings = ["producer_commit", "change_type", "/blob/<sha>"]
        for sub in expected_substrings:
            if not any(sub in e for e in errs2):
                print(f"  missing expected error about '{sub}'")
        return 0 if (ok and not ok2) else 1
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def main() -> int:
    if len(sys.argv) >= 2 and sys.argv[1] == "--self-test":
        return self_test()
    if len(sys.argv) != 2:
        print("Usage: python aliai_validate.py <repo-root> | --self-test")
        return 2
    root = Path(sys.argv[1]).resolve()
    ok, errors = validate_repo(root)
    if ok:
        print(f"PASS: governance validation for {root}")
        return 0
    print(f"FAIL: {len(errors)} issue(s) in {root}")
    for e in errors:
        print(f"  - {e}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
