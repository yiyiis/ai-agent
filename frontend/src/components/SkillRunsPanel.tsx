import { useEffect, useState } from 'react'
import { AlertCircle, ChevronDown, ChevronRight, Loader2, RotateCw } from 'lucide-react'
import { api } from '../api/client'
import type { SkillRunDetail, SkillRunStats, SkillRunSummary } from '../types'
import { cn } from '../lib/utils'

interface Props {
  skillId: number
}

const WINDOWS = [
  { days: 1, label: '24 小时' },
  { days: 7, label: '7 天' },
  { days: 30, label: '30 天' },
] as const

/** 某个 skill 的 API 调用历史：概览 + 按天曲线 + 明细。 */
export function SkillRunsPanel({ skillId }: Props) {
  const [days, setDays] = useState<number>(7)
  const [onlyFailed, setOnlyFailed] = useState(false)
  const [stats, setStats] = useState<SkillRunStats | null>(null)
  const [runs, setRuns] = useState<SkillRunSummary[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let alive = true
    setStats(null)
    setRuns(null)
    setError(null)
    Promise.all([
      api.getSkillRunStats(skillId, days),
      api.listSkillRuns(skillId, {
        days,
        limit: 100,
        status: onlyFailed ? 'failed' : undefined,
      }),
    ])
      .then(([s, r]) => {
        if (!alive) return
        setStats(s)
        setRuns(r)
      })
      .catch((e) => alive && setError(String(e)))
    return () => {
      alive = false
    }
  }, [skillId, days, onlyFailed])

  return (
    <div className="space-y-4 text-[#1f1f1f] dark:text-[#f1f3f4]">
      <div className="flex items-center gap-2">
        {WINDOWS.map((w) => (
          <button
            key={w.days}
            onClick={() => setDays(w.days)}
            className={cn(
              'px-2.5 py-1 rounded-full text-xs border transition',
              days === w.days
                ? 'bg-neutral-900 text-white border-neutral-900 dark:bg-white dark:text-neutral-900 dark:border-white'
                : 'bg-white dark:bg-[#28292a] text-neutral-600 dark:text-[#c4c7c5] border-neutral-200 dark:border-[#3c4043] hover:border-neutral-400 dark:hover:border-[#5e6368]',
            )}
          >
            {w.label}
          </button>
        ))}
        <div className="flex-1" />
        <label className="flex items-center gap-1.5 text-xs text-neutral-600 dark:text-[#c4c7c5] cursor-pointer">
          <input
            type="checkbox"
            checked={onlyFailed}
            onChange={(e) => setOnlyFailed(e.target.checked)}
          />
          只看失败
        </label>
      </div>

      {error && (
        <div className="px-3 py-2 bg-red-50 dark:bg-red-950/30 text-red-600 dark:text-red-400 text-xs rounded border border-red-100 dark:border-red-900">
          {error}
        </div>
      )}

      {!stats && !error && (
        <div className="text-xs text-neutral-400 dark:text-[#747775] flex items-center gap-1 py-6 justify-center">
          <Loader2 className="w-3 h-3 animate-spin" />
          加载中…
        </div>
      )}

      {stats && (
        <>
          <div className="grid grid-cols-4 gap-2">
            <Tile label="调用次数" value={String(stats.total)} />
            <Tile
              label="成功率"
              value={
                stats.success_rate == null
                  ? '—'
                  : `${Math.round(stats.success_rate * 100)}%`
              }
              tone={
                stats.success_rate != null && stats.success_rate < 0.9
                  ? 'warn'
                  : undefined
              }
            />
            <Tile label="P50 耗时" value={fmtMs(stats.p50_ms)} />
            <Tile label="P95 耗时" value={fmtMs(stats.p95_ms)} />
          </div>

          {stats.running > 0 && (
            <div className="text-xs text-neutral-500 dark:text-[#9aa0a6]">
              还有 {stats.running} 次在执行中
            </div>
          )}

          {stats.total === 0 ? (
            <div className="text-sm text-neutral-400 dark:text-[#747775] text-center py-10">
              这段时间没有 API 调用记录
            </div>
          ) : (
            <DailyChart stats={stats} />
          )}
        </>
      )}

      {runs && runs.length > 0 && (
        <div className="border border-neutral-200 dark:border-[#3c4043] rounded-md divide-y divide-neutral-100 dark:divide-[#28292a]">
          {runs.map((r) => (
            <RunRow key={r.id} skillId={skillId} run={r} />
          ))}
        </div>
      )}
      {runs && runs.length === 0 && stats && stats.total > 0 && (
        <div className="text-xs text-neutral-400 dark:text-[#747775] text-center py-4">
          没有符合筛选条件的记录
        </div>
      )}
    </div>
  )
}

