// Package sandbox 多驱动执行沙箱（阶段四核心）。
//
// 统一模型：**所有工具调用都发生在沙箱里**——文件读写与进程执行全部经
// Driver 接口操作沙箱内的工作区，三种驱动语义完全一致：
//   - local：本地隔离执行——沙箱即宿主，文件操作退化为 os 调用，
//     进程以会话工作区为 cwd 跑在宿主机上，超时整棵进程树击杀；
//   - docker：每会话独立容器——文件经 Engine /archive API（tar 流）传入传出，
//     不 bind 挂载宿主目录，按需拉起、环境隔离注入、只读挂载与超期强制销毁；
//   - e2b：E2B 云端沙箱——文件经 envd /files REST 传入传出，按会话在云端
//     拉起隔离 Linux 虚机。
//
// 持久化（ephemeral 工作区）：沙箱生灭工作区随之消失，后端不留任何文件状态。
// 持久性由外部存储兜底——附件原件与导出产物在对象存储（COS），链接元数据在
// messages 表；agent 层每轮开始把会话全部附件重新物化进沙箱（含新建的空沙箱），
// 因此"用户上传过的原件"永不丢，只有未 export 的中间态随沙箱回收消失。
// 依赖方向保持 tools → sandbox 单向。
package sandbox

import (
	"context"
	"fmt"
	"runtime"

	"backend/pkg/errors"
)

// Result 单次进程执行结果。Err 表达基础设施级失败（沙箱不可达 / 容器拉起失败等），
// 与命令自身失败（非零退出码）不同：这类错误应让模型停止重试而不是换一种命令继续撞墙。
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Killed   bool
	Err      error
}

// Driver 沙箱驱动。统一模型：**工作区文件活在沙箱里**，所有工具（文件与进程）
// 都经驱动读写沙箱；工作区 ephemeral——沙箱销毁即消失，无宿主镜像。三种驱动
// 的语义因此完全一致：
//   - local：沙箱即宿主，文件操作退化为直接 os 调用（零开销）
//   - docker：文件经 Engine /archive API（tar 流）传入传出容器，不做 bind 挂载
//   - e2b：文件经 envd /files REST 传入传出云端沙箱
type Driver interface {
	Name() string
	// EnvNote 运行环境契约说明，注入 bash 工具描述，让模型按真实环境写命令
	EnvNote() string
	// RunBash 在会话工作区内执行一条 bash 命令，timeoutSecs 为命令级超时
	RunBash(ctx context.Context, sessionID, hostDir, command string, timeoutSecs int) Result
	// RunPython 执行工作区内的一段 Python 脚本，scriptRel 为工作区相对路径
	RunPython(ctx context.Context, sessionID, hostDir, scriptRel string, timeoutSecs int) Result
	// ReadFile 读取工作区内相对路径的文件（沙箱内的实时内容）
	ReadFile(ctx context.Context, sessionID, hostDir, rel string) ([]byte, error)
	// WriteFile 写入工作区内相对路径的文件（沙箱与宿主镜像双落）
	WriteFile(ctx context.Context, sessionID, hostDir, rel string, data []byte) error
	// RemoveFile 删除工作区内相对路径的文件（临时脚本清理用）
	RemoveFile(sessionID, hostDir, rel string)
	// Close 销毁该会话的沙箱资源（本地驱动空操作；其余驱动强制销毁）
	Close(sessionID string)
}

// Config 沙箱配置段（包自持配置、由 config 包组合，yaml 键与 etc/config.yaml 对齐）
type Config struct {
	Driver      string   `yaml:"Driver" mapstructure:"Driver"`       // local | docker | e2b
	DockerURL   string   `yaml:"DockerURL" mapstructure:"DockerURL"` // tcp:// | unix:// | npipe://；空则按平台默认
	Image       string   `yaml:"Image" mapstructure:"Image"`         // docker 沙箱镜像，需含 bash 与 python3
	Env         []string `yaml:"Env" mapstructure:"Env"`             // 注入容器的环境变量（KEY=VALUE）
	Mounts      []string `yaml:"Mounts" mapstructure:"Mounts"`       // docker 额外挂载 host:container[:ro]
	NetworkMode string   `yaml:"NetworkMode" mapstructure:"NetworkMode"`
	// IdleTimeoutSec / MaxLifetimeSec docker 容器空闲多久 / 最长存活多久后强制销毁（秒，<=0 用默认值）
	IdleTimeoutSec int `yaml:"IdleTimeoutSec" mapstructure:"IdleTimeoutSec"`
	MaxLifetimeSec int `yaml:"MaxLifetimeSec" mapstructure:"MaxLifetimeSec"`
	// WorkspaceHostDir 已移除：docker 驱动不再 bind 挂载宿主目录（文件经
	// /archive API 传输，与 e2b 模型统一），宿主/沙箱路径二元性不复存在
	// MemoryMB / CPUS / PidsLimit 沙箱容器资源限额（防失控脚本打爆宿主；0 为不限制）
	MemoryMB  int64   `yaml:"MemoryMB" mapstructure:"MemoryMB"`
	CPUS      float64 `yaml:"CPUS" mapstructure:"CPUS"`
	PidsLimit int64   `yaml:"PidsLimit" mapstructure:"PidsLimit"`

	// E2B 云端沙箱（Driver: e2b 时生效）
	E2BKey        string `yaml:"E2BKey" mapstructure:"E2BKey"`               // e2b_ 开头的 API Key
	E2BTemplate   string `yaml:"E2BTemplate" mapstructure:"E2BTemplate"`     // 沙箱模板 ID，默认 base
	E2BTimeoutSec int    `yaml:"E2BTimeoutSec" mapstructure:"E2BTimeoutSec"` // 沙箱 TTL（秒），超时未续用会被服务端回收
}

