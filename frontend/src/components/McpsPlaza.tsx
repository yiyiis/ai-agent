import { useEffect, useState } from 'react'
import {
  ArrowLeft,
  CheckCircle2,
  Loader2,
  Pencil,
  Plug,
  Plus,
  RefreshCw,
  Server,
  Trash2,
  X,
  XCircle,
} from 'lucide-react'
import { api } from '../api/client'
import type { McpServer, McpTestResult } from '../types'
import { cn } from '../lib/utils'

/**
 * MCP 广场（阶段八）：共享生态页——所有用户可浏览、添加（http 型）、测试；
 * 编辑/删除限创建者，system 种子（config 投影）只读。
 */
export function McpsPlaza() {
  const [mcps, setMcps] = useState<McpServer[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<McpServer | null>(null)
  const [testingId, setTestingId] = useState<number | null>(null)
  const [testResult, setTestResult] = useState<McpTestResult | null>(null)

  const refresh = async () => {
    setLoading(true)
    try {
      setMcps(await api.listMcps())
      setError(null)
    } catch (e) {
      setError(String(e))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    refresh()
  }, [])

  const onTest = async (m: McpServer) => {
    setTestingId(m.id)
    setTestResult(null)
    try {
      const result = await api.testMcp(m.id)
      setTestResult(result)
      await refresh()
    } catch (e) {
      setError(String(e))
    } finally {
      setTestingId(null)
    }
  }

  const onDelete = async (m: McpServer) => {
    if (!confirm(`删除 MCP 服务 "${m.name}"？正在使用它的会话将立即失去其工具。`)) return
    try {
      await api.deleteMcp(m.id)
      await refresh()
    } catch (e) {
      setError(String(e))
    }
  }

  return (
    <div className="flex-1 overflow-y-auto scrollbar-thin bg-white dark:bg-[#131314]">
      {/* 顶栏 */}
      <div className="sticky top-0 z-10 px-8 py-4 bg-white/85 dark:bg-[#131314]/85 backdrop-blur-md border-b border-[#e3e3e3]/70 dark:border-[#28292a] flex items-center gap-3">
        <button
          onClick={() => history.back()}
          className="p-2 rounded-full text-[#444746] dark:text-[#c4c7c5] hover:bg-[#e1e7f0]/60 dark:hover:bg-[#28292a] transition"
          title="返回"
        >
          <ArrowLeft className="w-4 h-4" />
        </button>
        <Plug className="w-5 h-5 text-[#1a73e8]" />
        <div className="flex-1 min-w-0">
          <h1 className="text-base font-medium text-[#1f1f1f] dark:text-[#f1f3f4]">MCP 广场</h1>
          <p className="text-xs text-[#747775] dark:text-[#9aa0a6]">
            Model Context Protocol 标准工具服务：添加后在会话里启用，模型即可调用远端工具
          </p>
        </div>
        <button
          onClick={refresh}
          className="p-2 rounded-full text-[#444746] dark:text-[#c4c7c5] hover:bg-[#e1e7f0]/60 dark:hover:bg-[#28292a] transition"
          title="刷新"
        >
          <RefreshCw className={cn('w-4 h-4', loading && 'animate-spin')} />
        </button>
        <button
          onClick={() => setCreating(true)}
          className="flex items-center gap-1.5 px-3.5 py-2 text-xs font-medium rounded-full bg-[#1a73e8] dark:bg-[#8ab4f8] text-white dark:text-[#041e49] hover:brightness-110 transition shadow-sm"
        >
          <Plus className="w-3.5 h-3.5" />
          添加 MCP Server
        </button>
      </div>

      {error && (
        <div className="mx-8 mt-4 px-3 py-2 bg-red-50 dark:bg-red-950/30 text-red-600 dark:text-red-400 text-xs rounded border border-red-100 dark:border-red-900">
          {error}
        </div>
      )}

      {/* 卡片区 */}
      <div className="px-8 py-6">
        {loading && mcps.length === 0 ? (
          <div className="flex items-center justify-center gap-2 py-20 text-sm text-[#747775] dark:text-[#9aa0a6]">
            <Loader2 className="w-4 h-4 animate-spin" />
            加载中…
          </div>
        ) : mcps.length === 0 ? (
          <div className="text-center py-20">
            <Server className="w-10 h-10 mx-auto text-[#c4c7c5] dark:text-[#3c4043]" />
            <p className="mt-3 text-sm text-[#747775] dark:text-[#9aa0a6]">
              广场还没有 MCP Server，点右上角添加第一个
            </p>
            <p className="mt-1 text-xs text-[#747775] dark:text-[#9aa0a6]">
              支持 Streamable HTTP 端点；stdio 型由管理员在配置文件中导入
            </p>
          </div>
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
            {mcps.map((m) => (
              <McpCard
                key={m.id}
                mcp={m}
                testing={testingId === m.id}
                onTest={() => onTest(m)}
                onEdit={() => setEditing(m)}
                onDelete={() => onDelete(m)}
              />
            ))}
          </div>
        )}
      </div>

      {creating && (
        <McpEditModal
          onClose={() => setCreating(false)}
          onSaved={async () => {
            setCreating(false)
            await refresh()
          }}
        />
      )}
      {editing && (
        <McpEditModal
          existing={editing}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null)
            await refresh()
          }}
        />
      )}
      {testResult && (
        <McpToolsModal result={testResult} onClose={() => setTestResult(null)} />
      )}
    </div>
  )
}