function Tile({
  label,
  value,
  tone,
}: {
  label: string
  value: string
  tone?: 'warn'
}) {
  return (
    <div className="border border-neutral-200 dark:border-[#3c4043] rounded-md px-3 py-2 bg-white dark:bg-[#1e1f20]">
      <div className="text-[11px] text-neutral-500 dark:text-[#9aa0a6]">{label}</div>
      <div
        className={cn(
          'text-lg font-medium tabular-nums mt-0.5 text-[#1f1f1f] dark:text-[#f1f3f4]',
          tone === 'warn' && 'text-amber-600 dark:text-amber-400',
        )}
      >
        {value}
      </div>
    </div>
  )
}

/** 纯 CSS 柱状图——为了一个小图表引整套图表库不划算 */
function DailyChart({ stats }: { stats: SkillRunStats }) {
  const max = Math.max(1, ...stats.daily.map((d) => d.total))
  return (
    <div>
      <div className="flex items-end gap-1 h-24">
        {stats.daily.map((d) => {
          const ok = d.total - d.failed
          return (
            <div
              key={d.day}
              className="flex-1 flex flex-col justify-end gap-px min-w-[6px]"
              title={`${d.day}：${d.total} 次${d.failed ? `，失败 ${d.failed}` : ''}`}
            >
              {d.failed > 0 && (
                <div
                  className="bg-red-400 rounded-t-sm"
                  style={{ height: `${(d.failed / max) * 100}%` }}
                />
              )}
              <div
                className={cn(
                  'bg-neutral-800 dark:bg-[#8ab4f8]',
                  d.failed === 0 && 'rounded-t-sm',
                )}
                style={{ height: `${(ok / max) * 100}%` }}
              />
            </div>
          )
        })}
      </div>
      <div className="flex justify-between text-[10px] text-neutral-400 dark:text-[#9aa0a6] mt-1">
        <span>{stats.daily[0]?.day ?? ''}</span>
        <span>{stats.daily[stats.daily.length - 1]?.day ?? ''}</span>
      </div>
    </div>
  )
}

