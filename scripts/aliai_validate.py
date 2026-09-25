#!/usr/bin/env python3
"""ALIAI repository governance validator v1.1.

Validates (v1.0 preserved + v1.1 status lifecycle):
  v1.0:
    1. .github/aliai-governance.yml exists, valid YAML, required keys, repo.role in {backend,client,deploy}.
    2. docs/STATUS.md exists and contains required section headers.
    3. docs/handoffs/README.md + TEMPLATE.yaml exist.
    4. .github/PULL_REQUEST_TEMPLATE.md exists and mentions STATUS.md / handoff / skill.
    5. Every docs/handoffs/*.yaml (except TEMPLATE) matches schema (v1.0 fields preserved).
  v1.1:
    6. governance.yml schema_version=="1.1" and has a status_lifecycle block.
    7. docs/status.yml exists, valid YAML, schema_version=="1.1", repo.role matches,
       current_state in the role's allowed state list.
    8. docs/STATUS.md carries a <!-- status-sync: ... --> marker whose
       state/source_commit/updated_at match docs/status.yml.
    9. With --base/--head: git diff base...head; if trigger_paths are touched,
       docs/status.yml AND docs/STATUS.md must both be in the diff.
   10. Lifecycle transition previous_state->current_state is legal.
   11. candidate state: source_commit must not be a merge commit; merge-only fields
       (backend.image_digest, deploy.actual_digest, ...) must be absent.
   12. implemented state: source_commit must be a 40-hex SHA.
   13. Role evidence gates: released->image_tag+image_digest(sha256:);
       accepted->test_level=real_e2e+evidence; deployed->four-piece non-empty.
   14. handoffs/*.yaml: lifecycle_state in enum, evidence_phase in enum;
       producer_commit exists (online check, offline WARNING); feature_candidate PRs
       must not carry a final (>=implemented) handoff.
   15. PR template contains "PR Kind" / "feature candidate" / "post-merge" / "Lifecycle Target".

Usage:
  python scripts/aliai_validate.py <repo-root> [--base REF] [--head REF] [--pr-kind KIND]
                                              [--offline]
  python scripts/aliai_validate.py --self-test
"""
from __future__ import annotations

import argparse
import fnmatch
import re
import subprocess
import sys
from pathlib import Path

try:
    import yaml
except ImportError:
    print("FAIL: PyYAML is required (pip install pyyaml)")
    sys.exit(1)

# ---------------------------------------------------------------------------
# Enums
# ---------------------------------------------------------------------------
STATES_ALL = [
    "planned", "candidate", "implemented", "verified", "accepted",
    "released", "deployed", "blocked", "failed", "rolled_back", "superseded",
]
ROLE_STATES = {
    "backend":  ["planned", "candidate", "implemented", "verified", "released",
                 "blocked", "failed", "superseded"],
    "client":   ["planned", "candidate", "implemented", "verified", "accepted",
                 "blocked", "failed", "superseded"],
    "deploy":   ["planned", "candidate", "implemented", "deployed",
                 "blocked", "failed", "rolled_back", "superseded"],
}
EVIDENCE_PHASES = ["pre_merge", "post_merge", "post_build", "post_deploy", "post_acceptance"]
TEST_LEVELS = ["mock_unit", "typecheck", "lint", "build", "ci", "real_e2e"]
TEST_STATUS = ["passed", "failed", "blocked", "not_run"]
PR_KINDS = ["feature_candidate", "post_merge", "post_build_acceptance",
            "deploy_candidate", "post_deploy"]

HANDOFF_REQUIRED = [
    "schema_version", "producer_repo", "producer_commit", "artifact", "version",
    "change_type", "compatibility", "contract_refs", "tests",
    "runtime_switches", "downstream_actions", "blockers",
    "lifecycle_state", "evidence_phase",
]
CHANGE_TYPES = {"feature", "bugfix", "contract_change", "deprecation", "release", "docs", "infra"}
COMPAT = {"none", "backward", "breaking", "conditional"}
STATUS_REQUIRED_SECTIONS = ["当前阶段", "本仓权威事实", "版本与提交",
                            "开放项", "跨仓引用", "命名陷阱"]
GOVERNANCE_REQUIRED = ["schema_version", "repo", "status", "handoffs", "validator",
                       "cross_repo_rules", "status_lifecycle"]
