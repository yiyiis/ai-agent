package dao_test

import (
	"fmt"
	"testing"
	"time"

	"backend/constants"
	"backend/dal/model"
	"backend/dao"
	"backend/pkg/jwt"
)

func TestCompanyAndRBAC(t *testing.T) {
	ctx := setupTestDB(t)

	// 1. 验证默认企业就绪
	defComp, err := dao.EnsureDefaultCompany(ctx)
	if err != nil {
		t.Fatalf("EnsureDefaultCompany failed: %v", err)
	}
	if defComp.Code != "default" {
		t.Fatalf("Expected default company code 'default', got %s", defComp.Code)
	}

	// 2. 创建测试企业 A 与 企业 B
	suffix := time.Now().UnixNano()
	codeA := fmt.Sprintf("corp_a_%d", suffix)
	compA := &model.Company{
		Name:   "测试企业 Alpha",
		Code:   codeA,
		Status: 1,
	}
	if err := dao.CreateCompany(ctx, compA); err != nil {
		t.Fatalf("CreateCompany Alpha failed: %v", err)
	}

	codeB := fmt.Sprintf("corp_b_%d", suffix)
	compB := &model.Company{
		Name:   "测试企业 Beta",
		Code:   codeB,
		Status: 1,
	}
	if err := dao.CreateCompany(ctx, compB); err != nil {
		t.Fatalf("CreateCompany Beta failed: %v", err)
	}

	// 3. 验证跨企业可注册相同用户名 (zhangsan)
	sameUsername := fmt.Sprintf("user_%d", suffix)
	userA, err := dao.AddUserWithCompany(ctx, sameUsername, "张三A", "pass123", "13800000001", constants.User, compA.ID)
	if err != nil {
		t.Fatalf("Register user in Company A failed: %v", err)
	}
	if userA.CompanyID != compA.ID {
		t.Fatalf("Expected CompanyID %d, got %d", compA.ID, userA.CompanyID)
	}

	userB, err := dao.AddUserWithCompany(ctx, sameUsername, "张三B", "pass456", "13800000002", constants.User, compB.ID)
	if err != nil {
		t.Fatalf("Register same username in Company B should succeed, but failed: %v", err)
	}
	if userB.CompanyID != compB.ID {
		t.Fatalf("Expected CompanyID %d, got %d", compB.ID, userB.CompanyID)
	}

	// 4. 验证同企业内注册相同用户名必然报错
	_, err = dao.AddUserWithCompany(ctx, sameUsername, "张三A重名", "pass789", "13800000003", constants.User, compA.ID)
	if err == nil {
		t.Fatalf("Registering same username in same company should fail with duplicate error")
	}

	// 5. 验证 GetUserByUsernameAndCompany 精准查找
	foundA, err := dao.GetUserByUsernameAndCompany(ctx, sameUsername, compA.ID)
	if err != nil || foundA.UserID != userA.UserID {
		t.Fatalf("GetUserByUsernameAndCompany A failed, expected UserID %d, got %v", userA.UserID, foundA)
	}
	foundB, err := dao.GetUserByUsernameAndCompany(ctx, sameUsername, compB.ID)
	if err != nil || foundB.UserID != userB.UserID {
		t.Fatalf("GetUserByUsernameAndCompany B failed, expected UserID %d, got %v", userB.UserID, foundB)
	}

	// 6. 验证三级 RBAC 权限判定 (CheckSessionAccess 与 CheckSessionWriteAccess)
	// 构建模拟身份
	superAdmin := jwt.TokenClaims{UserID: 1, CompanyID: 1, UserType: constants.SuperManager}
	managerA := jwt.TokenClaims{UserID: 10, CompanyID: compA.ID, UserType: constants.Manager}
	managerB := jwt.TokenClaims{UserID: 20, CompanyID: compB.ID, UserType: constants.Manager}
	workerA := jwt.TokenClaims{UserID: int64(userA.UserID), CompanyID: compA.ID, UserType: constants.User}

	// 员工 A 的会话
	sessWorkerA := &model.Session{
		SessionID: fmt.Sprintf("sess_a_%d", suffix),
		CompanyID: compA.ID,
		UserID:    int64(userA.UserID),
	}
	// 企业 A 的无头/API 自动化会话
	sessHeadlessA := &model.Session{
		SessionID: fmt.Sprintf("sess_headless_a_%d", suffix),
		CompanyID: compA.ID,
		UserID:    0,
	}

	// 读权限验证：
	// workerA 读自己的会话 -> OK
	if err := dao.CheckSessionAccess(workerA, sessWorkerA); err != nil {
		t.Fatalf("workerA reading own session should succeed, got: %v", err)
	}
	// workerA 读 headlessA 会话 (user_id=0) -> 拒绝
	if err := dao.CheckSessionAccess(workerA, sessHeadlessA); err == nil {
		t.Fatalf("workerA reading headlessA should be rejected")
	}
	// managerA 读同企业员工会话与无头会话 -> OK
	if err := dao.CheckSessionAccess(managerA, sessWorkerA); err != nil {
		t.Fatalf("managerA reading company session should succeed, got: %v", err)
	}
	if err := dao.CheckSessionAccess(managerA, sessHeadlessA); err != nil {
		t.Fatalf("managerA reading headlessA should succeed, got: %v", err)
	}
	// managerB 读企业 A 会话 -> 拒绝
	if err := dao.CheckSessionAccess(managerB, sessWorkerA); err == nil {
		t.Fatalf("managerB reading company A session should be rejected")
	}
	// superAdmin 读任意企业会话 -> OK
	if err := dao.CheckSessionAccess(superAdmin, sessWorkerA); err != nil {
		t.Fatalf("superAdmin reading any session should succeed, got: %v", err)
	}

	// 写权限验证：
	// workerA 向自己会话写 -> OK
	if err := dao.CheckSessionWriteAccess(workerA, sessWorkerA); err != nil {
		t.Fatalf("workerA writing to own session should succeed, got: %v", err)
	}
	// workerA 向无头会话写 -> 拒绝
	if err := dao.CheckSessionWriteAccess(workerA, sessHeadlessA); err == nil {
		t.Fatalf("workerA writing to headlessA should be rejected")
	}
	// managerA 向自己企业无头会话写 -> OK
	if err := dao.CheckSessionWriteAccess(managerA, sessHeadlessA); err != nil {
		t.Fatalf("managerA writing to company headless session should succeed, got: %v", err)
	}
	// managerB 向企业 A 无头会话写 -> 拒绝
	if err := dao.CheckSessionWriteAccess(managerB, sessHeadlessA); err == nil {
		t.Fatalf("managerB writing to company A headless session should be rejected")
	}
	// superAdmin 向任意会话写 -> OK
	if err := dao.CheckSessionWriteAccess(superAdmin, sessHeadlessA); err != nil {
		t.Fatalf("superAdmin writing should succeed, got: %v", err)
	}
}
