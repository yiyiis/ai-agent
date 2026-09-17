#!/usr/bin/env python3
"""抓取网页并输出正文文本。

用法: fetch.py <url> [--mode jina|raw] [--max-chars N]
  jina (默认): 经 https://r.jina.ai/<url> 取清洗后的 Markdown
  raw:        直接抓 HTML，做朴素去标签
仅依赖标准库。出错时向 stderr 输出原因并以非零码退出。
"""

import argparse
import html
import re
import sys
import urllib.error
import urllib.request

UA = "ai-agent-skill-fetch-url/1.0"
DEFAULT_MAX_CHARS = 12000
TIMEOUT = 30


def http_get(url: str) -> str:
    req = urllib.request.Request(url, headers={"User-Agent": UA})
    with urllib.request.urlopen(req, timeout=TIMEOUT) as resp:
        data = resp.read(2 * 1024 * 1024)  # 2MB 上限，防超大页面拖垮沙箱
        charset = resp.headers.get_content_charset() or "utf-8"
        return data.decode(charset, errors="replace")


def fetch_jina(url: str) -> str:
    return http_get("https://r.jina.ai/" + url)


TAG_RE = re.compile(r"<(script|style)[^>]*>.*?</\1>", re.IGNORECASE | re.DOTALL)
ANY_TAG_RE = re.compile(r"<[^>]+>")
SPACE_RE = re.compile(r"\n{3,}")


def strip_html(text: str) -> str:
    text = TAG_RE.sub(" ", text)
    text = ANY_TAG_RE.sub(" ", html.unescape(text))
    return SPACE_RE.sub("\n\n", text).strip()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("url", help="完整网址，含 http(s)://")
    parser.add_argument("--mode", choices=["jina", "raw"], default="jina")
    parser.add_argument("--max-chars", type=int, default=DEFAULT_MAX_CHARS)
    args = parser.parse_args()

    if not args.url.lower().startswith(("http://", "https://")):
        print("url 必须以 http:// 或 https:// 开头", file=sys.stderr)
        return 2

    text, err = "", ""
    try:
        text = fetch_jina(args.url) if args.mode == "jina" else strip_html(http_get(args.url))
    except urllib.error.HTTPError as e:
        err = f"HTTP {e.code} {e.reason}"
    except Exception as e:  # noqa: BLE001 - 网络栈错误统一转文案
        err = str(e)

    if not text.strip():
        if args.mode == "jina":
            print(f"[jina 抓取失败：{err or '空正文'}，请换 --mode raw 重试]", file=sys.stderr)
        else:
            print(f"[抓取失败：{err or '空正文'}]", file=sys.stderr)
        return 1

    text = text.strip()
    if len(text) > args.max_chars:
        text = text[: args.max_chars] + f"\n\n…（已截断，原文共 {len(text)} 字符）"
    print(text)
    return 0


if __name__ == "__main__":
    sys.exit(main())
