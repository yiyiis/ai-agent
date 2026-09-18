package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/agent"
	"backend/pkg/errors"
	"backend/pkg/provider"
	"backend/pkg/sandbox"
	"backend/pkg/skill"
	"backend/pkg/tools"
)

// RunExecutor 任务执行器
type RunExecutor struct{}

// NewExecutor 创建任务执行器实例
func NewExecutor() *RunExecutor {
	return &RunExecutor{}
}

// Execute 执行单个任务，支持 script (纯脚本) 与 llm (模型推理) 双模
func (e *RunExecutor) Execute(ctx context.Context, run *model.SkillRun, sk *model.Skill) *ExecResult {
	mode := "script"
	if run.ExecContext != nil {
		var ec map[string]any
		if json.Unmarshal([]byte(*run.ExecContext), &ec) == nil {
			if m, ok := ec["mode"].(string); ok && m != "" {
				mode = m
			}
		}
	}

	// 若未显式指定，且技能绑定了专属模型，则走 llm 模式
	if mode == "auto" {
		if sk != nil && sk.Model != nil && strings.TrimSpace(*sk.Model) != "" {
			mode = "llm"
		} else {
			mode = "script"
		}
	}

	if mode == "llm" {
		return e.executeLLM(ctx, run, sk)
	}
	return e.executeScript(ctx, run, sk)
}

// executeScript 纯脚本沙箱模式执行：毫秒级响应，零 Token 开销
func (e *RunExecutor) executeScript(ctx context.Context, run *model.SkillRun, sk *model.Skill) *ExecResult {
	start := time.Now()
	sessionID := "run_" + run.RunID
	defer sandbox.Close(sessionID)

	dirName := ""
	if sk != nil {
		dirName = sk.DirName
	}
	if dirName == "" {
		dirName = run.SkillName
	}

	// 1. 挂载技能代码到沙箱工作区
	_, mountErr := skill.MountToSandbox(ctx, sessionID, dirName)
	if mountErr != nil {
		dur := time.Since(start).Milliseconds()
		return &ExecResult{
			DurationMs:  dur,
			Err:         mountErr,
			FailureKind: FailureKindSandbox,
			Stderr:      "[沙箱挂载失败] " + mountErr.Error(),
		}
	}

	// 2. 读取并注入技能私密密钥
	secrets := skill.ReadSecrets(dirName)
	envList := tools.SortedEnv(secrets)

	// 注入环境变量
	paramsJSON := "{}"
	if run.Params != nil && strings.TrimSpace(*run.Params) != "" {
		paramsJSON = *run.Params
	}
	envList = append(envList,
		"SKILL_PARAMS_JSON="+paramsJSON,
		"SKILL_NAME="+run.SkillName,
		"SKILL_DIR="+dirName,
		"SKILL_RUN_ID="+run.RunID,
	)

	// 3. 构建执行命令
	cmd := e.detectCommand(run, dirName, paramsJSON)

	// 4. 执行命令并记录耗时
	timeoutSec := 120
	if run.ExecContext != nil {
		var ec map[string]any
		if json.Unmarshal([]byte(*run.ExecContext), &ec) == nil {
			if t, ok := ec["timeout_sec"].(float64); ok && t > 0 {
				timeoutSec = int(t)
			}
		}
	}

	res := sandbox.RunBash(ctx, sessionID, cmd, envList, timeoutSec)
	dur := time.Since(start).Milliseconds()

	// 5. 分析执行结果与错误分类
	if res.Err != nil || res.ExitCode != 0 || res.Killed {
		fKind := ClassifyFailure(res.Err, res.Stdout, res.Stderr, res.ExitCode, res.Killed)
		err := res.Err
		if err == nil && res.ExitCode != 0 {
			err = fmt.Errorf("脚本执行退出码非零: %d", res.ExitCode)
		}
		return &ExecResult{
			Stdout:      res.Stdout,
			Stderr:      res.Stderr,
			ExitCode:    res.ExitCode,
			Killed:      res.Killed,
			DurationMs:  dur,
			Err:         err,
			FailureKind: fKind,
		}
	}

	// 6. 成功产出解析
	var resObj map[string]any
	stdoutTrimmed := strings.TrimSpace(res.Stdout)
	if strings.HasPrefix(stdoutTrimmed, "{") && strings.HasSuffix(stdoutTrimmed, "}") {
		_ = json.Unmarshal([]byte(stdoutTrimmed), &resObj)
	}
	if resObj == nil {
		resObj = map[string]any{
			"output": stdoutTrimmed,
		}
	}

	return &ExecResult{
		Stdout:     res.Stdout,
		Stderr:     res.Stderr,
		ExitCode:   0,
		DurationMs: dur,
		ResultJSON: resObj,
	}
}

