# Skill 接口契约与通信协议规范 (API Specification)

> **版本**：v1.2.0  
> **协议**：HTTP/1.1、HTTP/2、Server-Sent Events (SSE)  
> **认证方式**：`Authorization: Bearer <JWT_TOKEN>`  
> **核心代码实现**：[`backend/api/skills.go`](../../backend/api/skills.go)、[`backend/api/skill_files.go`](../../backend/api/skill_files.go)、[`backend/api/sessions.go`](../../backend/api/sessions.go)  
> **默认数据响应封套**：`{"code": 0, "data": ..., "msg": "success"}`（特定流式与裸数据接口除外）

---

## 1. 响应规范与统一封套

除文件下载（二进制流）和部分历史兼容接口外，所有 RESTful API 均遵循统一响应结构：

```json
{
  "code": 0,
  "data": { ... },
  "msg": "success"
}
```

- **`code = 0`**：业务处理成功。
- **`code != 0`**：业务异常，`msg` 包含前端可直接向用户展示的错误原因提示。

---

## 2. 技能库核心管理 API

### 2.1 获取技能列表
- **路径**：`GET /api/skills`
- **说明**：获取当前系统所有可用技能。服务端在响应前会自动触发一次文件系统扫描与数据表同步（`syncSkillRegistry`），确保磁盘新置技能秒级生效。
- **响应示例**：
```json
{
  "code": 0,
  "data": [
    {
      "id": 1,
      "name": "web-search",
      "description": "进行互联网实时搜索与信息汇总",
      "dir_name": "web-search",
      "tags": ["search", "web"],
      "created_at": "2026-09-18T10:00:00Z",
      "owner_id": 0,
      "owner_name": "System",
      "confidential": false,
      "model": null,
      "is_owner": false,
      "can_view": true
    },
    {
      "id": 2,
      "name": "enterprise-crm",
      "description": "企业客户关系数据库交互插件",
      "dir_name": "enterprise-crm",
      "tags": ["crm", "private"],
      "created_at": "2026-09-18T11:30:00Z",
      "owner_id": 1002,
      "owner_name": "张工",
      "confidential": true,
      "model": "gpt-4o",
      "is_owner": false,
      "can_view": false
    }
  ],
  "msg": "success"
}
```
- **关键派生字段**：
  - `is_owner` (`boolean`)：当前登录用户是否为该技能所有者。
  - `can_view` (`boolean`)：`!confidential || is_owner`。若为 `false`，前端禁止呈现代码编辑与下载入口，仅允许作为工具在对话中使用。

---

### 2.2 从空新建技能
- **路径**：`POST /api/skills`
- **请求体** (`application/json`)：
```json
{
  "name": "custom-calculator",
  "description": "执行科学计算与微积分运算"
}
```
- **说明**：在磁盘创建 `skills/custom-calculator/`，生成标准骨架 `SKILL.md`，并在版本表中记录 v1 版本。
- **响应**：同单体 `SkillOut` 实体。

---

### 2.3 上传 Zip 包安装/更新技能
- **路径**：`POST /api/skills/upload`
- **请求类型**：`multipart/form-data`
- **请求参数**：
  - `file` (`File`, 必填)：技能 Zip 压缩包（上限 30MB）。
  - `confidential` (`boolean`, 可选)：是否设定为机密技能。
- **特别注意**：本接口采用**裸 JSON 响应**（不经统一信封封装），便于兼容现有客户端上传流水线。
- **响应示例**：
```json
{
  "skill": {
    "id": 3,
    "name": "arxiv-search",
    "description": "搜索 Arxiv 论文并总结摘要",
    "dir_name": "arxiv-search",
    "tags": ["paper", "academic"],
    "created_at": "2026-09-18T14:00:00Z",
    "owner_id": 1001,
    "owner_name": "李工",
    "confidential": false,
    "is_owner": true,
    "can_view": true
  },
  "created_version_number": 1,
  "activated": true
}
```
- **处理逻辑**：
  1. 服务端先流式预检（`PeekZip`），从压缩包内的 `SKILL.md` 解析技能名称，并计算物理目录名。
  2. 若已存在同名且所有者非当前用户的技能，拒绝并报错：`"已存在同名技能「xxx」且不属于你，无法覆盖"`。
  3. 解压文件并覆盖落盘；若 Zip 包根目录附带 `.env` 机密配置，自动剥离归档至 `.secrets/<dir>.env`，防止代码泄露。
  4. 版本号递增并在 `skill_versions` 记录来源 `upload`。

