package api

import (
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"sort"
	"time"

	"backend/dao"
	"backend/dal/model"
	"backend/pkg/apiwarp"
	"backend/pkg/db"
	"backend/pkg/errors"
	"backend/pkg/jwt"
	"backend/pkg/skill"
)

type SkillOut struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	DirName      string    `json:"dir_name"`
	Tags         *string   `json:"tags"`
	CreatedAt    time.Time `json:"created_at"`
	OwnerID      int64     `json:"owner_id"`
	OwnerName    string    `json:"owner_name"`
	Confidential bool      `json:"confidential"`
	Model        *string   `json:"model"`
	// 机密可见性派生字段：前端据此显示"预览/管理"入口
	IsOwner bool `json:"is_owner"`
	CanView bool `json:"can_view"`
}

type ListSkillsReq struct{}

// toSkillOut 实体 → 出参（is_owner/can_view 按当前登录态派生：
// 机密技能仅所有者可预览，非所有者只能使用）
func toSkillOut(s *model.Skill, identity jwt.TokenClaims) SkillOut {
	isOwner := s.OwnerID != 0 && s.OwnerID == identity.UserID
	return SkillOut{
		ID:           s.ID,
		Name:         s.Name,
		Description:  s.Description,
		DirName:      s.DirName,
		Tags:         s.Tags,
		CreatedAt:    s.CreatedAt,
		OwnerID:      s.OwnerID,
		OwnerName:    s.OwnerName,
		Confidential: s.Confidential,
		Model:        s.Model,
		IsOwner:      isOwner,
		CanView:      !s.Confidential || isOwner,
	}
}

// skillOwnedBy 当前用户是否为技能所有者（owner_id=0 的系统技能无人"拥有"，
// 只能由管理员经文件系统维护——网页端不可删改，防公共技能被任意用户覆盖）
func skillOwnedBy(s *model.Skill, identity jwt.TokenClaims) bool {
	return s.OwnerID != 0 && s.OwnerID == identity.UserID
}

// loadOwnedSkillRow 加载技能行并校验所有者身份（管理操作统一入口）
func loadOwnedSkillRow(ctx context.Context, identity jwt.TokenClaims, id int64) (*model.Skill, error) {
	row, err := dao.GetSkillByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.NewMsg("技能不存在")
	}
	if !skillOwnedBy(row, identity) {
		return nil, errors.NewMsg("仅技能所有者可执行该操作")
	}
	return row, nil
}

// syncSkillRegistry 文件系统技能库 → skills 注册表同步（新增 / 更新 / 清理）。
// 技能目录是唯一源头，注册表是会话关联与目录检索的投影。
func syncSkillRegistry(ctx context.Context) error {
	items := make([]dao.SkillRegistryItem, 0)
	// ListDir 已剔除无 SKILL.md / 缺 name 的坏目录（告警不中断）
	for _, e := range skill.ListDir() {
		items = append(items, dao.SkillRegistryItem{
			Name:        e.Name,
			Description: e.Description,
			DirName:     e.DirName,
			TagsJSON:    dao.MarshalTags(e.Tags),
		})
	}
	return dao.SyncSkills(ctx, items)
}

// ListSkills 获取当前可用技能列表 (GET /api/skills)。
// 每次调用先做一次文件系统 → 注册表同步，保证目录里增删技能即时可见。
func ListSkills(ctx context.Context, _ *ListSkillsReq) (*[]SkillOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	if err := syncSkillRegistry(ctx); err != nil {
		return nil, err
	}
	skills, err := dao.ListSkills(ctx)
	if err != nil {
		return nil, err
	}

	res := make([]SkillOut, 0, len(skills))
	for _, s := range skills {
		res = append(res, toSkillOut(s, identity))
	}
	return &res, nil
}

// InitSkillRegistry 启动时同步一次技能注册表（main 调用），
// 让预置技能目录无需等首次 /api/skills 调用即可被会话使用
func InitSkillRegistry() {
	ctx := db.WithContext(context.Background())
	if err := syncSkillRegistry(ctx); err != nil {
		// 注册表同步失败不阻断启动：会话侧技能装配自行降级
		slog.Warn("启动同步技能注册表失败", "err", errors.Stack(err))
	}
}

// ---------- zip 上传安装 ----------

type UploadSkillReq struct {
	File         *multipart.FileHeader `form:"file" binding:"required"`
	Confidential bool                  `form:"confidential"`
}

type SkillUploadOut struct {
	Skill                SkillOut `json:"skill"`
	CreatedVersionNumber int64    `json:"created_version_number"`
	Activated            bool     `json:"activated"`
}

