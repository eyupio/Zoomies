"""Build-time SEO metadata for zoomies.sh.

Two things the site wants that MkDocs does not know by itself, both derived
from the repository rather than written by hand, so neither can go stale:

* **A real last-modified date per page.** MkDocs' own sitemap stamps every URL
  with the build date, which tells a crawler that the whole site changed every
  time anything did -- exactly the signal that makes freshness meaningless. The
  date a page was last edited is in git, so it is taken from there and put on
  ``page.meta.git_date`` for ``overrides/sitemap.xml`` and the sharing tags.

* **``llms.txt``**, the plain-text index an assistant fetches when it is asked
  about a project instead of crawling it. It is generated from the navigation,
  so a page added to ``mkdocs.yml`` appears in it without anyone remembering.

This is a hook rather than a plugin because it needs nothing a plugin would
bring: the docs build stays at four pinned dependencies.

The git call is one process for the whole repository, not one per page. A
shallow checkout has no history to read, so the dates come back empty and every
page falls back to the build date -- which is why the site workflow checks out
with ``fetch-depth: 0``.
"""

from __future__ import annotations

import logging
import os
import subprocess
from datetime import datetime, timezone

log = logging.getLogger("mkdocs.hooks.seo")

# Populated once per build by on_files, keyed by path relative to docs_dir.
_dates: dict[str, str] = {}
# The resolved navigation, kept from on_nav so llms.txt can be written from it.
_nav = None


def _git_dates(docs_dir: str) -> dict[str, str]:
    """Map each file under docs_dir to the ISO date of its last commit."""
    cmd = [
        "git",
        "log",
        "--pretty=format:%x00%cI",
        "--name-only",
        "--no-renames",
        "--",
        ".",
    ]
    try:
        out = subprocess.run(
            cmd,
            cwd=docs_dir,
            capture_output=True,
            text=True,
            timeout=60,
            check=True,
        ).stdout
    except (OSError, subprocess.SubprocessError) as err:
        log.warning("no git history for the docs, so pages fall back to the build date: %s", err)
        return {}

    dates: dict[str, str] = {}
    current = ""
    for line in out.splitlines():
        if line.startswith("\x00"):
            # A commit header: everything until the next one was touched by it.
            current = line[1:11]
            continue
        path = line.strip()
        if not path:
            continue
        # git prints paths relative to the repository root even with a cwd, and
        # the newest commit touching a file is the first one seen.
        dates.setdefault(path, current)
    return dates


def on_files(files, config):
    """Read the history once, before any page is rendered."""
    global _dates
    root = os.path.relpath(config["docs_dir"], os.path.dirname(config["config_file_path"]))
    raw = _git_dates(config["docs_dir"])
    prefix = root.replace(os.sep, "/") + "/"
    _dates = {}
    for path, date in raw.items():
        key = path[len(prefix):] if path.startswith(prefix) else path
        _dates.setdefault(key, date)
    return files


def on_nav(nav, config, files):
    """Keep the resolved navigation for llms.txt."""
    global _nav
    _nav = nav
    return nav


def on_page_context(context, page, config, nav):
    """Hand the page's own last-edited date to the templates."""
    date = _dates.get(page.file.src_uri)
    if date:
        page.meta.setdefault("git_date", date)
    return context


def on_post_build(config):
    """Write llms.txt: the site's table of contents, in one plain-text file."""
    lines = [
        f"# {config['site_name']}",
        "",
        f"> {' '.join(str(config['site_description']).split())}",
        "",
        "Zoomies is a single Go binary that watches a GitHub organisation for",
        "queued Actions jobs, starts one ephemeral runner per job, and destroys",
        "it when the job finishes. SQLite for state, no Kubernetes, MIT licensed.",
        "",
        "## Documentation",
        "",
    ]

    site_url = str(config["site_url"] or "").rstrip("/")

    def walk(items):
        for item in items:
            if item.is_page:
                url = item.canonical_url or f"{site_url}/{item.url}"
                summary = " ".join(str(item.meta.get("description", "")).split())
                lines.append(f"- [{item.title}]({url})" + (f": {summary}" if summary else ""))
            elif item.is_section:
                lines.append("")
                lines.append(f"### {item.title}")
                lines.append("")
                walk(item.children)

    walk(_nav.items if _nav is not None else [])

    lines += [
        "",
        "## Source",
        "",
        f"- [Repository]({config['repo_url']}): the Go controller, the Svelte UI and these docs.",
        f"- [install.sh]({site_url}/install.sh): the one-line installer, byte-identical to the one in the repository.",
        f"- [OpenAPI document]({config['repo_url']}/blob/main/api/openapi.yaml): the REST contract both clients are generated from.",
        "",
        f"Generated {datetime.now(timezone.utc).strftime('%Y-%m-%d')}.",
        "",
    ]

    with open(os.path.join(config["site_dir"], "llms.txt"), "w", encoding="utf-8") as fh:
        fh.write("\n".join(lines))