---

### 2.4 下载技能 Zip 包
- **路径**：`GET /api/skills/:id/download`
- **鉴权要求**：机密技能仅所有者可下载。
- **响应**：
  - `Content-Type: application/zip`
  - `Content-Disposition: attachment; filename="<skill-name>.zip"`
  - 响应体：实时将 `skills/<dir_name>/` 下的当前工作副本打包输出的二进制流。

---

### 2.5 编辑技能属性
- **路径**：`PATCH /api/skills/:id`
- **权限**：仅所有者可操作。
- **请求体** (`application/json`)：
```json
{
  "confidential": true,
  "model": "claude-3-5-sonnet",
  "tags": ["finance", "audit"]
}
```
- **说明**：字段为可选。`model: ""` 表示清空模型指定，恢复继承系统默认；`tags: []` 表示清空标签。

---

### 2.6 删除技能
- **路径**：`DELETE /api/skills/:id`
- **权限**：仅所有者可操作；系统公共技能（`owner_id = 0`）禁止网页端删除。
- **处理逻辑**：
  1. 物理删除 `skills/<dir_name>` 目录及其所有文件。
  2. 物理删除 `.secrets/<dir_name>.env` 密钥文件。
  3. 数据库级联清理：`skills` 记录、`skill_versions` 记录、`session_skills` 会话关联记录。

---

## 3. 技能详情、正文与机密管理 API

### 3.1 获取技能详情正文与机密键概况
- **路径**：`GET /api/skills/:id/body`
- **权限**：公开技能全员可见；机密技能仅所有者可查看（非所有者请求将报错：`"机密技能仅所有者可预览"`）。
- **响应示例**：
```json
{
  "code": 0,
  "data": {
    "name": "web-search",
    "description": "进行互联网实时搜索与信息汇总",
    "body": "---\nname: web-search\n...\n---\n# 使用指南\n...",
    "has_secrets": true,
    "secret_keys": ["BRAVE_API_KEY", "SEARCH_TIMEOUT_SECONDS"]
  },
  "msg": "success"
}
```
- **安全说明**：`secret_keys` 仅对所有者返回键名列表；非所有者返回空数组，避免泄露内部用途。

---

### 3.2 读取技能机密具体明细
- **路径**：`GET /api/skills/:id/secrets`
- **说明**：仅所有者返回明细；非所有者返回空对象 `{}`（不报错，防止前端多端点并行加载打挂详情弹窗）。
- **响应示例**：
```json
{
  "code": 0,
  "data": {
    "BRAVE_API_KEY": "BSA_xxxxxxxxx",
    "SEARCH_TIMEOUT_SECONDS": "15"
  },
  "msg": "success"
}
```

---

### 3.3 全量替换/保存技能机密
- **路径**：`PUT /api/skills/:id/secrets`
- **权限**：仅所有者。
- **请求体** (`application/json`)：
```json
{
  "secrets": {
    "BRAVE_API_KEY": "BSA_new_key_value",
    "SEARCH_TIMEOUT_SECONDS": "20"
  }
}
```
- **语义**：整体覆盖语义（PUT）。提交的 Map 即为全部机密；删除某个键提交即代表删除该环境变量。

---