// UploadSkill 上传 zip 安装技能 (POST /api/skills/upload)。
//
// 响应为**裸 JSON**（不经统一信封）：前端 uploadSkill 沿参考项目契约直接
// res.json() 取实体。同名（同名目录）覆盖仅限所有者；系统技能（无主）不允许
// 网页覆盖。每次安装记录一个版本号并立即生效（工作副本即激活副本，最小闭环
// 不做"发布不激活"的版本目录机制，activated 恒为 true）。
func UploadSkill(ctx context.Context, req *UploadSkillReq) (*apiwarp.RawJSONData, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}

	fh := req.File
	if fh.Size > skill.MaxZipBytes {
		return nil, errors.NewMsg("zip 包超过 30MB 上限")
	}
	src, err := fh.Open()
	if err != nil {
		return nil, errors.Join(err, errors.New("open skill zip"), errors.NewMsg("读取上传文件失败"))
	}
	defer src.Close()
	zipBytes, err := io.ReadAll(io.LimitReader(src, skill.MaxZipBytes+1))
	if err != nil {
		return nil, errors.Join(err, errors.New("read skill zip"), errors.NewMsg("读取上传文件失败"))
	}
	if len(zipBytes) > skill.MaxZipBytes {
		return nil, errors.NewMsg("zip 包超过 30MB 上限")
	}

	// 先预检：拿 frontmatter 名字决定"新建还是覆盖"，覆盖前校验归属
	meta, err := skill.PeekZip(zipBytes)
	if err != nil {
		return nil, err
	}
	dirName := skill.SanitizeDirName(meta.Name)
	if existing, err := dao.GetSkillByDirName(ctx, dirName); err != nil {
		return nil, err
	} else if existing != nil && !skillOwnedBy(existing, identity) {
		return nil, errors.NewMsg("已存在同名技能「" + existing.Name + "」且不属于你，无法覆盖")
	}

	// 安装：文件落 skills/<dir>/，zip 携带的 secrets 抽到 .secrets/
	//（仅当本次带了才写——避免空 zip 覆盖掉已配置的机密）
	extracted, err := skill.InstallFromZip(zipBytes, dirName)
	if err != nil {
		return nil, err
	}
	if len(extracted) > 0 {
		if err := skill.WriteSecrets(dirName, extracted); err != nil {
			return nil, err
		}
	}

	row, err := dao.UpsertUploadedSkill(ctx, dao.UploadedSkillItem{
		Name:         meta.Name,
		Description:  meta.Description,
		DirName:      dirName,
		TagsJSON:     tagsPtrOrNil(dao.MarshalTags(meta.Tags)),
		OwnerID:      identity.UserID,
		OwnerName:    identity.Name,
		Confidential: req.Confidential,
	})
	if err != nil {
		return nil, err
	}

	// 版本记录：每次上传 +1，工作副本即当前激活版本
	versionNum, err := dao.NextSkillVersionNumber(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	if err := dao.CreateSkillVersion(ctx, &model.SkillVersion{
		SkillID:       row.ID,
		VersionNumber: versionNum,
		Notes:         "",
		CreatedBy:     identity.UserID,
		CreatedByName: identity.Name,
		Source:        "upload",
	}); err != nil {
		return nil, err
	}
	slog.Info("技能上传安装", "skill", meta.Name, "dir", dirName, "version", versionNum, "uid", identity.UserID)

	return &apiwarp.RawJSONData{Body: SkillUploadOut{
		Skill:                toSkillOut(row, identity),
		CreatedVersionNumber: versionNum,
		Activated:            true,
	}}, nil
}

func tagsPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---------- 删除 / 编辑 ----------

type SkillIDReq struct {
	ID int64 `uri:"id"`
}

// DeleteSkill 删除技能 (DELETE /api/skills/:id)：目录、机密文件、注册行、
// 会话关联与版本历史一并清理；仅所有者可删。
func DeleteSkill(ctx context.Context, req *SkillIDReq) (*StatusOKResp, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := loadOwnedSkillRow(ctx, identity, req.ID)
	if err != nil {
		return nil, err
	}

	if err := skill.RemoveSkill(row.DirName); err != nil {
		return nil, err
	}
	if err := dao.DeleteSkillCascade(ctx, row.ID); err != nil {
		return nil, err
	}
	slog.Info("技能删除", "skill", row.Name, "dir", row.DirName, "uid", identity.UserID)
	return &StatusOKResp{Status: "ok"}, nil
}

type UpdateSkillReq struct {
	ID           int64    `uri:"id"`
	Confidential *bool    `json:"confidential"`
	Model        *string  `json:"model"`
	Tags         []string `json:"tags"`
}

// UpdateSkill 编辑技能属性 (PATCH /api/skills/:id)：机密标记 / 指定模型 / 标签。
// model 传空串表示清空；tags 传空数组表示清空。
func UpdateSkill(ctx context.Context, req *UpdateSkillReq) (*SkillOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := loadOwnedSkillRow(ctx, identity, req.ID)
	if err != nil {
		return nil, err
	}

	updates := make(map[string]interface{})
	if req.Confidential != nil {
		updates["confidential"] = *req.Confidential
		row.Confidential = *req.Confidential
	}
	if req.Model != nil {
		if *req.Model == "" {
			updates["model"] = nil
			row.Model = nil
		} else {
			updates["model"] = *req.Model
			row.Model = req.Model
		}
	}
	if req.Tags != nil {
		if len(req.Tags) == 0 {
			updates["tags"] = nil
			row.Tags = nil
		} else {
			tagsJSON := dao.MarshalTags(req.Tags)
			updates["tags"] = tagsJSON
			row.Tags = &tagsJSON
		}
	}
	if err := dao.UpdateSkillColumns(ctx, row.ID, updates); err != nil {
		return nil, err
	}
	out := toSkillOut(row, identity)
	return &out, nil
}