/** 状态点：绿色=最近测试成功；红色=最近失败；灰色=未测试 */
function statusDot(m: McpServer) {
  if (m.last_error) return 'bg-red-500'
  if (m.last_synced_at) return 'bg-emerald-500'
  return 'bg-neutral-300 dark:bg-[#5f6368]'
}

function McpCard({
  mcp,
  testing,
  onTest,
  onEdit,
  onDelete,
}: {
  mcp: McpServer
  testing: boolean
  onTest: () => void
  onEdit: () => void
  onDelete: () => void
}) {
  return (
    <div className="group rounded-2xl border border-[#e3e3e3] dark:border-[#3c4043] bg-white dark:bg-[#1e1f20] p-4 flex flex-col gap-3 hover:shadow-md transition-shadow">
      <div className="flex items-start justify-between gap-2">
        <div className="flex items-center gap-2 min-w-0">
          <span className={cn('w-2 h-2 rounded-full shrink-0', statusDot(mcp))} title={
            mcp.last_error ? `最近错误：${mcp.last_error}` : mcp.last_synced_at ? '连接正常' : '未测试'
          } />
          <span className="text-sm font-medium text-[#1f1f1f] dark:text-[#f1f3f4] truncate">
            {mcp.name}
          </span>
          {mcp.system && (
            <span className="shrink-0 text-[10px] px-1.5 py-px rounded bg-[#d3e3fd] text-[#041e49] dark:bg-[#004a77] dark:text-[#c2e7ff]">
              官方
            </span>
          )}
        </div>
        <span className="shrink-0 text-[10px] px-1.5 py-px rounded bg-neutral-100 dark:bg-[#28292a] text-[#5f6368] dark:text-[#9aa0a6] uppercase">
          {mcp.transport}
        </span>
      </div>

      <p className="text-xs text-[#5f6368] dark:text-[#9aa0a6] line-clamp-2 min-h-[2rem]">
        {mcp.description || '暂无描述'}
      </p>

      {mcp.url && (
        <p className="text-[11px] text-[#747775] dark:text-[#747775] truncate" title={mcp.url}>
          {mcp.url}
        </p>
      )}

      {mcp.last_error && (
        <p className="text-[11px] text-red-500 dark:text-red-400 line-clamp-2" title={mcp.last_error}>
          {mcp.last_error}
        </p>
      )}

      <div className="flex items-center justify-between gap-2 pt-1 border-t border-[#e3e3e3]/70 dark:border-[#3c4043]/60">
        <span className="text-[11px] text-[#747775] dark:text-[#9aa0a6]">
          {mcp.tool_count ? `${mcp.tool_count} 个工具` : '工具数未知'}
          <span className="mx-1">·</span>
          {mcp.owner_id ? mcp.owner_name || `用户 ${mcp.owner_id}` : '系统'}
        </span>
        <div className="flex items-center gap-0.5">
          <button
            onClick={onTest}
            disabled={testing}
            className="flex items-center gap-1 px-2 py-1 text-[11px] rounded text-[#1a73e8] dark:text-[#8ab4f8] hover:bg-[#d3e3fd]/40 dark:hover:bg-[#004a77]/40 transition disabled:opacity-50"
            title="连接并发现工具"
          >
            {testing ? <Loader2 className="w-3 h-3 animate-spin" /> : <RefreshCw className="w-3 h-3" />}
            测试
          </button>
          {mcp.is_owner && !mcp.system && (
            <>
              <button
                onClick={onEdit}
                className="p-1.5 rounded text-[#5f6368] dark:text-[#9aa0a6] hover:bg-neutral-100 dark:hover:bg-[#28292a] transition"
                title="编辑"
              >
                <Pencil className="w-3.5 h-3.5" />
              </button>
              <button
                onClick={onDelete}
                className="p-1.5 rounded text-red-500 hover:bg-red-50 dark:hover:bg-red-950/40 transition"
                title="删除"
              >
                <Trash2 className="w-3.5 h-3.5" />
              </button>
            </>
          )}
        </div>
      </div>
    </div>
  )
}