### 3.4 查询技能版本历史
- **路径**：`GET /api/skills/:id/versions`
- **响应示例**：
```json
{
  "code": 0,
  "data": [
    {
      "id": 12,
      "version_number": 2,
      "notes": "升级搜索解析规则并支持多源聚合",
      "created_at": "2026-09-18T15:20:00Z",
      "is_current": true,
      "created_by": 1001,
      "created_by_name": "李工",
      "source": "upload"
    },
    {
      "id": 5,
      "version_number": 1,
      "notes": "初始创建",
      "created_at": "2026-09-18T14:00:00Z",
      "is_current": false,
      "created_by": 1001,
      "created_by_name": "李工",
      "source": "create"
    }
  ],
  "msg": "success"
}
```

---

## 4. 技能工作副本在线文件系统 API

### 4.1 获取文件树清单
- **路径**：`GET /api/skills/:id/tree`
- **响应示例**：
```json
{
  "code": 0,
  "data": [
    { "path": "SKILL.md", "size": 1024, "is_binary": false },
    { "path": "scripts/search.py", "size": 3520, "is_binary": false },
    { "path": "assets/logo.png", "size": 18240, "is_binary": true }
  ],
  "msg": "success"
}
```

### 4.2 读取文件内容
- **路径**：`GET /api/skills/:id/file?path=scripts/search.py`
- **响应示例**：
```json
{
  "code": 0,
  "data": {
    "path": "scripts/search.py",
    "content": "import sys\nimport os\n...",
    "is_binary": false
  },
  "msg": "success"
}
```

### 4.3 写入/更新文件
- **路径**：`PUT /api/skills/:id/file?path=scripts/search.py`
- **请求体** (`application/json`)：
```json
{
  "content": "import sys\n# updated\n..."
}
```

### 4.4 新建文件
- **路径**：`POST /api/skills/:id/file?path=scripts/utils.py`
- **请求体** (`application/json`)：
```json
{
  "content": "def helper(): pass\n"
}
```
- **注意**：若文件已存在，返回错误 `400` 防误覆盖。

### 4.5 删除文件
- **路径**：`DELETE /api/skills/:id/file?path=scripts/temp.py`
- **保护限制**：根目录下的 `SKILL.md` 是技能核心声明文件，受系统物理保护，禁止通过接口删除。

### 4.6 重命名 / 移动文件
- **路径**：`POST /api/skills/:id/file/rename`
- **请求体** (`application/json`)：
```json
{
  "old_path": "scripts/legacy_calc.py",
  "new_path": "scripts/calc.py"
}
```

---

## 5. 会话级技能绑定与实时流式事件

### 5.1 设置会话启用的技能清单
- **路径**：`PUT /api/sessions/:id/skills`
- **说明**：全量替换会话当前的技能绑定配置。
- **请求体** (`application/json`)：
```json
{
  "skills": [
    {
      "skill_id": 1,
      "pinned_version_number": null,
      "source": "full"
    },
    {
      "skill_id": 4,
      "pinned_version_number": null,
      "source": "auto"
    }
  ]
}
```
- **参数校验**：
  - `source` 可选值：`"full"` 或 `"auto"`。若为空则缺省为 `"auto"`。
  - 服务端会对 `skill_id` 集合执行批量存在性校验，若存在已失效的 ID 则整体回滚并返回错误提示。

---

### 5.2 SSE 流式传输中的技能动态装载事件 (`skill_loaded`)

在长连接对话（`POST /api/sessions/:id/messages`）中，当 LLM 发起 `load_skill` 工具调用并且后端装载物化成功后，后端会实时向客户端推送专属事件：

#### SSE 消息帧规范
```text
event: skill_loaded
data: {"type":"skill_loaded","id":"call_9a8b7c6d","name":"web-search","skill_id":1}
```

#### 字段含义
- **`type`**：恒为 `"skill_loaded"`。
- **`id`**：发起该次调用的 LLM ToolCall ID。
- **`name`**：加载的技能名称（如 `web-search`）。
- **`skill_id`**：技能在系统数据库中的主键 ID。

#### 前端消费规范
前端监听到该事件后：
1. 立即将该技能追加到当前会话已启用列表中（标记为 `auto_loaded` 状态）。
2. 在输入栏上方的 Activated Skills 标签栏中实时点亮该 Skill Chip，给予用户即时视觉反馈。