// ---------- 详情 / 机密 ----------

type SkillBodyOut struct {
	Name        string   `json:"name"`
	Description *string  `json:"description"`
	Body        string   `json:"body"`
	HasSecrets  bool     `json:"has_secrets"`
	SecretKeys  []string `json:"secret_keys"`
}

// GetSkillBody 技能详情 (GET /api/skills/:id/body)：SKILL.md 全文 + 机密概况。
// 机密技能仅所有者可预览（正文等于"预览"，非所有者只能使用不能看）。
func GetSkillBody(ctx context.Context, req *SkillIDReq) (*SkillBodyOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := dao.GetSkillByID(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.NewMsg("技能不存在")
	}
	if row.Confidential && !skillOwnedBy(row, identity) {
		return nil, errors.NewMsg("机密技能仅所有者可预览")
	}
	body, err := skill.ReadSkillMD(row.DirName)
	if err != nil {
		return nil, err
	}

	var desc *string
	if row.Description != "" {
		d := row.Description
		desc = &d
	}
	out := &SkillBodyOut{Name: row.Name, Description: desc, Body: body, SecretKeys: []string{}}
	// 机密键名只对所有人可见——键名本身可能泄漏用途（如 BRAVE_API_KEY）
	if skillOwnedBy(row, identity) {
		for _, k := range sortedSecretKeys(skill.ReadSecrets(row.DirName)) {
			out.SecretKeys = append(out.SecretKeys, k)
		}
		out.HasSecrets = len(out.SecretKeys) > 0
	}
	return out, nil
}

func sortedSecretKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

type UpdateSkillSecretsReq struct {
	ID      int64             `uri:"id"`
	Secrets map[string]string `json:"secrets" binding:"required"`
}

// GetSkillSecrets 读取技能机密 (GET /api/skills/:id/secrets)。
// 机密值只对所有人返回；非所有者返回空表而不是报错——详情弹窗会并行拉
// body + secrets，公开技能对所有人可见，这里报错会把整个弹窗打挂。
func GetSkillSecrets(ctx context.Context, req *SkillIDReq) (*map[string]string, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := dao.GetSkillByID(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.NewMsg("技能不存在")
	}
	if !skillOwnedBy(row, identity) {
		empty := map[string]string{}
		return &empty, nil
	}
	secrets := skill.ReadSecrets(row.DirName)
	return &secrets, nil
}

// UpdateSkillSecrets 整体替换技能机密 (PUT /api/skills/:id/secrets)，仅所有者。
// PUT 语义：提交的 map 就是全量，删掉某个键即删除该机密。
func UpdateSkillSecrets(ctx context.Context, req *UpdateSkillSecretsReq) (*StatusOKResp, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := loadOwnedSkillRow(ctx, identity, req.ID)
	if err != nil {
		return nil, err
	}
	if err := skill.WriteSecrets(row.DirName, req.Secrets); err != nil {
		return nil, err
	}
	return &StatusOKResp{Status: "ok"}, nil
}

// ---------- 版本历史 ----------

type SkillVersionOut struct {
	ID            int64  `json:"id"`
	VersionNumber int64  `json:"version_number"`
	Notes         string `json:"notes"`
	CreatedAt     string `json:"created_at"`
	IsCurrent     bool   `json:"is_current"`
	CreatedBy     int64  `json:"created_by"`
	CreatedByName string `json:"created_by_name"`
	Source        string `json:"source"`
}

// ListSkillVersions 版本历史 (GET /api/skills/:id/versions)：版本号倒序，
// 最新一条即当前生效版本（工作副本始终是激活副本）。
func ListSkillVersions(ctx context.Context, req *SkillIDReq) (*[]SkillVersionOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := dao.GetSkillByID(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.NewMsg("技能不存在")
	}

	rows, err := dao.ListSkillVersions(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	res := make([]SkillVersionOut, 0, len(rows))
	for i, v := range rows {
		res = append(res, SkillVersionOut{
			ID:            v.ID,
			VersionNumber: v.VersionNumber,
			Notes:         v.Notes,
			CreatedAt:     v.CreatedAt.Format(time.RFC3339),
			IsCurrent:     i == 0, // 倒序第一条即最新
			CreatedBy:     v.CreatedBy,
			CreatedByName: v.CreatedByName,
			Source:        v.Source,
		})
	}
	return &res, nil
}