function RunRow({ skillId, run }: { skillId: number; run: SkillRunSummary }) {
  const [open, setOpen] = useState(false)
  const [detail, setDetail] = useState<SkillRunDetail | null>(null)
  const [detailErr, setDetailErr] = useState<string | null>(null)
  const [retrying, setRetrying] = useState(false)
  const [retryErr, setRetryErr] = useState<string | null>(null)

  // 重试后行本身被重置了（同一个 run_id），所以列表里那份 summary 已经过期。
  // 用详情里的状态覆盖显示，避免出现"徽章还是失败、详情已经在跑"的错位。
  const status = detail?.status ?? run.status
  const attempt = detail?.attempt ?? run.attempt

  const toggle = () => {
    const next = !open
    setOpen(next)
    if (next && !detail && !detailErr) {
      api
        .getSkillRunDetail(skillId, run.id)
        .then(setDetail)
        .catch((e) => setDetailErr(String(e)))
    }
  }

  const retry = async () => {
    setRetrying(true)
    setRetryErr(null)
    try {
      setDetail(await api.retrySkillRun(skillId, run.id))
    } catch (e) {
      setRetryErr(String(e))
    } finally {
      setRetrying(false)
    }
  }

  return (
    <div>
      <div
        onClick={toggle}
        className="flex items-center gap-2 px-3 py-2 text-xs cursor-pointer hover:bg-neutral-50 dark:hover:bg-[#28292a]"
      >
        {open ? (
          <ChevronDown className="w-3 h-3 text-neutral-400 dark:text-[#9aa0a6] shrink-0" />
        ) : (
          <ChevronRight className="w-3 h-3 text-neutral-400 dark:text-[#9aa0a6] shrink-0" />
        )}
        <StatusBadge status={status} />
        {attempt > 1 && (
          <span
            title={`这条调用已经跑了 ${attempt} 次`}
            className="px-1 py-0.5 rounded text-[10px] shrink-0 bg-amber-100 dark:bg-amber-950/40 text-amber-700 dark:text-amber-300 tabular-nums"
          >
            第 {attempt} 次
          </span>
        )}
        <span className="text-neutral-500 dark:text-[#9aa0a6] tabular-nums w-32 shrink-0">
          {new Date(run.created_at).toLocaleString()}
        </span>
        <span className="text-neutral-500 dark:text-[#9aa0a6] tabular-nums w-16 shrink-0">
          {fmtMs(detail?.duration_ms ?? run.duration_ms)}
        </span>
        <span className="text-red-600 dark:text-red-400 truncate flex-1 min-w-0">
          {(detail ? detail.error : run.error) ?? ''}
        </span>
        <span className="font-mono text-neutral-300 dark:text-[#5e6368] shrink-0">
          {run.id.slice(0, 8)}
        </span>
      </div>

      {open && (
        <div className="px-3 pb-3 pl-8 text-xs space-y-2">
          {!detail && !detailErr && (
            <div className="text-neutral-400 dark:text-[#747775] flex items-center gap-1">
              <Loader2 className="w-3 h-3 animate-spin" />
              加载中…
            </div>
          )}
          {detailErr && (
            <div className="flex items-start gap-1.5 px-2 py-1.5 bg-neutral-50 dark:bg-[#28292a] border border-neutral-200 dark:border-[#3c4043] rounded text-neutral-500 dark:text-[#9aa0a6]">
              <AlertCircle className="w-3 h-3 shrink-0 mt-px" />
              <span>{detailErr}</span>
            </div>
          )}
          {detail && (
            <>
              {detail.error && (
                <Block title="错误">
                  <pre className="whitespace-pre-wrap break-all text-red-700 dark:text-red-400">
                    {detail.error}
                  </pre>
                </Block>
              )}

              {/* retryable 是后端算好的，前端不推规则 */}
              {detail.retryable && (
                <div className="flex items-center gap-2">
                  <button
                    onClick={retry}
                    disabled={retrying}
                    className="flex items-center gap-1 px-2 py-1 rounded border border-neutral-300 dark:border-[#3c4043] text-neutral-700 dark:text-[#c4c7c5] hover:bg-neutral-100 dark:hover:bg-[#28292a] disabled:opacity-50 disabled:cursor-not-allowed focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-sky-500"
                  >
                    {retrying ? (
                      <Loader2 className="w-3 h-3 animate-spin" />
                    ) : (
                      <RotateCw className="w-3 h-3" />
                    )}
                    {retrying ? '正在重跑…' : '重试'}
                  </button>
                  {detail.failure_kind && (
                    <span className="text-neutral-400 dark:text-[#9aa0a6]">
                      失败类型：{failureKindLabel(detail.failure_kind)}
                    </span>
                  )}
                </div>
              )}
              {retryErr && (
                <div className="flex items-start gap-1.5 px-2 py-1.5 bg-red-50 dark:bg-red-950/30 border border-red-200 dark:border-red-900 rounded text-red-700 dark:text-red-400">
                  <AlertCircle className="w-3 h-3 shrink-0 mt-px" />
                  <span className="break-all">{retryErr}</span>
                </div>
              )}

              {detail.attempts.length > 0 && (
                <Block title={`历史尝试（${detail.attempts.length} 次）`}>
                  <div className="space-y-1">
                    {detail.attempts.map((a) => (
                      <div key={a.attempt} className="flex gap-2">
                        <span className="text-neutral-400 dark:text-[#9aa0a6] shrink-0 tabular-nums">
                          第 {a.attempt} 次
                        </span>
                        <span className="text-neutral-400 dark:text-[#9aa0a6] shrink-0">
                          {failureKindLabel(a.failure_kind)}
                        </span>
                        <span className="text-red-600 dark:text-red-400 break-all">
                          {a.error ?? ''}
                        </span>
                      </div>
                    ))}
                  </div>
                </Block>
              )}
              <Block title="入参">
                <pre className="whitespace-pre-wrap break-all">
                  {JSON.stringify(detail.params, null, 2)}
                </pre>
              </Block>
              {detail.progress.length > 0 && (
                <Block title={`执行过程（${detail.progress.length} 条）`}>
                  <div className="space-y-1">
                    {detail.progress.map((p, i) => (
                      <div key={i} className="flex gap-2">
                        <span className="text-neutral-400 dark:text-[#9aa0a6] shrink-0 font-mono">
                          {String(p.phase ?? '')}
                        </span>
                        <span className="text-neutral-600 dark:text-[#c4c7c5] break-all">
                          {String(p.message ?? JSON.stringify(p))}
                        </span>
                      </div>
                    ))}
                  </div>
                </Block>
              )}
              {detail.stderr_tail && (
                <Block title="stderr（尾段）">
                  <pre className="whitespace-pre-wrap break-all text-neutral-600 dark:text-[#c4c7c5]">
                    {detail.stderr_tail}
                  </pre>
                </Block>
              )}
            </>
          )}
        </div>
      )}
    </div>
  )
}

