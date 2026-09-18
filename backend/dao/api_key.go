package dao

import (
	"context"
	"strings"

	"backend/dal/model"
	"backend/pkg/db"
	"backend/pkg/errors"

	"gorm.io/gorm"
)

// GetApiKey 根据 api_key 凭证字符串检索已启用的 API 密钥记录
func GetApiKey(ctx context.Context, apiKeyStr string) (*model.ApiKey, error) {
	keyStr := strings.TrimSpace(apiKeyStr)
	if keyStr == "" {
		return nil, errors.NewMsg("API Key 不能为空")
	}

	q := db.Ctx(ctx)
	ak := q.ApiKey

	record, err := ak.WithContext(ctx).
		Where(ak.APIKey.Eq(keyStr), ak.IsActive.Is(true)).
		First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.NewMsg("无效的 API Key 或密钥已被停用")
		}
		return nil, errors.Join(err, errors.New("查询 API Key 失败"), errors.NewMsg("系统异常"))
	}

	return record, nil
}

// CountActiveRunsByKey 统计指定 API Key 当前处于排队中(pending)或执行中(running)的任务总数（并发守卫）
func CountActiveRunsByKey(ctx context.Context, apiKeyID int64) (int64, error) {
	if apiKeyID <= 0 {
		return 0, nil
	}

	q := db.Ctx(ctx)
	sr := q.SkillRun

	count, err := sr.WithContext(ctx).
		Where(
			sr.APIKeyID.Eq(apiKeyID),
			sr.Status.In("pending", "running"),
		).
		Count()
	if err != nil {
		return 0, errors.Join(err, errors.New("统计活跃任务数失败"), errors.NewMsg("系统异常"))
	}

	return count, nil
}

// CreateApiKey 新建 API 密钥（供测试与系统管理使用）
func CreateApiKey(ctx context.Context, key *model.ApiKey) error {
	if key == nil || strings.TrimSpace(key.APIKey) == "" {
		return errors.NewMsg("API Key 不能为空")
	}

	q := db.Ctx(ctx)
	if err := q.ApiKey.WithContext(ctx).Create(key); err != nil {
		return errors.Join(err, errors.New("创建 API Key 失败"), errors.NewMsg("系统异常"))
	}
	return nil
}

// ListApiKeysByCompany 按企业查询关联的 API Key 列表
func ListApiKeysByCompany(ctx context.Context, companyID int64) ([]*model.ApiKey, error) {
	q := db.Ctx(ctx)
	ak := q.ApiKey

	list, err := ak.WithContext(ctx).
		Where(ak.CompanyID.Eq(companyID)).
		Order(ak.CreatedAt.Desc()).
		Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("查询企业 API Key 失败"), errors.NewMsg("系统异常"))
	}
	return list, nil
}
