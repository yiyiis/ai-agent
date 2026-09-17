// skill_files.go 技能在线文件编辑与配套管理端点。
//
// 权限分层：浏览（tree / 读文件 / 下载）按 can_view（机密技能仅所有者），
// 修改（写 / 新建 / 删除 / 重命名 / 从空新建）仅所有者。路径安全由
// pkg/skill 的 safeJoin 统一收口（越界 / 保留目录 / SKILL.md 保护）。
package api

import (
	"context"
	"strings"

	"backend/dao"
	"backend/dal/model"
	"backend/pkg/apiwarp"
	"backend/pkg/errors"
	"backend/pkg/jwt"
	"backend/pkg/skill"
)

// loadSkillRow 加载技能行（浏览类入口：不存在统一报错）
func loadSkillRow(ctx context.Context, id int64) (*model.Skill, error) {
	row, err := dao.GetSkillByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.NewMsg("技能不存在")
	}
	return row, nil
}

// ---------- 文件清单与读写 ----------

type SkillTreeReq struct {
	ID int64 `uri:"id"`
}

// GetSkillTree 工作副本文件清单 (GET /api/skills/:id/tree)：扁平列表（path/size/is_binary），
// 前端自行构建树。
func GetSkillTree(ctx context.Context, req *SkillTreeReq) (*[]skill.FileNode, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := loadSkillRow(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if row.Confidential && !skillOwnedBy(row, identity) {
		return nil, errors.NewMsg("机密技能仅所有者可查看文件")
	}
	nodes, err := skill.ListWorkingTree(row.DirName)
	if err != nil {
		return nil, err
	}
	return &nodes, nil
}

type SkillFileReq struct {
	ID   int64  `uri:"id" form:"-"`
	Path string `form:"path" binding:"required"`
}

type SkillFileContentOut struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	IsBinary bool   `json:"is_binary"`
}

// GetSkillFile 读取文件 (GET /api/skills/:id/file?path=)；二进制文件返回空内容 + is_binary
func GetSkillFile(ctx context.Context, req *SkillFileReq) (*SkillFileContentOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := loadSkillRow(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if row.Confidential && !skillOwnedBy(row, identity) {
		return nil, errors.NewMsg("机密技能仅所有者可查看文件")
	}
	content, isBinary, err := skill.ReadWorkingFile(row.DirName, req.Path)
	if err != nil {
		return nil, err
	}
	return &SkillFileContentOut{Path: req.Path, Content: content, IsBinary: isBinary}, nil
}

type WriteSkillFileReq struct {
	ID      int64  `uri:"id" form:"-"`
	Path    string `form:"path" binding:"required"`
	Content string `json:"content"`
}

// WriteSkillFile 保存文件 (PUT /api/skills/:id/file?path=)，仅所有者。
// SKILL.md 正文改动会在下次会话轮次生效（工作副本即激活副本）。
func WriteSkillFile(ctx context.Context, req *WriteSkillFileReq) (*StatusOKResp, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := loadOwnedSkillRow(ctx, identity, req.ID)
	if err != nil {
		return nil, err
	}
	if err := skill.WriteWorkingFile(row.DirName, req.Path, req.Content, false); err != nil {
		return nil, err
	}
	return &StatusOKResp{Status: "ok"}, nil
}

// CreateSkillFile 新建文件 (POST /api/skills/:id/file?path=)，仅所有者；已存在报错防覆盖
func CreateSkillFile(ctx context.Context, req *WriteSkillFileReq) (*StatusOKResp, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := loadOwnedSkillRow(ctx, identity, req.ID)
	if err != nil {
		return nil, err
	}
	if err := skill.WriteWorkingFile(row.DirName, req.Path, req.Content, true); err != nil {
		return nil, err
	}
	return &StatusOKResp{Status: "ok"}, nil
}

// DeleteSkillFile 删除文件 (DELETE /api/skills/:id/file?path=)，仅所有者；根 SKILL.md 受保护
func DeleteSkillFile(ctx context.Context, req *SkillFileReq) (*StatusOKResp, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := loadOwnedSkillRow(ctx, identity, req.ID)
	if err != nil {
		return nil, err
	}
	if err := skill.DeleteWorkingFile(row.DirName, req.Path); err != nil {
		return nil, err
	}
	return &StatusOKResp{Status: "ok"}, nil
}

type RenameSkillFileReq struct {
	ID      int64  `uri:"id"`
	OldPath string `json:"old_path" binding:"required"`
	NewPath string `json:"new_path" binding:"required"`
}

// RenameSkillFile 重命名 / 移动文件 (POST /api/skills/:id/file/rename)，仅所有者
func RenameSkillFile(ctx context.Context, req *RenameSkillFileReq) (*StatusOKResp, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := loadOwnedSkillRow(ctx, identity, req.ID)
	if err != nil {
		return nil, err
	}
	if err := skill.RenameWorkingFile(row.DirName, req.OldPath, req.NewPath); err != nil {
		return nil, err
	}
	return &StatusOKResp{Status: "ok"}, nil
}

// ---------- 从空新建 / 下载 ----------

type CreateEmptySkillReq struct {
	Name        string  `json:"name" binding:"required"`
	Description *string `json:"description"`
}

// CreateEmptySkill 从空新建技能 (POST /api/skills)：骨架 SKILL.md + 注册行 + v1 版本，
// 返回后前端直接打开文件页在线编辑。
func CreateEmptySkill(ctx context.Context, req *CreateEmptySkillReq) (*SkillOut, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, errors.NewMsg("技能名不能为空")
	}
	description := ""
	if req.Description != nil {
		description = strings.TrimSpace(*req.Description)
	}
	dirName, err := skill.CreateEmpty(name, description)
	if err != nil {
		return nil, err
	}
	row, err := dao.UpsertUploadedSkill(ctx, dao.UploadedSkillItem{
		Name:        name,
		Description: description,
		DirName:     dirName,
		OwnerID:     identity.UserID,
		OwnerName:   identity.Name,
	})
	if err != nil {
		return nil, err
	}
	if err := dao.CreateSkillVersion(ctx, &model.SkillVersion{
		SkillID:       row.ID,
		VersionNumber: 1,
		Notes:         "",
		CreatedBy:     identity.UserID,
		CreatedByName: identity.Name,
		Source:        "create",
	}); err != nil {
		return nil, err
	}
	out := toSkillOut(row, identity)
	return &out, nil
}

// DownloadSkill 下载技能工作副本 zip (GET /api/skills/:id/download)。
// 响应为 zip 字节流（非信封），Content-Disposition 携带文件名。
func DownloadSkill(ctx context.Context, req *SkillTreeReq) (*apiwarp.FileBytesData, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	row, err := loadSkillRow(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if row.Confidential && !skillOwnedBy(row, identity) {
		return nil, errors.NewMsg("机密技能仅所有者可下载")
	}
	data, err := skill.ZipWorkingTree(row.DirName)
	if err != nil {
		return nil, err
	}
	return &apiwarp.FileBytesData{
		FileType: "application/zip",
		Data:     data,
		Filename: row.Name + ".zip",
	}, nil
}