// detectCommand 探测或构造技能的调用命令
func (e *RunExecutor) detectCommand(run *model.SkillRun, dirName, paramsJSON string) string {
	// 若显式指定了 command，优先执行
	if run.ExecContext != nil {
		var ec map[string]any
		if json.Unmarshal([]byte(*run.ExecContext), &ec) == nil {
			if c, ok := ec["command"].(string); ok && strings.TrimSpace(c) != "" {
				return c
			}
		}
	}

	// 探测 skills/<dirName>/scripts/ 下的脚本
	srcRoot := filepath.Join(skill.Root(), dirName, "scripts")
	if entries, err := os.ReadDir(srcRoot); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if strings.HasSuffix(name, ".py") {
				scriptRel := fmt.Sprintf("skills/%s/scripts/%s", dirName, name)
				args := buildScriptArgs(paramsJSON)
				return fmt.Sprintf("python %s %s", scriptRel, args)
			}
			if strings.HasSuffix(name, ".sh") {
				scriptRel := fmt.Sprintf("skills/%s/scripts/%s", dirName, name)
				return fmt.Sprintf("bash %s", scriptRel)
			}
		}
	}

	// 回退：若存在 skills/<dirName>/main.py
	if _, err := os.Stat(filepath.Join(skill.Root(), dirName, "main.py")); err == nil {
		return fmt.Sprintf("python skills/%s/main.py", dirName)
	}

	// 最终兜底：打印参数确认环境连通性
	return `python -c "import os; print(os.environ.get('SKILL_PARAMS_JSON', '{}'))"`
}

// buildScriptArgs 解析 params JSON，抽取命令行参数
func buildScriptArgs(paramsJSON string) string {
	var p map[string]any
	if err := json.Unmarshal([]byte(paramsJSON), &p); err != nil || len(p) == 0 {
		return ""
	}

	var parts []string
	// 优先抽取核心主参数（如 url, query, target 等作为首个位置参数）
	for _, key := range []string{"url", "query", "target", "path", "file"} {
		if val, ok := p[key]; ok {
			strVal := fmt.Sprintf("%v", val)
			parts = append(parts, fmt.Sprintf("%q", strVal))
			delete(p, key)
			break
		}
	}

	// 其余参数转为 --key value
	for k, v := range p {
		switch val := v.(type) {
		case bool:
			if val {
				parts = append(parts, fmt.Sprintf("--%s", k))
			}
		default:
			parts = append(parts, fmt.Sprintf("--%s %q", k, fmt.Sprintf("%v", val)))
		}
	}

	return strings.Join(parts, " ")
}

// executeLLM 大模型推理编排模式执行
func (e *RunExecutor) executeLLM(ctx context.Context, run *model.SkillRun, sk *model.Skill) *ExecResult {
	start := time.Now()
	sessionID := "run_" + run.RunID
	if run.SessionID != nil && strings.TrimSpace(*run.SessionID) != "" {
		sessionID = *run.SessionID
	}

	// 若为无头模式 (user_id == 0)，任务结束时清理沙箱；若为有头模式 (user_id > 0)，
	// 保持沙箱存活，由沙箱管理器的空闲超时策略 (15分钟) 统一回收，方便员工继续交互
	if run.UserID == 0 {
		defer sandbox.Close(sessionID)
	}

	// 1. 选择模型与 Provider
	modelName := ""
	if sk != nil && sk.Model != nil {
		modelName = *sk.Model
	}
	p, ok := provider.GetProviderForModel(modelName)
	if !ok {
		dur := time.Since(start).Milliseconds()
		return &ExecResult{
			DurationMs:  dur,
			Err:         errors.New("未找到可用的模型 Provider"),
			FailureKind: FailureKindRateLimited,
			Stderr:      "未找到可用的模型 Provider",
		}
	}

	// 2. 挂载技能
	dirName := run.SkillName
	if sk != nil {
		dirName = sk.DirName
	}
	_, _ = skill.MountToSandbox(ctx, sessionID, dirName)

	// 3. 构建或物化真实/无头 Session，保证外键约束有效与全链路可追溯
	now := time.Now()
	title := fmt.Sprintf("API: %s", run.SkillName)
	sess, err := dao.GetSessionBySessionID(ctx, sessionID)
	if err != nil || sess == nil {
		sess = &model.Session{
			SessionID:    sessionID,
			CompanyID:    run.CompanyID,
			UserID:       run.UserID,
			Title:        title,
			Model:        modelName,
			SystemPrompt: "",
			AutoSkill:    true,
			Interactive:  false,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if cErr := dao.CreateSession(ctx, sess); cErr == nil {
			run.SessionID = &sessionID
			_ = dao.UpdateSkillRun(ctx, run.RunID, map[string]interface{}{"session_id": sessionID})
		}
	}

	paramsStr := "{}"
	if run.Params != nil && strings.TrimSpace(*run.Params) != "" {
		paramsStr = *run.Params
	}
	userPrompt := fmt.Sprintf("请执行技能 `%s`，输入参数为：%s", run.SkillName, paramsStr)
	var finalAnswer strings.Builder
	emit := func(ev agent.Event) {
		if ev.Type == "answer" {
			finalAnswer.WriteString(ev.Content)
		}
	}

	deps := agent.Deps{
		Provider: p,
	}

	err = agent.Run(ctx, deps, sess, nil, userPrompt, nil, emit)
	dur := time.Since(start).Milliseconds()

	if err != nil {
		fKind := ClassifyFailure(err, "", err.Error(), -1, false)
		return &ExecResult{
			Stderr:      err.Error(),
			DurationMs:  dur,
			Err:         err,
			FailureKind: fKind,
		}
	}

	return &ExecResult{
		DurationMs: dur,
		Stdout:     finalAnswer.String(),
		ResultJSON: map[string]any{
			"answer": finalAnswer.String(),
		},
	}
}
