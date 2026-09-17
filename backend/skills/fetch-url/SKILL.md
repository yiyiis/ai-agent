---
name: fetch-url
description: 抓取公开网页并把正文转成干净的 Markdown 文本。用户给出网址想"读一下 / 总结 / 提取信息"，或搜索结果需要打开原文阅读时使用。对"只要链接列表不用读正文"的场景不适用。
tags: [工具, 网页]
---

# Fetch URL → Markdown

抓取一个网页，返回其正文文本。默认走 Jina Reader（`https://r.jina.ai/<url>`，免费、无需
API Key，能处理 JS 渲染页面并剥离页面模板），失败时回退为直接抓 HTML 并做朴素去标签。

## 何时使用

- 用户贴出一个 URL 并询问关于它的任何问题
- 刚做过搜索，需要读取某条结果的原文
- 用户要求"总结 / 提取"某个具体页面

如果用户只想要链接列表（不读正文），不要用本技能。

## 用法

```bash
python3 skills/fetch-url/scripts/fetch.py <url> [--mode jina|raw] [--max-chars N]
```

- `url`（必填）：完整网址，含 `http(s)://`；带 `&` 等特殊字符时记得加引号
- `--mode jina`（默认）：Jina Reader，返回清洗后的 Markdown
- `--mode raw`：绕过 Jina 直接抓 HTML 并去标签，Jina 失败或非 HTML 资源时使用
- `--max-chars N`：截断输出到 N 字符（默认 12000）

输出走 stdout；出错时错误信息走 stderr 且退出码非零。

## 流程

1. 默认 `--mode jina`，绝大多数页面可用
2. Jina 返回 4xx/5xx 或空正文时，换 `--mode raw` 重试
3. 把清洗后的文本交给用户（原样或按用户要求总结），引用来源 URL

## 限制

- 登录 / 付费墙后的页面无法抓取
- 超大页面会被 `--max-chars` 截断
- PDF 请改用专门的 PDF 技能（如有）
