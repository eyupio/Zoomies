"""Image loading for zoomies.sh: do not fetch what the reader cannot see, and do
not reflow the page as pictures arrive.

Every screenshot in these docs is in the page twice, once for each colour
scheme, and Material hides the one that does not match with `display: none`.
That stops it being drawn but not being fetched: the UI tour made 44 image
requests to show 22 pictures, and the half nobody saw was about 3 MB. An `<img>`
that is lazy and has no layout box is never requested, so marking every content
image lazy removes that on its own, and the screenshots below the fold wait until
the reader scrolls towards them.

Lazy images with no declared size have a second cost: the page is shorter than
it will be, grows as each one arrives, and the scrollbar jumps. The width and
height are known at build time, because the files are in the repository, so they
are written into the tag. Material's own `height: auto` keeps the picture in
proportion at whatever width the column gives it; the attributes only tell the
browser the aspect ratio to reserve.

This is a hook rather than an `attr_list` on every image because there are
dozens of them, a new screenshot would have to remember it, and the sizes are
the kind of fact that drifts when written by hand. It reads the headers of the
two formats the site uses, PNG and WebP, and leaves a file it cannot read alone.
"""

from __future__ import annotations

import logging
import os
import posixpath
import re
import struct

log = logging.getLogger("mkdocs.hooks.images")

_img = re.compile(r"<img\b[^>]*>", re.IGNORECASE)
_src = re.compile(r"""\ssrc\s*=\s*(?:"([^"]*)"|'([^']*)')""", re.IGNORECASE)


def _has(tag: str, name: str) -> bool:
    return re.search(rf"\s{name}\s*=", tag, re.IGNORECASE) is not None


def _size(path: str) -> tuple[int, int] | None:
    """Pixel size of a PNG or WebP file, from its header, or None."""
    try:
        with open(path, "rb") as fh:
            head = fh.read(32)
    except OSError:
        return None
    if head[:8] == b"\x89PNG\r\n\x1a\n" and len(head) >= 24:
        return struct.unpack(">II", head[16:24])
    if head[:4] == b"RIFF" and head[8:12] == b"WEBP" and len(head) >= 30:
        kind = head[12:16]
        if kind == b"VP8 ":
            # Lossy: a 14-bit width and height after the frame's start code.
            w, h = struct.unpack("<HH", head[26:30])
            return w & 0x3FFF, h & 0x3FFF
        if kind == b"VP8L":
            # Lossless: 14 bits each, stored as size minus one.
            bits = int.from_bytes(head[21:25], "little")
            return (bits & 0x3FFF) + 1, ((bits >> 14) & 0x3FFF) + 1
        if kind == b"VP8X":
            # Extended: the canvas, as 24-bit sizes minus one.
            w = int.from_bytes(head[24:27], "little") + 1
            h = int.from_bytes(head[27:30], "little") + 1
            return w, h
    return None


def on_page_content(html, page, config, files):
    """Make every content image lazy, and give it the size it will be drawn at."""
    # Where the page will be served from, so a relative `src` can be turned back
    # into a path in docs_dir: quickstart.md is served from quickstart/, and its
    # screenshots are `../screenshots/...`.
    served_from = posixpath.dirname(page.file.dest_uri)

    def fix(match: re.Match) -> str:
        tag = match.group(0)
        found = _src.search(tag)
        source = (found.group(1) or found.group(2)) if found else ""
        if not source or source.startswith("data:"):
            return tag
        add = []
        if not _has(tag, "loading"):
            add.append('loading="lazy"')
        if not _has(tag, "decoding"):
            add.append('decoding="async"')
        if not (_has(tag, "width") or _has(tag, "height")) and "://" not in source:
            # `#only-dark` and `#only-light` are Material's, not part of the path.
            relative = source.split("#", 1)[0].split("?", 1)[0]
            local = posixpath.normpath(posixpath.join(served_from, relative))
            size = _size(os.path.join(config["docs_dir"], *local.split("/")))
            if size:
                add.append(f'width="{size[0]}" height="{size[1]}"')
        if not add:
            return tag
        return tag[:4] + " " + " ".join(add) + tag[4:]

    return _img.sub(fix, html)