// driver 活动驱动；默认本地，Init 未被调用时开箱即用
var driver Driver = localDriver{}

// Init 装配全局沙箱驱动（main 启动时调用一次）
func Init(cfg Config) {
	switch cfg.Driver {
	case "docker":
		driver = newDockerDriver(cfg)
		return
	case "e2b":
		if cfg.E2BKey == "" {
			fmt.Printf("[WARN] E2BKey 未配置，执行沙箱回落到 local 驱动\n")
			driver = localDriver{}
			return
		}
		driver = newE2BDriver(cfg)
		return
	}
	driver = localDriver{}
	fmt.Printf("[INFO] 执行沙箱驱动: local\n")
}

// Default 活动沙箱驱动
func Default() Driver { return driver }

// DriverName 活动驱动名（local / docker）
func DriverName() string { return driver.Name() }

// EnvNote 活动驱动的运行环境契约
func EnvNote() string { return driver.EnvNote() }

// RunBash 经活动驱动执行 bash 命令
func RunBash(ctx context.Context, sessionID, hostDir, command string, timeoutSecs int) Result {
	return driver.RunBash(ctx, sessionID, hostDir, command, timeoutSecs)
}

// RunPython 经活动驱动执行 Python 脚本
func RunPython(ctx context.Context, sessionID, hostDir, scriptRel string, timeoutSecs int) Result {
	return driver.RunPython(ctx, sessionID, hostDir, scriptRel, timeoutSecs)
}

// Close 销毁会话沙箱（会话删除时调用；尽力而为，失败不阻塞删除流程）
func Close(sessionID string) { driver.Close(sessionID) }

// ReadFile 经活动驱动读取工作区文件
func ReadFile(ctx context.Context, sessionID, hostDir, rel string) ([]byte, error) {
	return driver.ReadFile(ctx, sessionID, hostDir, rel)
}

// WriteFile 经活动驱动写入工作区文件（沙箱与宿主镜像双落）
func WriteFile(ctx context.Context, sessionID, hostDir, rel string, data []byte) error {
	return driver.WriteFile(ctx, sessionID, hostDir, rel, data)
}

// RemoveFile 经活动驱动删除工作区文件
func RemoveFile(sessionID, hostDir, rel string) { driver.RemoveFile(sessionID, hostDir, rel) }

// localEnvNote 本地驱动的环境契约：宿主即执行环境，按构建平台给模型真实预期
func localEnvNote() string {
	if runtime.GOOS == "windows" {
		return "- 宿主是 **Windows + Git Bash**，不是 Linux/macOS；\n" +
			"- 启动目录就是会话工作区，用户上传与工具产出的文件都在这里；不要 cd 到 /tmp 等系统目录找文件（重定向到 /dev/null 可用）；\n" +
			"- Python 解释器命令是 `python`；`python3` 是无效的商店占位符，会报 \"Python was not found\"；要跑 Python 优先用 python_exec 工具；\n" +
			"- 没有 systemd、apt、brew 等，系统依赖不可安装。"
	}
	return "- 宿主是 Linux/macOS，命令直接跑在宿主 shell 上；\n" +
		"- 启动目录就是会话工作区，用户上传与工具产出的文件都在这里；\n" +
		"- Python 解释器命令通常是 `python3`；跑 Python 优先用 python_exec 工具；\n" +
		"- 系统依赖不可随意安装，注意权限约束。"
}

// infraErr 把连接层失败转成给模型看的文案：模型读不懂 "connection refused"，
// 会误以为是自己命令写错而反复换路试探（参考阶段四前车之鉴），必须明确告知
// 坏的是环境、不是命令。真实原因通过错误链保留给日志。
func infraErr(err error, what string) error {
	return errors.Join(err, errors.New(what), errors.NewMsg(
		"沙箱服务不可用（"+what+"）：这不是命令本身的问题——换用其它工具或其它路径重试同样会失败。"+
			"请立即停止重试，直接告诉用户沙箱环境异常、需要管理员检查执行环境。"))
}
