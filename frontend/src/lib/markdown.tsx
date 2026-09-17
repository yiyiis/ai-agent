import type { Components } from 'react-markdown'

/** Markdown 链接渲染约定：统一新窗口打开。
 *  模型回复里的产物/附件是 COS 或后端直链，在当前窗口跳转会丢掉对话现场。 */
export const markdownLinkComponents: Components = {
  a: ({ node: _node, ...rest }) => (
    <a {...rest} target="_blank" rel="noopener noreferrer" />
  ),
}
