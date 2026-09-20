import { Check, Clock, Zap } from 'lucide-react'
import type { TurnMetric } from '../types'

export function formatTokens(n: number): string {
  if (n < 1000) return String(n)
  if (n < 10000) return `${(n / 1000).toFixed(1)}k`
  return `${(n / 1000).toFixed(0)}k`
}

interface LiveTokenProgressProps {
  elapsedSec: string
  totalTokens: number
  cachedTokens?: number
  promptTokens?: number
}

/** 流式生成中的动态活性进度指示器（秒表累加 + Token 递增 + 缓存命中率） */
export function LiveTokenProgress({
  elapsedSec,
  totalTokens,
  cachedTokens = 0,
  promptTokens = 0,
}: LiveTokenProgressProps) {
  const hitRate =
    promptTokens > 0 && cachedTokens > 0
      ? Math.min(100, Math.round((cachedTokens / promptTokens) * 100))
      : 0

  return (
    <div className="inline-flex items-center gap-2 px-2.5 py-1 rounded-lg bg-[#f0f4f9] dark:bg-[#28292a] border border-[#e3e3e3] dark:border-[#3c4043] text-xs text-[#444746] dark:text-[#c4c7c5] select-none animate-fade-in">
      <span className="relative flex h-2 w-2">
        <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-[#1a73e8] opacity-75" />
        <span className="relative inline-flex rounded-full h-2 w-2 bg-[#1a73e8]" />
      </span>
      <span className="flex items-center gap-1 font-mono">
        <Clock className="w-3 h-3 text-[#747775] dark:text-[#9aa0a6]" />
        <span>{elapsedSec}s</span>
      </span>
      {totalTokens > 0 && (
        <>
          <span className="text-[#c4c7c5] dark:text-[#5f6368]">·</span>
          <span className="font-mono font-medium text-[#1f1f1f] dark:text-[#e3e3e3]">
            {formatTokens(totalTokens)} tokens
          </span>
        </>
      )}
      {hitRate > 0 && (
        <>
          <span className="text-[#c4c7c5] dark:text-[#5f6368]">·</span>
          <span className="flex items-center gap-0.5 text-emerald-600 dark:text-emerald-400 font-mono">
            <Zap className="w-3 h-3 fill-current" />
            <span>{hitRate}% 命中</span>
          </span>
        </>
      )}
    </div>
  )
}

interface CompletedTokenBadgeProps {
  metric: TurnMetric
}

/** 方案 B（平滑瞬态）：回答完成后在当前屏渲染的淡灰色弱化总结；刷新页面后自然不复存在 */
export function CompletedTokenBadge({ metric }: CompletedTokenBadgeProps) {
  const tooltip = [
    `总计消耗: ${metric.totalTokens.toLocaleString()} tokens`,
    `输入 (Prompt): ${metric.promptTokens.toLocaleString()}`,
    `输出 (Completion): ${metric.completionTokens.toLocaleString()}`,
    metric.cachedTokens > 0 ? `命中缓存 (Cached): ${metric.cachedTokens.toLocaleString()}` : '',
    metric.reasoningTokens > 0 ? `推理链 (Reasoning): ${metric.reasoningTokens.toLocaleString()}` : '',
  ]
    .filter(Boolean)
    .join('\n')

  return (
    <div
      className="inline-flex items-center gap-1 mt-2.5 text-xs text-[#747775] dark:text-[#9aa0a6] select-none transition-opacity cursor-default"
      title={tooltip}
    >
      <Check className="w-3.5 h-3.5 text-emerald-600 dark:text-emerald-400" />
      <span>本次耗时 {metric.durationSec}s</span>
      <span>
        ({formatTokens(metric.totalTokens)} tokens
        {metric.cacheHitRate > 0 && (
          <span className="text-emerald-600/90 dark:text-emerald-400/90">
            {' · '}⚡ {metric.cacheHitRate}% 命中
          </span>
        )}
        )
      </span>
    </div>
  )
}
