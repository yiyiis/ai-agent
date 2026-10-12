// config.go MCP 配置段（项目惯例：配置结构体由所属包自持）。
//
// config.yaml 的 MCP.Servers 是广场的"官方种子"：启动时投影进 mcp_servers 表
// （system=1，置顶展示、网页不可删改），运行态以 DB 广场表为源头。stdio 型
// server 只能经种子配置——广场 API 只接受 http（安全边界见 specs/mcp/README.md）。
package mcp

// Config MCP 配置段
type Config struct {
	Servers []SeedServer `yaml:"Servers" mapstructure:"Servers"`
}

// SeedServer 管理员配置的种子 server
type SeedServer struct {
	Name        string `yaml:"Name" mapstructure:"Name"`
	Description string `yaml:"Description" mapstructure:"Description"`
	Transport   string `yaml:"Transport" mapstructure:"Transport"` // http | stdio
	URL         string `yaml:"URL" mapstructure:"URL"`             // http 型端点
	// Headers 静态鉴权头（Bearer 等），日志与出参脱敏
	Headers map[string]string `yaml:"Headers" mapstructure:"Headers"`
	// Command stdio 型完整命令行（含程序名）；Args 追加在其后
	Command []string          `yaml:"Command" mapstructure:"Command"`
	Args    []string          `yaml:"Args" mapstructure:"Args"`
	Env     map[string]string `yaml:"Env" mapstructure:"Env"`
	// CallTimeoutSec tools/call 超时（秒），0 走默认 60
	CallTimeoutSec int `yaml:"CallTimeoutSec" mapstructure:"CallTimeoutSec"`
	// ToolAllow 工具白名单（空=全部）
	ToolAllow []string `yaml:"ToolAllow" mapstructure:"ToolAllow"`
}