function Block({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="text-[11px] text-neutral-500 dark:text-[#9aa0a6] mb-1">{title}</div>
      <div className="px-2 py-1.5 bg-neutral-50 dark:bg-[#131314] border border-neutral-200 dark:border-[#3c4043] text-[#1f1f1f] dark:text-[#f1f3f4] rounded max-h-56 overflow-auto scrollbar-thin font-mono text-[11px]">
        {children}
      </div>
    </div>
  )
}

/** 失败分类 → 中文。开放枚举，认不出的原样显示（后端随时可能加新分类）。 */
function failureKindLabel(kind: string | null): string {
  if (!kind) return '未分类'
  const map: Record<string, string> = {
    sandbox: '沙箱未就绪',
    interrupted: '服务重启中断',
    timeout: '超时',
    script_error: '脚本报错',
    llm_error: '模型报错',
    param_invalid: '入参不合法',
    canceled: '已取消',
    unknown: '未知错误',
  }
  return map[kind] ?? kind
}

function StatusBadge({ status }: { status: string }) {
  const map: Record<string, { label: string; cls: string }> = {
    succeeded: { label: '成功', cls: 'bg-emerald-100 dark:bg-emerald-950/40 text-emerald-700 dark:text-emerald-300' },
    failed: { label: '失败', cls: 'bg-red-100 dark:bg-red-950/40 text-red-700 dark:text-red-300' },
    canceled: { label: '取消', cls: 'bg-neutral-200 dark:bg-[#28292a] text-neutral-600 dark:text-[#c4c7c5]' },
    running: { label: '执行中', cls: 'bg-sky-100 dark:bg-sky-950/40 text-sky-700 dark:text-sky-300' },
    queued: { label: '排队', cls: 'bg-neutral-100 dark:bg-[#28292a] text-neutral-500 dark:text-[#9aa0a6]' },
    // 失败了、还有预算、在等下一次自动重试
    retry_pending: { label: '待重试', cls: 'bg-amber-100 dark:bg-amber-950/40 text-amber-700 dark:text-amber-300' },
    needs_input: { label: '待补充', cls: 'bg-blue-100 dark:bg-blue-950/40 text-blue-700 dark:text-blue-300' },
  }
  const m = map[status] ?? { label: status, cls: 'bg-neutral-100 dark:bg-[#28292a] text-neutral-600 dark:text-[#c4c7c5]' }
  return (
    <span
      className={cn('px-1.5 py-0.5 rounded text-[10px] shrink-0 w-12 text-center', m.cls)}
    >
      {m.label}
    </span>
  )
}

function fmtMs(ms: number | null): string {
  if (ms == null) return '—'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / 60_000)}m${Math.round((ms % 60_000) / 1000)}s`
}