MERGE_ONLY_FIELDS = ["backend.image_digest", "deploy.actual_digest"]
SHA_RE = re.compile(r"^[0-9a-f]{40}$")
DIGEST_RE = re.compile(r"^sha256:[a-f0-9]{64}$")
MARKER_RE = re.compile(
    r"<!--\s*status-sync:\s*state=(\S+)\s+source_commit=([0-9a-f]{40})\s+updated_at=(\S+?)\s*-->")


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
def fail(errors: list[str], msg: str) -> None:
    errors.append(msg)


def warn(warnings: list[str], msg: str) -> None:
    warnings.append(msg)


def git(root: Path, *args: str) -> subprocess.CompletedProcess:
    return subprocess.run(["git", "-C", str(root), *args],
                          capture_output=True, text=True)


def is_40hex(s: str) -> bool:
    return isinstance(s, str) and bool(SHA_RE.match(s))


def commit_object_exists(root: Path, sha: str) -> bool:
    r = git(root, "cat-file", "-t", sha)
    return r.returncode == 0 and "commit" in r.stdout


def is_merge_commit(root: Path, sha: str) -> bool:
    r = git(root, "rev-list", "--parents", "-n1", sha)
    if r.returncode != 0:
        return False
    parts = r.stdout.split()
    return len(parts) >= 3  # <sha> <parent1> <parent2>...


def get_nested(data: dict, dotted: str):
    cur = data
    for part in dotted.split("."):
        if not isinstance(cur, dict) or part not in cur:
            return None
        cur = cur[part]
    return cur


# ---------------------------------------------------------------------------
# Governance manifest
# ---------------------------------------------------------------------------
def load_governance(root: Path, errors: list[str]) -> dict:
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
    elif repo["role"] not in ROLE_STATES:
        fail(errors, f"{p.name}: repo.role must be backend/client/deploy, got {repo['role']}")
    # v1.1: schema_version + status_lifecycle block
    if data.get("schema_version") != "1.1":
        fail(errors, f"{p.name}: schema_version must be \"1.1\", got {data.get('schema_version')!r}")
    sl = data.get("status_lifecycle")
    if not isinstance(sl, dict):
        fail(errors, f"{p.name}: status_lifecycle block missing or not a mapping")
    else:
        for k in ["machine_file", "human_file", "states", "transitions",
                  "trigger_paths", "role_requirements"]:
            if k not in sl:
                fail(errors, f"{p.name}: status_lifecycle.{k} missing")
    return data


# ---------------------------------------------------------------------------
# docs/status.yml (machine authority)
# ---------------------------------------------------------------------------
def transition_legal(prev, curr, role, gov_transitions) -> bool:
    if prev is None:
        return False
    explicit = set()
    for t in gov_transitions or []:
        if isinstance(t, str) and "→" in t:
            a, b = t.split("→", 1)
            explicit.add((a.strip(), b.strip()))
    if (prev, curr) in explicit:
        return True
    # wildcards
    if curr in ("blocked", "failed", "superseded"):
        return True
    if prev in ("blocked", "failed"):  # return to previous state
        return True
    if role == "deploy" and prev == "deployed" and curr == "rolled_back":
        return True
    return False


def load_status_yml(root: Path, errors: list[str]) -> dict:
    p = root / "docs" / "status.yml"
    if not p.exists():
        fail(errors, "missing docs/status.yml")
        return {}
    try:
        data = yaml.safe_load(p.read_text(encoding="utf-8"))
    except yaml.YAMLError as e:
        fail(errors, f"docs/status.yml: invalid YAML: {e}")
        return {}
    if not isinstance(data, dict):
        fail(errors, "docs/status.yml: must be a mapping")
        return {}
    if data.get("schema_version") != "1.1":
        fail(errors, f"docs/status.yml: schema_version must be \"1.1\", got {data.get('schema_version')!r}")
    return data


