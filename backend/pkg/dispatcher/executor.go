package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/agent"
	"backend/pkg/errors"
	"backend/pkg/provider"
	"backend/pkg/sandbox"
	"backend/pkg/skill"
	"backend/pkg/storage"
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

	// 读取技能清单契约 (manifest.json)
	manifest, _ := skill.ReadManifest(dirName)

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
		"SKILL_RUN_ATTEMPT="+fmt.Sprintf("%d", run.Attempt),
	)

	// 3. 构建执行命令与参数输入管道
	workdir := path.Join("workdir", run.RunID)
	timeoutSec := 120
	if manifest != nil && manifest.TimeoutSec > 0 {
		timeoutSec = manifest.TimeoutSec
	}
	if run.ExecContext != nil {
		var ec map[string]any
		if json.Unmarshal([]byte(*run.ExecContext), &ec) == nil {
			if t, ok := ec["timeout_sec"].(float64); ok && t > 0 {
				timeoutSec = int(t)
			}
		}
	}

	var explicitCmd string
	if run.ExecContext != nil {
		var ec map[string]any
		if json.Unmarshal([]byte(*run.ExecContext), &ec) == nil {
			if c, ok := ec["command"].(string); ok && strings.TrimSpace(c) != "" {
				explicitCmd = c
			}
		}
	}

	var cmd string
	if explicitCmd != "" {
		cmd = explicitCmd
	} else if manifest != nil && strings.TrimSpace(manifest.Command) != "" {
		// 标准 manifest 契约模式：占位符替换
		cmdTpl := strings.ReplaceAll(manifest.Command, "/skills/", "skills/")
		vars := map[string]string{
			"dir":     dirName,
			"workdir": workdir,
			"run_id":  run.RunID,
		}
		if run.Params != nil && strings.TrimSpace(*run.Params) != "" {
			var pMap map[string]any
			if json.Unmarshal([]byte(*run.Params), &pMap) == nil {
				for k, v := range pMap {
					vars[k] = fmt.Sprintf("%v", v)
				}
			}
		}
		cmdBody := skill.RenderTemplate(cmdTpl, vars)

		if manifest.Stdin != "none" {
			// 将 params JSON 安全落盘至工作区并通过 stdin 重定向喂入
			inputRel := path.Join(workdir, "input.json")
			_ = sandbox.WriteFile(ctx, sessionID, inputRel, []byte(paramsJSON))
			cmd = fmt.Sprintf("mkdir -p %s && %s < %s", workdir, cmdBody, inputRel)
		} else {
			cmd = fmt.Sprintf("mkdir -p %s && %s", workdir, cmdBody)
		}
	} else {
		// 向下兼容：无 manifest 时走原有探测逻辑
		cmd = e.detectCommand(run, dirName, paramsJSON)
	}

	res := sandbox.RunBash(ctx, sessionID, cmd, envList, timeoutSec)
	dur := time.Since(start).Milliseconds()

	// 4. 分析执行结果与错误分类
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

	// 5. 产物与结构化结果抽取 (日志与结果彻底解耦)
	resObj, artifacts := e.extractResults(ctx, sessionID, manifest, res.Stdout, workdir)

	return &ExecResult{
		Stdout:     res.Stdout,
		Stderr:     res.Stderr,
		ExitCode:   0,
		DurationMs: dur,
		ResultJSON: resObj,
		Artifacts:  artifacts,
	}
}

var defaultResultRe = regexp.MustCompile(`(?s)\[API_RESULT_BEGIN\]\s*(.+?)\s*\[API_RESULT_END\]`)

