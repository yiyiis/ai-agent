import { useEffect, useState } from 'react'
import {
  BrowserRouter,
  Routes,
  Route,
  useNavigate,
  useLocation,
  useParams,
} from 'react-router-dom'
import { ChatArea } from './components/ChatArea'
import { LoginGate } from './components/LoginGate'
import { Sidebar } from './components/Sidebar'
import { MemoriesPanel } from './components/MemoriesPanel'
import { SkillsPlaza } from './components/SkillsPlaza'
import { BrandIcon } from './components/BrandIcons'
import { Toast } from './components/Toast'
import { api } from './api/client'
import type { Session } from './types'

export default function App() {
  return (
    <BrowserRouter>
      <LoginGate>
        {(identity) => (
          <AppInner
            userName={identity.name}
            userId={identity.user_id}
            onLogout={identity.onLogout}
          />
        )}
      </LoginGate>
    </BrowserRouter>
  )
}

function AppInner({
  userName,
  userId,
  onLogout,
}: {
  userName: string
  userId: number
  onLogout: () => void
}) {
  const navigate = useNavigate()
  const location = useLocation()
  const [sessions, setSessions] = useState<Session[]>([])
  const [initLoading, setInitLoading] = useState(true)
  const [toastMsg, setToastMsg] = useState<{ title: string; variant?: 'error' | 'info' | 'success' } | null>(null)

  // 从当前路径解析视图类型与当前活跃会话 ID
  const activeId = location.pathname.startsWith('/c/')
    ? location.pathname.slice(3)
    : null
  const view: 'chat' | 'plaza' | 'memories' =
    location.pathname === '/plaza'
      ? 'plaza'
      : location.pathname === '/memories'
        ? 'memories'
        : 'chat'

  const refresh = async () => {
    try {
      const list = await api.listSessions()
      setSessions(list)
      return list
    } catch (e) {
      console.error('加载会话列表失败', e)
      return []
    }
  }

  // 初次加载会话列表
  useEffect(() => {
    refresh().finally(() => {
      setInitLoading(false)
    })
  }, [])

  // 发起新对话（惰性创建：直接跳转到新对话草稿态 /，未真正对话前不创建会话，不占用左侧列表）
  const handleCreate = () => {
    navigate('/')
  }

  const handleDelete = async (id: string) => {
    try {
      await api.deleteSession(id)
      await refresh()
      if (activeId === id) {
        navigate('/')
      }
    } catch (e) {
      setToastMsg({ title: `删除会话失败: ${String(e)}`, variant: 'error' })
    }
  }

  const handleRename = async (id: string, title: string) => {
    try {
      await api.updateSession(id, { title })
      await refresh()
    } catch (e) {
      setToastMsg({ title: `重命名失败: ${String(e)}`, variant: 'error' })
    }
  }

  return (
    <div className="flex h-screen bg-white dark:bg-[#131314] text-[#1f1f1f] dark:text-[#e3e3e3] overflow-hidden font-sans">
      {toastMsg && (
        <Toast
          title={toastMsg.title}
          variant={toastMsg.variant ?? 'info'}
          onClose={() => setToastMsg(null)}
        />
      )}
      <Sidebar
        sessions={sessions}
        activeId={activeId}
        view={view}
        onSelect={(id) => navigate(`/c/${id}`)}
        onCreate={handleCreate}
        onDelete={handleDelete}
        onRename={handleRename}
        onOpenPlaza={() => navigate('/plaza')}
        onOpenMemories={() => navigate('/memories')}
        userName={userName}
        userId={userId}
        onLogout={onLogout}
      />
      <main className="flex-1 flex flex-col overflow-hidden bg-white dark:bg-[#131314] relative">
        <Routes>
          <Route path="/plaza" element={<SkillsPlaza />} />
          <Route path="/memories" element={<MemoriesPanel />} />
          <Route
            path="/c/:sessionId"
            element={
              <SessionRouteWrapper
                onTitleChange={refresh}
                userName={userName}
                onSessionNotFound={(err) => {
                  setToastMsg({ title: err, variant: 'error' })
                  navigate('/', { replace: true })
                }}
              />
            }
          />
          <Route
            path="/"
            element={
              initLoading ? (
                <div className="flex-1 flex flex-col items-center justify-center bg-white dark:bg-[#131314]">
                  <div className="w-12 h-12 rounded-2xl bg-[#f0f4f9] dark:bg-[#1e1f20] border border-[#e3e3e3] dark:border-[#3c4043] flex items-center justify-center animate-pulse shadow-sm">
                    <BrandIcon className="w-6 h-6 text-[#1a73e8]" />
                  </div>
                </div>
              ) : (
                <SessionRouteWrapper
                  onTitleChange={refresh}
                  userName={userName}
                  onSessionNotFound={(err) => {
                    setToastMsg({ title: err, variant: 'error' })
                  }}
                />
              )
            }
          />
          <Route
            path="*"
            element={
              <SessionRouteWrapper
                onTitleChange={refresh}
                userName={userName}
                onSessionNotFound={(err) => {
                  setToastMsg({ title: err, variant: 'error' })
                }}
              />
            }
          />
        </Routes>
      </main>
    </div>
  )
}

function SessionRouteWrapper({
  onTitleChange,
  userName,
  onSessionNotFound,
}: {
  onTitleChange: () => void
  userName?: string
  onSessionNotFound: (err: string) => void
}) {
  const { sessionId } = useParams<{ sessionId?: string }>()
  const navigate = useNavigate()

  return (
    <ChatArea
      sessionId={sessionId ?? null}
      onTitleChange={onTitleChange}
      userName={userName}
      onSessionNotFound={onSessionNotFound}
      onSessionCreated={(newId) => {
        navigate(`/c/${newId}`, { replace: true })
        onTitleChange()
      }}
    />
  )
}