def validate_status_yml(root: Path, gov: dict, errors: list[str], warnings: list[str],
                        offline: bool) -> dict:
    data = load_status_yml(root, errors)
    if not data:
        return {}
    role = (gov.get("repo") or {}).get("role")
    repo = data.get("repo") or {}
    if repo.get("role") != role:
        fail(errors, f"docs/status.yml: repo.role={repo.get('role')!r} != governance role={role!r}")
    life = data.get("lifecycle") or {}
    cur = life.get("current_state")
    allowed = ROLE_STATES.get(role, [])
    if cur not in allowed:
        fail(errors, f"docs/status.yml: current_state={cur!r} not allowed for role={role} "
                     f"(allowed: {allowed})")
    prev = life.get("previous_state")
    sl = gov.get("status_lifecycle") or {}
    if not transition_legal(prev, cur, role, sl.get("transitions")):
        fail(errors, f"docs/status.yml: illegal lifecycle transition {prev!r} -> {cur!r}")
    src = life.get("source_commit", "")
    # implemented+ requires a 40-hex source_commit
    if cur in ("implemented", "verified", "accepted", "released", "deployed"):
        if not is_40hex(src):
            fail(errors, f"docs/status.yml: source_commit must be 40-hex SHA in state={cur}, got {src!r}")
    # candidate: source_commit must not be a merge commit, and merge-only fields absent
    if cur == "candidate":
        if is_40hex(src) and not offline:
            try:
                if is_merge_commit(root, src):
                    fail(errors, f"docs/status.yml: candidate source_commit {src[:12]} is a merge commit")
            except Exception:
                pass
        for mf in MERGE_ONLY_FIELDS:
            v = get_nested(data, mf)
            if v not in (None, ""):
                fail(errors, f"docs/status.yml: candidate state must not set {mf}")
    # tests enum
    tests = data.get("tests")
    if not isinstance(tests, list):
        fail(errors, "docs/status.yml: tests must be a list")
    else:
        for i, t in enumerate(tests):
            if isinstance(t, dict):
                if t.get("level") not in TEST_LEVELS:
                    fail(errors, f"docs/status.yml: tests[{i}].level must be one of {TEST_LEVELS}")
                if t.get("status") not in TEST_STATUS:
                    fail(errors, f"docs/status.yml: tests[{i}].status must be one of {TEST_STATUS}")
    if not isinstance(data.get("blockers"), list):
        fail(errors, "docs/status.yml: blockers must be a list")
    # role evidence gates
    rr = sl.get("role_requirements") or {}
    req = rr.get(cur) or {}
    for f in req.get("requires_fields", []) or []:
        v = get_nested(data, f)
        if v in (None, ""):
            fail(errors, f"docs/status.yml: state={cur} requires non-empty field {f}")
    digest_fmt = req.get("image_digest_format")
    if digest_fmt:
        d = get_nested(data, "backend.image_digest") or ""
        if d and not re.match(digest_fmt, d):
            fail(errors, f"docs/status.yml: backend.image_digest does not match {digest_fmt}: {d!r}")
    if cur == "accepted":
        if get_nested(data, "client.test_level") != req.get("test_level_value", "real_e2e"):
            fail(errors, "docs/status.yml: accepted requires client.test_level=real_e2e")
        if not life.get("evidence"):
            fail(errors, "docs/status.yml: accepted requires lifecycle.evidence")
    return data


# ---------------------------------------------------------------------------
# STATUS.md marker
# ---------------------------------------------------------------------------
def validate_status_md(root: Path, status_yml: dict, errors: list[str]) -> None:
    p = root / "docs" / "STATUS.md"
    if not p.exists():
        fail(errors, "missing docs/STATUS.md")
        return
    text = p.read_text(encoding="utf-8")
    for sec in STATUS_REQUIRED_SECTIONS:
        if sec not in text:
            fail(errors, f"docs/STATUS.md: missing required section '{sec}'")
    m = MARKER_RE.search(text)
    if not m:
        fail(errors, "docs/STATUS.md: missing <!-- status-sync: state=... source_commit=... updated_at=... --> marker")
        return
    m_state, m_sha, m_updated = m.group(1), m.group(2), m.group(3)
    life = (status_yml or {}).get("lifecycle") or {}
    if status_yml:
        if m_state != life.get("current_state"):
            fail(errors, f"docs/STATUS.md marker state={m_state!r} != status.yml current_state={life.get('current_state')!r}")
        if m_sha != life.get("source_commit"):
            fail(errors, f"docs/STATUS.md marker source_commit mismatch")
        if m_updated != life.get("updated_at"):
            fail(errors, f"docs/STATUS.md marker updated_at={m_updated!r} != status.yml updated_at={life.get('updated_at')!r}")


# ---------------------------------------------------------------------------
# Trigger-path diff check
# ---------------------------------------------------------------------------
def changed_files_from_diff(root: Path, base: str, head: str) -> list[str]:
    r = git(root, "diff", "--name-only", f"{base}...{head}")
    if r.returncode != 0:
        # fall back to two-dot
        r = git(root, "diff", "--name-only", base, head)
    if r.returncode != 0:
        return []
    return [ln for ln in r.stdout.splitlines() if ln.strip()]


