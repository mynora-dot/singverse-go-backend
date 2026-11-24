#!/usr/bin/env python3
"""用同一份中文说明创建 Git 注释标签和 GitHub Release 草稿。"""
import argparse
from pathlib import Path
import re
import subprocess

from build_version import SEMVER


def github_repository():
    urls = subprocess.check_output(
        ["git", "remote", "get-url", "--push", "--all", "origin"], encoding="utf-8"
    ).splitlines()
    if len(urls) != 1:
        raise ValueError("origin 需要配置唯一的 GitHub 推送地址")
    match = re.fullmatch(
        r"(?:https://github\.com/|git@github\.com:|ssh://git@github\.com/)([\w.-]+/[\w.-]+)",
        urls[0].removesuffix(".git"),
        flags=re.ASCII,
    )
    if not match:
        raise ValueError("origin 的推送地址须指向 github.com 仓库")
    return match[1]


def prepare_tag(tag, tag_ref, revision, notes):
    existing = subprocess.check_output(
        ["git", "tag", "--list", tag, "--format=%(objecttype)%00%(contents)%00%(contents:signature)"],
        encoding="utf-8",
    ).removesuffix("\n")
    if not existing:
        subprocess.run(
            ["git", "tag", "-a", "--cleanup=verbatim", "-F", "-", tag, revision],
            input=notes, encoding="utf-8", check=True,
        )
        return

    object_type, annotation, signature = existing.split("\0", 2)
    tagged_revision = subprocess.check_output(
        ["git", "rev-parse", "--verify", tag_ref + "^{commit}"], encoding="utf-8"
    ).strip()
    if object_type != "tag" or tagged_revision != revision or annotation.removesuffix(signature) != notes:
        raise ValueError(f"标签 {tag} 已存在，须使用新的版本号")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tag", help="正式版本标签，例如 v1.2.3")
    parser.add_argument("notes", type=Path, help="UTF-8 编码的中文发布说明 Markdown 文件")
    parser.add_argument("--ref", default="HEAD", help="要发布的提交或分支，默认 HEAD")
    args = parser.parse_args()

    if not args.tag.startswith("v") or not SEMVER.fullmatch(args.tag[1:]):
        raise ValueError("正式标签须使用 vMAJOR.MINOR.PATCH 格式，例如 v1.2.3")
    notes = args.notes.read_text(encoding="utf-8-sig").strip("\n") + "\n"
    if "\0" in notes or not re.search(r"[\u3400-\u4dbf\u4e00-\u9fff]", notes):
        raise ValueError("请提供包含中文正文的 UTF-8 发布说明")

    repository = github_repository()
    if repository.lower().split("/")[-1] in {"singverse-mobile", "singverse-tv"}:
        if len(notes.strip().encode("utf-8")) > 16000:
            raise ValueError("手机端和电视端发布说明最多为 16000 个 UTF-8 字节")
    revision = subprocess.check_output(
        ["git", "rev-parse", "--verify", "--end-of-options", args.ref + "^{commit}"],
        encoding="utf-8",
    ).strip()
    existing_release = subprocess.check_output(
        ["gh", "api", "--hostname", "github.com", "--paginate",
         f"repos/{repository}/releases?per_page=100",
         "--jq", f'.[] | select(.tag_name == "{args.tag}") | .html_url'],
        encoding="utf-8",
    ).strip()
    if existing_release:
        raise ValueError(f"{args.tag} 已有 GitHub Release，请查看现有发布页面\n{existing_release}")

    tag_ref = f"refs/tags/{args.tag}"
    prepare_tag(args.tag, tag_ref, revision, notes)
    subprocess.run(
        ["git", "push", "--no-mirror", "--no-follow-tags", "origin", f"{tag_ref}:{tag_ref}"],
        check=True,
    )
    subprocess.run(
        ["gh", "release", "create", args.tag, "--repo", f"github.com/{repository}",
         "--draft", "--verify-tag", "--title", f"{args.tag} 正式发布", "--notes-file", "-"],
        input=notes, encoding="utf-8", check=True,
    )


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError) as error:
        raise SystemExit(str(error))
    except subprocess.CalledProcessError as error:
        raise SystemExit(f"{error.cmd[0]} 执行失败，退出码 {error.returncode}")