// extractResults 依据 manifest.result_extractors 抽取结构化结果与文件产物
func (e *RunExecutor) extractResults(
	ctx context.Context,
	sessionID string,
	manifest *skill.Manifest,
	stdout, workdir string,
) (map[string]any, []map[string]any) {
	resObj := make(map[string]any)
	var artifacts []map[string]any

	if manifest != nil && len(manifest.ResultExtractors) > 0 {
		for _, ext := range manifest.ResultExtractors {
			name := ext.Name
			if name == "" {
				name = "result"
			}
			typ := strings.ToLower(ext.Type)

			switch typ {
			case "regex_stdout":
				patStr := ext.Pattern
				if ext.DotAll && !strings.HasPrefix(patStr, "(?s)") {
					patStr = "(?s)" + patStr
				}
				re, err := regexp.Compile(patStr)
				if err != nil {
					continue
				}
				allMatches := re.FindAllStringSubmatch(stdout, -1)
				if len(allMatches) > 0 {
					lastMatch := allMatches[len(allMatches)-1]
					grp := ext.Group
					if grp <= 0 {
						grp = 1
					}
					if len(lastMatch) > grp {
						captured := strings.TrimSpace(lastMatch[grp])
						if ext.AsJSON {
							var parsed any
							if json.Unmarshal([]byte(captured), &parsed) == nil {
								resObj[name] = parsed
							} else {
								resObj[name] = captured
							}
						} else {
							resObj[name] = captured
						}
					}
				}

			case "regex_all_stdout":
				patStr := ext.Pattern
				if ext.DotAll && !strings.HasPrefix(patStr, "(?s)") {
					patStr = "(?s)" + patStr
				}
				re, err := regexp.Compile(patStr)
				if err != nil {
					continue
				}
				allMatches := re.FindAllStringSubmatch(stdout, -1)
				var items []any
				grp := ext.Group
				if grp <= 0 {
					grp = 1
				}
				for _, m := range allMatches {
					if len(m) > grp {
						captured := strings.TrimSpace(m[grp])
						if ext.AsJSON {
							var parsed any
							if json.Unmarshal([]byte(captured), &parsed) == nil {
								items = append(items, parsed)
							} else {
								items = append(items, captured)
							}
						} else {
							items = append(items, captured)
						}
					}
				}
				resObj[name] = items

			case "file":
				fileRel := strings.ReplaceAll(ext.Path, "{workdir}", workdir)
				data, readErr := sandbox.ReadFile(ctx, sessionID, fileRel)
				if readErr == nil && len(data) > 0 {
					if ext.Upload {
						baseName := filepath.Base(fileRel)
						if obj, upErr := storage.Default().Put(ctx, baseName, bytes.NewReader(data), int64(len(data)), ""); upErr == nil {
							artifacts = append(artifacts, map[string]any{
								"name":         name,
								"url":          obj.URL,
								"filename":     obj.Filename,
								"size":         obj.Size,
								"content_type": obj.ContentType,
							})
							resObj[name] = obj.URL
						}
					} else if ext.AsJSON {
						var parsed any
						if json.Unmarshal(data, &parsed) == nil {
							resObj[name] = parsed
						} else {
							resObj[name] = string(data)
						}
					} else {
						resObj[name] = string(data)
					}
				}
			}
		}
	}

	// 未配置抽取器或抽取结果为空时，走通用默认兜底逻辑
	if len(resObj) == 0 {
		stdoutTrimmed := strings.TrimSpace(stdout)
		if m := defaultResultRe.FindStringSubmatch(stdoutTrimmed); len(m) > 1 {
			captured := strings.TrimSpace(m[1])
			var parsed map[string]any
			if json.Unmarshal([]byte(captured), &parsed) == nil {
				resObj = parsed
			} else {
				resObj = map[string]any{"output": captured}
			}
		} else if strings.HasPrefix(stdoutTrimmed, "{") && strings.HasSuffix(stdoutTrimmed, "}") {
			_ = json.Unmarshal([]byte(stdoutTrimmed), &resObj)
		}
	}

	if len(resObj) == 0 {
		resObj = map[string]any{
			"output": strings.TrimSpace(stdout),
		}
	}

	return resObj, artifacts
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

	// 若配置了 manifest.llm_prompt_template，使用模板渲染专属提示词
	manifest, _ := skill.ReadManifest(dirName)
	if manifest != nil && strings.TrimSpace(manifest.LLMPromptTemplate) != "" {
		vars := map[string]string{
			"skill_name":  run.SkillName,
			"skill_dir":   dirName,
			"params_json": paramsStr,
			"run_id":      run.RunID,
			"session_id":  sessionID,
		}
		userPrompt = skill.RenderTemplate(manifest.LLMPromptTemplate, vars)
	}

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