def check_trigger_paths(changed: list[str], gov: dict, errors: list[str]) -> None:
    sl = gov.get("status_lifecycle") or {}
    patterns = sl.get("trigger_paths") or []
    touched = [f for f in changed if any(fnmatch.fnmatch(f, pat) for pat in patterns)]
    if not touched:
        return
    has_status = "docs/status.yml" in changed
    has_status_md = "docs/STATUS.md" in changed
    if not has_status or not has_status_md:
        missing = []
        if not has_status:
            missing.append("docs/status.yml")
        if not has_status_md:
            missing.append("docs/STATUS.md")
        fail(errors, f"trigger path changed but status.yml not updated: touched={touched}; "
                     f"missing in diff={missing}")


# ---------------------------------------------------------------------------
# Handoffs
# ---------------------------------------------------------------------------
def validate_handoff_file(p: Path, root: Path, errors: list[str], warnings: list[str],
                         offline: bool, pr_kind: str | None) -> None:
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
    if data.get("schema_version") not in ("1.0", "1.1"):
        fail(errors, f"{p.name}: schema_version must be '1.0' or '1.1'")
    if data.get("change_type") not in CHANGE_TYPES:
        fail(errors, f"{p.name}: change_type must be one of {sorted(CHANGE_TYPES)}")
    if data.get("compatibility") not in COMPAT:
        fail(errors, f"{p.name}: compatibility must be one of {sorted(COMPAT)}")
    pc = data.get("producer_commit", "")
    if not is_40hex(pc):
        fail(errors, f"{p.name}: producer_commit must be 40-char lowercase hex SHA")
    # producer_commit existence: online check, offline WARNING (never fake pass)
    if is_40hex(pc):
        if offline:
            warn(warnings, f"REMOTE_COMMIT_CHECK_SKIPPED_OFFLINE: {p.name} producer_commit {pc[:12]} not verified offline")
        else:
            if not commit_object_exists(root, pc):
                fail(errors, f"{p.name}: producer_commit {pc} does not exist in object store (online check)")
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
                    m = re.search(r"/blob/([^/]+)/", url)
                    if not m:
                        fail(errors, f"{p.name}: contract_refs[{i}].url must be a pinned /blob/<sha>/ URL")
                    elif not is_40hex(m.group(1)):
                        fail(errors, f"{p.name}: contract_refs[{i}].url must pin a 40-char commit SHA, got '{m.group(1)}'")
    tests = data.get("tests")
    if not isinstance(tests, list):
        fail(errors, f"{p.name}: tests must be a list")
    else:
        for i, t in enumerate(tests):
            if isinstance(t, dict) and t.get("status") not in TEST_STATUS:
                fail(errors, f"{p.name}: tests[{i}].status must be one of {TEST_STATUS}")
    if not isinstance(data.get("blockers"), list):
        fail(errors, f"{p.name}: blockers must be a list")
    # v1.1 lifecycle fields
    ls = data.get("lifecycle_state")
    if ls not in STATES_ALL:
        fail(errors, f"{p.name}: lifecycle_state must be one of {STATES_ALL}, got {ls!r}")
    ep = data.get("evidence_phase")
    if ep not in EVIDENCE_PHASES:
        fail(errors, f"{p.name}: evidence_phase must be one of {EVIDENCE_PHASES}, got {ep!r}")
    # feature_candidate PR must not carry a final (>=implemented) handoff
    FINAL = {"implemented", "verified", "accepted", "released", "deployed"}
    if pr_kind == "feature_candidate" and ls in FINAL:
        fail(errors, f"{p.name}: feature_candidate PR must not include a final handoff (lifecycle_state={ls})")


def validate_handoffs(root: Path, errors: list[str], warnings: list[str],
                      offline: bool, pr_kind: str | None) -> None:
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
        validate_handoff_file(p, root, errors, warnings, offline, pr_kind)


# ---------------------------------------------------------------------------
# PR template
# ---------------------------------------------------------------------------
def validate_pr_template(root: Path, errors: list[str]) -> None:
    p = root / ".github" / "PULL_REQUEST_TEMPLATE.md"
    if not p.exists():
        fail(errors, "missing .github/PULL_REQUEST_TEMPLATE.md")
        return
    text = p.read_text(encoding="utf-8")
    for needle in ["aliai-cross-repo-coordination", "STATUS.md", "handoff"]:
        if needle not in text:
            fail(errors, f"PULL_REQUEST_TEMPLATE.md: must mention '{needle}'")
    for needle in ["PR Kind", "feature candidate", "post-merge", "Lifecycle Target"]:
        if needle not in text:
            fail(errors, f"PULL_REQUEST_TEMPLATE.md: must contain v1.1 keyword {needle!r}")


