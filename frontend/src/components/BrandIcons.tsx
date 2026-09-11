/**
 * 自主设计的 AI Agent 官方专属品牌徽标：AgentNexus
 * 采用多维智慧节点与轨道拓扑几何结构，兼具现代科技感与极简优雅
 */
export function BrandIcon({ className = 'w-5 h-5' }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 32 32"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
    >
      <defs>
        <linearGradient id="brand-nexus-grad" x1="4" y1="4" x2="28" y2="28" gradientUnits="userSpaceOnUse">
          <stop offset="0%" stopColor="#1a73e8" />
          <stop offset="60%" stopColor="#4285f4" />
          <stop offset="100%" stopColor="#1557b0" />
        </linearGradient>
        <linearGradient id="brand-core-grad" x1="12" y1="12" x2="20" y2="20" gradientUnits="userSpaceOnUse">
          <stop offset="0%" stopColor="#ffffff" />
          <stop offset="100%" stopColor="#d3e3fd" />
        </linearGradient>
      </defs>

      {/* 外圈科技轨道微环 */}
      <circle
        cx="16"
        cy="16"
        r="14"
        stroke="url(#brand-nexus-grad)"
        strokeWidth="2"
        strokeLinecap="round"
        strokeDasharray="16 5 28 5"
        className="opacity-75"
      />

      {/* 核心多维智能微晶拓扑 */}
      <path
        d="M16 6.5C14.8 11.2 11.2 14.8 6.5 16C11.2 17.2 14.8 20.8 16 25.5C17.2 20.8 20.8 17.2 25.5 16C20.8 14.8 17.2 11.2 16 6.5Z"
        fill="url(#brand-nexus-grad)"
      />

      {/* 中心能量核心 */}
      <circle cx="16" cy="16" r="3.2" fill="url(#brand-core-grad)" />
      <circle cx="16" cy="16" r="1.4" fill="#1a73e8" />
    </svg>
  )
}

/** 侧边栏折叠/展开图标 */
export function SidebarToggleIcon({ className = 'w-4 h-4' }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <rect width="18" height="18" x="3" y="3" rx="2" />
      <path d="M9 3v18" />
    </svg>
  )
}

// 保持向下兼容别名
export const GeminiStar = BrandIcon
export const GeminiSidebarToggleIcon = SidebarToggleIcon