/** 新增 / 编辑弹窗（新增仅 http；编辑可改 URL、鉴权头、白名单、超时、启停） */
function McpEditModal({
  existing,
  onClose,
  onSaved,
}: {
  existing?: McpServer
  onClose: () => void
  onSaved: () => void
}) {
  const isEdit = Boolean(existing)
  const [name, setName] = useState(existing?.name ?? '')
  const [description, setDescription] = useState(existing?.description ?? '')
  const [url, setUrl] = useState(existing?.url ?? '')
  // 鉴权头 KV 行（编辑态初始为空：值已打码，仅提交了新值才覆盖）
  const [headerRows, setHeaderRows] = useState<Array<{ k: string; v: string }>>(
    isEdit
      ? Object.keys(existing?.headers_masked ?? {}).map((k) => ({ k, v: '' }))
      : [{ k: 'Authorization', v: '' }],
  )
  const [toolAllow, setToolAllow] = useState((existing?.tool_allow ?? []).join(', '))
  const [timeoutSec, setTimeoutSec] = useState(String(existing?.call_timeout_sec ?? 60))
  const [enabled, setEnabled] = useState(existing?.enabled ?? true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = async () => {
    setError(null)
    if (!isEdit && !/^[a-z0-9][a-z0-9-]{0,62}$/.test(name.trim())) {
      setError('名字需为 1-64 位小写字母、数字与连字符（如 weather-api）')
      return
    }
    // 鉴权头：任何值都没填新值 → 不提交该字段（保持原值）；
    // 填了至少一个 → 整体替换（值为空的行被清除）
    const touched = headerRows.some((r) => r.v.trim() !== '')
    const headers: Record<string, string> = {}
    if (!isEdit || touched) {
      for (const r of headerRows) {
        const k = r.k.trim()
        if (k && r.v.trim() !== '') headers[k] = r.v.trim()
      }
    }
    const allow = toolAllow.split(',').map((s) => s.trim()).filter(Boolean)
    try {
      setSaving(true)
      if (isEdit) {
        await api.updateMcp(existing!.id, {
          description,
          url,
          ...(touched || !isEdit ? { headers } : {}),
          tool_allow: allow,
          call_timeout_sec: Number(timeoutSec) || 60,
          enabled,
        })
      } else {
        await api.createMcp({
          name: name.trim(),
          description,
          url: url.trim(),
          headers,
          tool_allow: allow,
          call_timeout_sec: Number(timeoutSec) || 60,
        })
      }
      onSaved()
    } catch (e) {
      setError(String(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4">
      <div className="w-full max-w-lg max-h-[85vh] overflow-y-auto scrollbar-thin rounded-2xl bg-white dark:bg-[#1e1f20] border border-[#e3e3e3] dark:border-[#3c4043] shadow-2xl">
        <header className="flex items-center justify-between px-5 py-3.5 border-b border-[#e3e3e3]/70 dark:border-[#28292a] sticky top-0 bg-white dark:bg-[#1e1f20]">
          <h2 className="text-sm font-medium text-[#1f1f1f] dark:text-[#f1f3f4]">
            {isEdit ? `编辑 ${existing?.name}` : '添加 MCP Server'}
          </h2>
          <button onClick={onClose} className="p-1 rounded hover:bg-neutral-100 dark:hover:bg-[#28292a]">
            <X className="w-4 h-4 text-[#5f6368] dark:text-[#9aa0a6]" />
          </button>
        </header>

        <div className="px-5 py-4 space-y-4 text-sm">
          {!isEdit && (
            <div>
              <label className="block text-xs font-medium text-[#5f6368] dark:text-[#9aa0a6] mb-1">
                短名（工具调用前缀，全局唯一，创建后不可改）
              </label>
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="如 weather-api"
                className="w-full px-3 py-2 rounded-lg border border-[#e3e3e3] dark:border-[#3c4043] bg-white dark:bg-[#131314] text-[#1f1f1f] dark:text-[#e3e3e3] text-sm focus:outline-none focus:border-[#1a73e8]"
              />
            </div>
          )}
          <div>
            <label className="block text-xs font-medium text-[#5f6368] dark:text-[#9aa0a6] mb-1">描述</label>
            <input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="这个 MCP Server 提供什么能力？"
              className="w-full px-3 py-2 rounded-lg border border-[#e3e3e3] dark:border-[#3c4043] bg-white dark:bg-[#131314] text-[#1f1f1f] dark:text-[#e3e3e3] text-sm focus:outline-none focus:border-[#1a73e8]"
            />
          </div>
          <div>
            <label className="block text-xs font-medium text-[#5f6368] dark:text-[#9aa0a6] mb-1">
              端点 URL（Streamable HTTP）
            </label>
            <input
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder="https://mcp.example.com/mcp"
              className="w-full px-3 py-2 rounded-lg border border-[#e3e3e3] dark:border-[#3c4043] bg-white dark:bg-[#131314] text-[#1f1f1f] dark:text-[#e3e3e3] text-sm focus:outline-none focus:border-[#1a73e8]"
            />
          </div>

          <div>
            <div className="flex items-center justify-between mb-1">
              <label className="text-xs font-medium text-[#5f6368] dark:text-[#9aa0a6]">鉴权头（可选）</label>
              <button
                type="button"
                onClick={() => setHeaderRows((rs) => [...rs, { k: '', v: '' }])}
                className="text-xs text-[#1a73e8] dark:text-[#8ab4f8] hover:underline"
              >
                + 添加一行
              </button>
            </div>
            <div className="space-y-2">
              {headerRows.map((r, i) => (
                <div key={i} className="flex items-center gap-2">
                  <input
                    value={r.k}
                    onChange={(e) =>
                      setHeaderRows((rs) => rs.map((x, j) => (j === i ? { ...x, k: e.target.value } : x)))
                    }
                    placeholder="Header 名（如 Authorization）"
                    className="w-2/5 px-2.5 py-1.5 rounded-lg border border-[#e3e3e3] dark:border-[#3c4043] bg-white dark:bg-[#131314] text-xs focus:outline-none focus:border-[#1a73e8]"
                  />
                  <input
                    value={r.v}
                    type="password"
                    onChange={(e) =>
                      setHeaderRows((rs) => rs.map((x, j) => (j === i ? { ...x, v: e.target.value } : x)))
                    }
                    placeholder={isEdit ? '留空 = 保持现有值（填任意新值后保存将整体替换）' : '值（如 Bearer xxx）'}
                    className="flex-1 px-2.5 py-1.5 rounded-lg border border-[#e3e3e3] dark:border-[#3c4043] bg-white dark:bg-[#131314] text-xs focus:outline-none focus:border-[#1a73e8]"
                  />
                  <button
                    type="button"
                    onClick={() => setHeaderRows((rs) => rs.filter((_, j) => j !== i))}
                    className="p-1 rounded text-red-500 hover:bg-red-50 dark:hover:bg-red-950/40"
                    title="删除此头"
                  >
                    <Trash2 className="w-3.5 h-3.5" />
                  </button>
                </div>
              ))}
            </div>
            <p className="mt-1.5 text-[11px] text-[#747775] dark:text-[#747775]">
              明文存储于服务端且所有用户共享此 Server；值不会回显
            </p>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs font-medium text-[#5f6368] dark:text-[#9aa0a6] mb-1">
                工具白名单（可选，逗号分隔）
              </label>
              <input
                value={toolAllow}
                onChange={(e) => setToolAllow(e.target.value)}
                placeholder="留空 = 全部工具"
                className="w-full px-3 py-2 rounded-lg border border-[#e3e3e3] dark:border-[#3c4043] bg-white dark:bg-[#131314] text-xs focus:outline-none focus:border-[#1a73e8]"
              />
            </div>
            <div>
              <label className="block text-xs font-medium text-[#5f6368] dark:text-[#9aa0a6] mb-1">
                调用超时（秒）
              </label>
              <input
                value={timeoutSec}
                onChange={(e) => setTimeoutSec(e.target.value)}
                type="number"
                min={5}
                max={600}
                className="w-full px-3 py-2 rounded-lg border border-[#e3e3e3] dark:border-[#3c4043] bg-white dark:bg-[#131314] text-xs focus:outline-none focus:border-[#1a73e8]"
              />
            </div>
          </div>

          {isEdit && (
            <label className="flex items-center gap-2 text-xs text-[#5f6368] dark:text-[#9aa0a6]">
              <input
                type="checkbox"
                checked={enabled}
                onChange={(e) => setEnabled(e.target.checked)}
                className="rounded"
              />
              启用（停用后所有会话无法使用其工具）
            </label>
          )}

          {error && (
            <div className="px-3 py-2 bg-red-50 dark:bg-red-950/30 text-red-600 dark:text-red-400 text-xs rounded border border-red-100 dark:border-red-900">
              {error}
            </div>
          )}
        </div>

        <footer className="px-5 py-3.5 border-t border-[#e3e3e3]/70 dark:border-[#28292a] flex justify-end gap-2">
          <button
            onClick={onClose}
            className="px-4 py-1.5 text-xs rounded-full border border-[#e3e3e3] dark:border-[#3c4043] text-[#5f6368] dark:text-[#c4c7c5] hover:bg-neutral-50 dark:hover:bg-[#28292a]"
          >
            取消
          </button>
          <button
            onClick={submit}
            disabled={saving || (!isEdit && (!name || !url))}
            className="px-4 py-1.5 text-xs rounded-full bg-[#1a73e8] dark:bg-[#8ab4f8] text-white dark:text-[#041e49] hover:brightness-110 disabled:opacity-50"
          >
            {saving ? '保存中…' : isEdit ? '保存' : '添加并测试连接'}
          </button>
        </footer>
      </div>
    </div>
  )
}

/** 测试连接结果弹窗：协议版本 + 工具清单 */
function McpToolsModal({ result, onClose }: { result: McpTestResult; onClose: () => void }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4" onClick={onClose}>
      <div
        className="w-full max-w-lg max-h-[75vh] overflow-y-auto scrollbar-thin rounded-2xl bg-white dark:bg-[#1e1f20] border border-[#e3e3e3] dark:border-[#3c4043] shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <header className="flex items-center justify-between px-5 py-3.5 border-b border-[#e3e3e3]/70 dark:border-[#28292a] sticky top-0 bg-white dark:bg-[#1e1f20]">
          <div className="flex items-center gap-2">
            {result.ok ? (
              <CheckCircle2 className="w-4 h-4 text-emerald-500" />
            ) : (
              <XCircle className="w-4 h-4 text-red-500" />
            )}
            <h2 className="text-sm font-medium text-[#1f1f1f] dark:text-[#f1f3f4]">
              {result.ok ? `连接成功 · ${result.tool_count} 个工具` : '连接失败'}
            </h2>
          </div>
          <button onClick={onClose} className="p-1 rounded hover:bg-neutral-100 dark:hover:bg-[#28292a]">
            <X className="w-4 h-4 text-[#5f6368] dark:text-[#9aa0a6]" />
          </button>
        </header>
        <div className="px-5 py-4 text-sm">
          {!result.ok ? (
            <p className="text-xs text-red-500 dark:text-red-400 break-all">{result.error}</p>
          ) : (
            <>
              <p className="text-[11px] text-[#747775] dark:text-[#9aa0a6] mb-3">
                服务器 {result.server_name || '未署名'} · 协议 {result.protocol_version || '未知'}；
                会话中启用后模型以 mcp_&lt;server&gt;_&lt;tool&gt; 名调用
              </p>
              <div className="space-y-2">
                {result.tools.map((t) => (
                  <div key={t.qualified} className="px-3 py-2 rounded-lg border border-[#e3e3e3]/70 dark:border-[#3c4043]/60">
                    <p className="text-xs font-medium text-[#1a73e8] dark:text-[#8ab4f8] font-mono break-all">
                      {t.qualified}
                    </p>
                    <p className="text-[11px] text-[#5f6368] dark:text-[#9aa0a6] mt-0.5 line-clamp-2">
                      {t.description || '无描述'}
                    </p>
                  </div>
                ))}
                {result.tools.length === 0 && (
                  <p className="text-xs text-[#747775] dark:text-[#9aa0a6]">
                    服务器未暴露任何工具（或全部被白名单过滤）
                  </p>
                )}
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
