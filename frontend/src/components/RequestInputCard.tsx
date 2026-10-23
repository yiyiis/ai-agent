import { createContext, useContext, useState } from 'react'
import type { ToolInvocation } from '../types'
import { parseInputQuestion, parseInputResponse } from '../lib/requestInput'

export const InputActionsContext = createContext<{
  respond: (inv: ToolInvocation, action: 'answer' | 'skip', answer: string) => Promise<void>
  stop: () => Promise<void>
} | null>(null)

export function RequestInputCard({ invocation }: { invocation: ToolInvocation }) {
  const actions = useContext(InputActionsContext)
  const [answer, setAnswer] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const question = parseInputQuestion(invocation.arguments)
  const response = parseInputResponse(invocation.output)
  const waiting = invocation.status === 'waiting' && !!invocation.inputMessageId

  async function submit(action: 'answer' | 'skip' | 'stop') {
    if (!actions || busy) return
    setBusy(true)
    setError('')
    try {
      if (action === 'stop') await actions.stop()
      else await actions.respond(invocation, action, answer)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section aria-label="用户输入" className="rounded-2xl border border-blue-200 dark:border-blue-800 bg-blue-50/60 dark:bg-blue-950/20 p-4 space-y-3 text-sm">
      <div className="font-medium text-blue-700 dark:text-blue-300">
        {waiting ? '等待你的回答' : response?.status === 'answered' ? '已回答' : response?.action === 'stop' ? '任务已停止' : response?.status === 'cancelled' ? '已跳过问题' : '正在准备问题'}
      </div>
      <p className="whitespace-pre-wrap break-words">{question?.question || '正在接收问题…'}</p>
      {waiting && actions && (
        <form onSubmit={(e) => { e.preventDefault(); void submit('answer') }} className="space-y-3">
          {!!question?.options?.length && (
            <div className="flex flex-wrap gap-2" role="group" aria-label="建议选项">
              {question.options.map((option, index) => (
                <button key={index} type="button" disabled={busy} aria-pressed={answer === option}
                  onClick={() => setAnswer(option)}
                  className={`rounded-lg border px-3 py-2 text-left ${answer === option ? 'border-blue-600 bg-blue-100 dark:bg-blue-900' : 'border-gray-300 dark:border-gray-600'}`}>
                  {option}
                </button>
              ))}
            </div>
          )}
          <textarea aria-label="你的回答" placeholder="选择上方选项，或输入你的回答"
            value={answer} onChange={(e) => setAnswer(e.target.value)} disabled={busy}
            maxLength={4000} rows={3}
            className="w-full rounded-lg border border-gray-300 dark:border-gray-600 bg-white dark:bg-[#1e1f20] p-3 resize-y" />
          <div className="flex flex-wrap items-center gap-3">
            <button type="submit" disabled={busy || !answer.trim()}
              className="rounded-lg bg-blue-600 px-4 py-2 text-white disabled:opacity-50">提交并继续</button>
            <button type="button" disabled={busy} onClick={() => void submit('skip')}
              className="rounded-lg border border-gray-300 dark:border-gray-600 px-3 py-2">跳过这个问题</button>
            <button type="button" disabled={busy} onClick={() => void submit('stop')}
              className="text-red-600 dark:text-red-400 px-2 py-2">停止整个任务</button>
          </div>
          <p className="text-xs text-gray-500">跳过会让助手在没有这项答案的情况下继续；停止会结束整个任务。</p>
        </form>
      )}
      {response?.answer && <p className="whitespace-pre-wrap break-words"><span className="font-medium">你的回答：</span>{response.answer}</p>}
      {error && <p role="alert" className="text-red-600 dark:text-red-400">{error}</p>}
    </section>
  )
}