# ---------------------------------------------------------------------------
# Top-level
# ---------------------------------------------------------------------------
def validate_repo(root: Path, base: str | None = None, head: str | None = None,
                  pr_kind: str | None = None, offline: bool = False
                  ) -> tuple[bool, list[str], list[str]]:
    errors: list[str] = []
    warnings: list[str] = []
    gov = load_governance(root, errors)
    status_yml = validate_status_yml(root, gov, errors, warnings, offline)
    validate_status_md(root, status_yml, errors)
    validate_handoffs(root, errors, warnings, offline, pr_kind)
    validate_pr_template(root, errors)
    if base and head:
        changed = changed_files_from_diff(root, base, head)
        check_trigger_paths(changed, gov, errors)
    return (len(errors) == 0, errors, warnings)


# ---------------------------------------------------------------------------
# Self-test fixtures
# ---------------------------------------------------------------------------
def _write(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")


def _git_setup_fixture(tmp: Path) -> str:
    git(tmp, "init", "-q")
    git(tmp, "config", "user.email", "ci@aliai.local")
    git(tmp, "config", "user.name", "aliai-ci")
    return _git_commit(tmp)


def _git_commit(tmp: Path) -> str:
    git(tmp, "add", "-A")
    git(tmp, "commit", "-q", "-m", "fixture")
    r = git(tmp, "rev-parse", "HEAD")
    return r.stdout.strip()


def _base_files(tmp: Path, role: str, life: dict, extra_status: dict,
                handoff_ls: str, handoff_pc: str) -> None:
    states = ROLE_STATES[role]
    gov = {
        "schema_version": "1.1",
        "repo": {"name": "o/r", "role": role, "authoritative_fields": ["api"]},
        "status": {"doc": "docs/STATUS.md", "machine_file": "docs/status.yml",
                   "last_verified_commit": "a" * 40},
        "handoffs": {"dir": "docs/handoffs", "readme": "docs/handoffs/README.md",
                     "schema_version": "1.1", "required_fields": HANDOFF_REQUIRED},
        "pr_template": ".github/PULL_REQUEST_TEMPLATE.md",
        "validator": {"script": "scripts/aliai_validate.py",
                      "ci_workflow": ".github/workflows/x.yml"},
        "upstream_producers": [], "downstream_consumers": [],
        "cross_repo_rules": {"declare_other_repo_dynamic_state": False,
                             "references_must_be_url_plus_commit": True,
                             "independent_pr_per_repo": True, "preserve_source_sha": True},
        "naming_traps": [{"note": "x"}],
        "status_lifecycle": {
            "machine_file": "docs/status.yml", "human_file": "docs/STATUS.md",
            "states": states,
            "transitions": ["planned→candidate", "candidate→implemented",
                            "implemented→verified", "verified→released",
                            "implemented→accepted", "implemented→deployed",
                            "any→blocked", "any→failed", "blocked→previous",
                            "failed→previous", "deployed→rolled_back", "any→superseded"],
            "trigger_paths": ["controller/**", "service/**"],
            "required_sections": STATUS_REQUIRED_SECTIONS,
            "post_merge_followup": {"enabled": True, "creator": "merge_agent",
                                    "target_state": "implemented"},
            "post_release_followup": {"enabled": True, "creator": "release_agent",
                                      "target_state": "released"},
            "role_requirements": {
                "implemented": {"requires_source_commit": True, "source_commit_format": "40-hex"},
                "verified": {"requires_tests_passed": True},
                "released": {"requires_fields": ["backend.image_tag", "backend.image_digest"],
                             "image_digest_format": "sha256:[a-f0-9]{64}"},
                "accepted": {"requires_fields": ["client.test_level"],
                              "test_level_value": "real_e2e", "requires_evidence": True},
                "deployed": {"requires_fields": ["deploy.actual_digest", "deploy.current_tag",
                                                 "deploy.health_evidence", "deploy.rollback_to"]},
            },
        },
    }
    _write(tmp / ".github" / "aliai-governance.yml", yaml.safe_dump(gov))
    status = {
        "schema_version": "1.1",
        "repo": {"name": "o/r", "role": role},
        "lifecycle": life,
        "artifact": {"name": "a", "version": "v1"},
        "tests": [{"level": "ci", "status": "passed", "evidence": "ci"}],
        "blockers": [],
    }
    status.update(extra_status)
    _write(tmp / "docs" / "status.yml", yaml.safe_dump(status))
    marker = (f"<!-- status-sync: state={life['current_state']} "
              f"source_commit={life['source_commit']} updated_at={life['updated_at']} -->")
    _write(tmp / "docs" / "STATUS.md",
           f"# s\n{marker}\n\n## 当前阶段\nx\n## 本仓权威事实\nx\n## 版本与提交\nx\n"
           f"## 开放项\nx\n## 跨仓引用\nx\n## 命名陷阱\nx\n")
    _write(tmp / "docs" / "handoffs" / "README.md", "# handoffs\n")
    _write(tmp / "docs" / "handoffs" / "TEMPLATE.yaml", "lifecycle_state: candidate\n")
    handoff = {
        "schema_version": "1.0", "producer_repo": "o/r", "producer_commit": handoff_pc,
        "artifact": "a", "version": "v1", "change_type": "contract_change",
        "compatibility": "backward", "summary": "s", "created_at": "2026-09-25",
        "contract_refs": [{"url": f"https://github.com/o/r/blob/{handoff_pc}/x.md", "kind": "api"}],
        "tests": [{"name": "t", "status": "passed", "evidence_url": ""}],
        "runtime_switches": {}, "downstream_actions": [], "blockers": [],
        "lifecycle_state": handoff_ls, "evidence_phase": "post_merge",
    }
    _write(tmp / "docs" / "handoffs" / "2026-09-25-h.yaml", yaml.safe_dump(handoff))
    _write(tmp / ".github" / "PULL_REQUEST_TEMPLATE.md",
           "aliai-cross-repo-coordination STATUS.md handoff "
           "PR Kind feature candidate post-merge Lifecycle Target\n")


def self_test() -> int:
    import shutil, tempfile
    results: list[tuple[str, bool]] = []

    PH = "a" * 40  # placeholder sha, overwritten by real head in valid cases

    def released_life(sha):
        return {"current_state": "released", "previous_state": "verified",
                "updated_at": "2026-09-26T00:00:00Z", "source_commit": sha,
                "evidence": "ci"}

    BACKEND_REL = {"backend": {"image_tag": "aliai/v1", "image_digest": "sha256:" + "e" * 64}}
    DEPLOY_FOUR = {"deploy": {"actual_digest": "sha256:" + "d" * 64, "current_tag": "t1",
                              "health_evidence": "ok", "rollback_to": "prev"}}

    def case(name, expect_fail, build, post=None):
        tmp = Path(tempfile.mkdtemp(prefix="aliai-st-"))
        try:
            build(tmp, PH)
            _git_setup_fixture(tmp)          # git init + first commit -> real head
            r = git(tmp, "rev-parse", "HEAD")
            head = r.stdout.strip()
            if post:
                post(tmp, head)
                _git_commit(tmp)
            ok, errs, warns = validate_repo(tmp)
            actually_fail = not ok
            good = (actually_fail == expect_fail)
            results.append((name, good))
            mark = "PASS" if good else "FAIL"
            print(f"[self-test] {name}: {mark} (expected_fail={expect_fail}, got_fail={actually_fail})")
            for e in errs:
                print(f"      err: {e}")
            for w in warns:
                print(f"      warn: {w}")
        finally:
            shutil.rmtree(tmp, ignore_errors=True)

    # --- Valid case C: backend released + image_tag + sha256 digest ---
    def build_rel(tmp, ph):
        _base_files(tmp, "backend", released_life(ph), dict(BACKEND_REL), "released", ph)
    def post_rel(tmp, head):
        _base_files(tmp, "backend", released_life(head), dict(BACKEND_REL), "released", head)
    case("valid backend released + image digest", False, build_rel, post_rel)

    # --- Valid case A: candidate + branch source_commit, no merge fields ---
    def build_cand(tmp, ph):
        _base_files(tmp, "backend",
                    {"current_state": "candidate", "previous_state": "planned",
                     "updated_at": "2026-09-26T00:00:00Z", "source_commit": ph, "evidence": ""},
                    {}, "candidate", ph)
    def post_cand(tmp, head):
        _base_files(tmp, "backend",
                    {"current_state": "candidate", "previous_state": "planned",
                     "updated_at": "2026-09-26T00:00:00Z", "source_commit": head, "evidence": ""},
                    {}, "candidate", head)
    case("valid candidate + branch source_commit", False, build_cand, post_cand)

    # --- Valid case B: implemented + 40-hex source_commit ---
    def build_impl(tmp, ph):
        _base_files(tmp, "backend",
                    {"current_state": "implemented", "previous_state": "candidate",
                     "updated_at": "2026-09-26T00:00:00Z", "source_commit": ph, "evidence": ""},
                    {}, "implemented", ph)
    def post_impl(tmp, head):
        _base_files(tmp, "backend",
                    {"current_state": "implemented", "previous_state": "candidate",
                     "updated_at": "2026-09-26T00:00:00Z", "source_commit": head, "evidence": ""},
                    {}, "implemented", head)
    case("valid implemented + 40-hex", False, build_impl, post_impl)

    # --- Valid case D: client accepted + test_level=real_e2e + evidence ---
    def build_acc(tmp, ph):
        _base_files(tmp, "client",
                    {"current_state": "accepted", "previous_state": "implemented",
                     "updated_at": "2026-09-26T00:00:00Z", "source_commit": ph, "evidence": "e2e"},
                    {"client": {"test_level": "real_e2e",
                                "upstream_contract_ref": f"https://github.com/o/r/blob/{ph}/x"}},
                    "accepted", ph)
    def post_acc(tmp, head):
        _base_files(tmp, "client",
                    {"current_state": "accepted", "previous_state": "implemented",
                     "updated_at": "2026-09-26T00:00:00Z", "source_commit": head, "evidence": "e2e"},
                    {"client": {"test_level": "real_e2e",
                                "upstream_contract_ref": f"https://github.com/o/r/blob/{head}/x"}},
                    "accepted", head)
    case("valid client accepted + real_e2e", False, build_acc, post_acc)

    # --- Valid case E: deploy deployed + four-piece ---
    def build_dep(tmp, ph):
        _base_files(tmp, "deploy",
                    {"current_state": "deployed", "previous_state": "implemented",
                     "updated_at": "2026-09-26T00:00:00Z", "source_commit": ph, "evidence": "health"},
                    dict(DEPLOY_FOUR), "deployed", ph)
    def post_dep(tmp, head):
        _base_files(tmp, "deploy",
                    {"current_state": "deployed", "previous_state": "implemented",
                     "updated_at": "2026-09-26T00:00:00Z", "source_commit": head, "evidence": "health"},
                    dict(DEPLOY_FOUR), "deployed", head)
    case("valid deploy deployed + four-piece", False, build_dep, post_dep)

    # --- 反例 3: candidate filled image_digest ---
    def bad_cand_field(tmp, ph):
        _base_files(tmp, "backend",
                    {"current_state": "candidate", "previous_state": "planned",
                     "updated_at": "2026-09-26T00:00:00Z", "source_commit": ph, "evidence": ""},
                    {"backend": {"image_tag": "t", "image_digest": "sha256:" + "e" * 64}},
                    "candidate", ph)
    case("反例3 candidate with image_digest", True, bad_cand_field)

    # --- 反例 4: illegal transition candidate->deployed (deploy role) ---
    def bad_trans(tmp, ph):
        _base_files(tmp, "deploy",
                    {"current_state": "deployed", "previous_state": "candidate",
                     "updated_at": "2026-09-26T00:00:00Z", "source_commit": ph, "evidence": ""},
                    dict(DEPLOY_FOUR), "deployed", ph)
    case("反例4 illegal candidate->deployed", True, bad_trans)

    # --- 反例 5: client accepted but test_level=mock_unit ---
    def bad_acc(tmp, ph):
        _base_files(tmp, "client",
                    {"current_state": "accepted", "previous_state": "implemented",
                     "updated_at": "2026-09-26T00:00:00Z", "source_commit": ph, "evidence": "x"},
                    {"client": {"test_level": "mock_unit", "upstream_contract_ref": ""}},
                    "accepted", ph)
    case("反例5 accepted but test_level=mock_unit", True, bad_acc)

    # --- 反例 6: backend released without image_digest ---
    def bad_rel(tmp, ph):
        _base_files(tmp, "backend", released_life(ph),
                    {"backend": {"image_tag": "t"}}, "released", ph)
    case("反例6 released without image_digest", True, bad_rel)

    # --- 反例 7: deploy deployed missing current_tag ---
    def bad_dep(tmp, ph):
        _base_files(tmp, "deploy",
                    {"current_state": "deployed", "previous_state": "implemented",
                     "updated_at": "2026-09-26T00:00:00Z", "source_commit": ph, "evidence": "h"},
                    {"deploy": {"actual_digest": "sha256:" + "d" * 64,
                                "health_evidence": "h", "rollback_to": "r"}},
                    "deployed", ph)
    case("反例7 deployed missing current_tag", True, bad_dep)

    # --- 反例 8: status.yml state != STATUS.md marker ---
    def bad_marker(tmp, ph):
        _base_files(tmp, "backend", released_life(ph), dict(BACKEND_REL), "released", ph)
        p = tmp / "docs" / "STATUS.md"
        t = p.read_text(encoding="utf-8").replace("state=released", "state=verified")
        p.write_text(t, encoding="utf-8")
    case("反例8 marker state mismatch", True, bad_marker)

    # --- 反例 9: handoff producer_commit does not exist remotely (online FAIL) ---
    def bad_hsha(tmp, ph):
        build_rel(tmp, ph)
    def post_hsha(tmp, head):
        _base_files(tmp, "backend", released_life(head), dict(BACKEND_REL), "released", head)
        h = tmp / "docs" / "handoffs" / "2026-09-25-h.yaml"
        d = yaml.safe_load(h.read_text(encoding="utf-8"))
        d["producer_commit"] = "f" * 40
        h.write_text(yaml.safe_dump(d), encoding="utf-8")
    case("反例9 handoff producer_commit missing remotely", True, bad_hsha, post_hsha)

    # --- 反例 10: current_state not in role list (deploy uses accepted) ---
    def bad_role(tmp, ph):
        _base_files(tmp, "deploy",
                    {"current_state": "accepted", "previous_state": "implemented",
                     "updated_at": "2026-09-26T00:00:00Z", "source_commit": ph, "evidence": "x"},
                    {}, "accepted", ph)
    case("反例10 deploy uses accepted (not allowed)", True, bad_role)

    # --- 反例 1 & 2: trigger-path diff logic (direct, no git needed) ---
    tmp = Path(tempfile.mkdtemp(prefix="aliai-st-"))
    try:
        build_rel(tmp, PH)
        _git_setup_fixture(tmp)
        gov = load_governance(tmp, [])
        e1: list[str] = []
        check_trigger_paths(["controller/foo.go"], gov, e1)
        ok1 = any("trigger path changed" in x for x in e1)
        results.append(("反例1 trigger changed but status not updated", ok1))
        print(f"[self-test] 反例1 trigger changed but status not updated: {'PASS' if ok1 else 'FAIL'}")
        e2: list[str] = []
        check_trigger_paths(["controller/foo.go", "docs/status.yml"], gov, e2)
        ok2 = any("STATUS.md" in x for x in e2)
        results.append(("反例2 only status.yml not STATUS.md", ok2))
        print(f"[self-test] 反例2 only status.yml not STATUS.md: {'PASS' if ok2 else 'FAIL'}")
    finally:
        shutil.rmtree(tmp, ignore_errors=True)

    failed = [n for n, ok in results if not ok]
    print(f"\n[self-test] {len(results) - len(failed)}/{len(results)} cases passed")
    if failed:
        print("[self-test] FAILING cases:")
        for n in failed:
            print(f"  - {n}")
        return 1
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("repo", nargs="?")
    ap.add_argument("--base", default=None)
    ap.add_argument("--head", default=None)
    ap.add_argument("--pr-kind", default=None, choices=PR_KINDS)
    ap.add_argument("--offline", action="store_true")
    ap.add_argument("--self-test", action="store_true")
    args = ap.parse_args()

    if args.self_test:
        return self_test()
    if not args.repo:
        print("Usage: python aliai_validate.py <repo-root> [--base REF] [--head REF] "
              "[--pr-kind KIND] [--offline] | --self-test")
        return 2
    root = Path(args.repo).resolve()
    ok, errors, warnings = validate_repo(root, args.base, args.head, args.pr_kind, args.offline)
    for w in warnings:
        print(f"WARNING: {w}")
    if args.offline:
        print("WARNING: REMOTE_COMMIT_CHECK_SKIPPED_OFFLINE (degraded pass)")
    if ok:
        print(f"PASS: governance validation for {root}")
        return 0
    print(f"FAIL: {len(errors)} issue(s) in {root}")
    for e in errors:
        print(f"  - {e}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
