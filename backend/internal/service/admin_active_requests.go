package service

import (
	"context"
	"fmt"

	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/user"
)

// ActiveRequestView 是一个在途请求（并发槽位占用）的展示数据。
type ActiveRequestView struct {
	SlotID string `json:"slot_id"`
	// Live 表示 OpenAI Live 会话租约。
	Live bool `json:"live"`
	// Recorded 为 false 表示该槽位没有占用者详情。
	Recorded        bool   `json:"recorded"`
	RequestID       string `json:"request_id,omitempty"`
	ElapsedSeconds  int64  `json:"elapsed_seconds"`
	Model           string `json:"model,omitempty"`
	UserID          int64  `json:"user_id,omitempty"`
	UserEmail       string `json:"user_email,omitempty"`
	Username        string `json:"username,omitempty"`
	APIKeyID        int64  `json:"api_key_id,omitempty"`
	APIKeyName      string `json:"api_key_name,omitempty"`
	GroupID         int64  `json:"group_id,omitempty"`
	GroupName       string `json:"group_name,omitempty"`
	GroupPlatform   string `json:"group_platform,omitempty"`
	AccountID       int64  `json:"account_id,omitempty"`
	AccountName     string `json:"account_name,omitempty"`
	AccountPlatform string `json:"account_platform,omitempty"`
}

// DescribeActiveSlots 把槽位快照补全为可展示的在途请求列表：按 ID 批量查出用户、Key、分组、账号名称，
// 查不到的（已删除等）只保留 ID。弹窗会每几秒刷新一次，所以直接用 ent 只取名称列，
// 不走仓储层加载完整实体（如账号的凭据、分组关联）。
func (s *adminServiceImpl) DescribeActiveSlots(ctx context.Context, snapshot *ActiveSlotSnapshot) ([]ActiveRequestView, error) {
	if snapshot == nil || len(snapshot.Slots) == 0 {
		return []ActiveRequestView{}, nil
	}

	views := make([]ActiveRequestView, 0, len(snapshot.Slots))
	keyIDs := make(map[int64]struct{})
	groupIDs := make(map[int64]struct{})
	accountIDs := make(map[int64]struct{})
	for _, slot := range snapshot.Slots {
		view := ActiveRequestView{
			SlotID:    slot.SlotID,
			Live:      slot.Live,
			Recorded:  slot.Holder != nil,
			AccountID: slot.AccountID,
		}
		// Live 租约的分数是最近一次续租时间，算不出会话时长。
		if elapsed := snapshot.Now - slot.StartedAt; elapsed > 0 && !slot.Live {
			view.ElapsedSeconds = elapsed
		}
		if h := slot.Holder; h != nil {
			view.RequestID = h.RequestID
			view.Model = h.Model
			view.UserID = h.UserID
			view.APIKeyID = h.APIKeyID
			view.GroupID = h.GroupID
		}
		addPositiveID(keyIDs, view.APIKeyID)
		addPositiveID(groupIDs, view.GroupID)
		addPositiveID(accountIDs, view.AccountID)
		views = append(views, view)
	}

	keys, err := s.activeRequestAPIKeys(ctx, keyIDs)
	if err != nil {
		return nil, err
	}
	userIDs := make(map[int64]struct{})
	for i := range views {
		// 详情里没记用户时从 Key 反查。
		if views[i].UserID <= 0 {
			if key := keys[views[i].APIKeyID]; key != nil {
				views[i].UserID = key.UserID
			}
		}
		addPositiveID(userIDs, views[i].UserID)
	}
	users, err := s.activeRequestUsers(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	groups, err := s.activeRequestGroups(ctx, groupIDs)
	if err != nil {
		return nil, err
	}
	accounts, err := s.activeRequestAccounts(ctx, accountIDs)
	if err != nil {
		return nil, err
	}

	for i := range views {
		v := &views[i]
		if u := users[v.UserID]; u != nil {
			v.UserEmail = u.Email
			v.Username = u.Username
		}
		if k := keys[v.APIKeyID]; k != nil {
			v.APIKeyName = k.Name
		}
		if g := groups[v.GroupID]; g != nil {
			v.GroupName = g.Name
			v.GroupPlatform = g.Platform
		}
		if a := accounts[v.AccountID]; a != nil {
			v.AccountName = a.Name
			v.AccountPlatform = a.Platform
		}
	}
	return views, nil
}

func addPositiveID(set map[int64]struct{}, id int64) {
	if id > 0 {
		set[id] = struct{}{}
	}
}

func idList(set map[int64]struct{}) []int64 {
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	return ids
}

type activeRequestKey struct {
	UserID int64
	Name   string
}

func (s *adminServiceImpl) activeRequestAPIKeys(ctx context.Context, ids map[int64]struct{}) (map[int64]*activeRequestKey, error) {
	out := make(map[int64]*activeRequestKey, len(ids))
	if len(ids) == 0 || s.entClient == nil {
		return out, nil
	}
	rows, err := s.entClient.APIKey.Query().
		Where(apikey.IDIn(idList(ids)...)).
		Select(apikey.FieldID, apikey.FieldUserID, apikey.FieldName).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query api keys: %w", err)
	}
	for _, row := range rows {
		out[row.ID] = &activeRequestKey{UserID: row.UserID, Name: row.Name}
	}
	return out, nil
}

type activeRequestUser struct {
	Email    string
	Username string
}

func (s *adminServiceImpl) activeRequestUsers(ctx context.Context, ids map[int64]struct{}) (map[int64]*activeRequestUser, error) {
	out := make(map[int64]*activeRequestUser, len(ids))
	if len(ids) == 0 || s.entClient == nil {
		return out, nil
	}
	rows, err := s.entClient.User.Query().
		Where(user.IDIn(idList(ids)...)).
		Select(user.FieldID, user.FieldEmail, user.FieldUsername).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query users: %w", err)
	}
	for _, row := range rows {
		out[row.ID] = &activeRequestUser{Email: row.Email, Username: row.Username}
	}
	return out, nil
}

// activeRequestNamed 用于分组和账号：只需要名称和平台。
type activeRequestNamed struct {
	Name     string
	Platform string
}

func (s *adminServiceImpl) activeRequestGroups(ctx context.Context, ids map[int64]struct{}) (map[int64]*activeRequestNamed, error) {
	out := make(map[int64]*activeRequestNamed, len(ids))
	if len(ids) == 0 || s.entClient == nil {
		return out, nil
	}
	rows, err := s.entClient.Group.Query().
		Where(group.IDIn(idList(ids)...)).
		Select(group.FieldID, group.FieldName, group.FieldPlatform).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query groups: %w", err)
	}
	for _, row := range rows {
		out[row.ID] = &activeRequestNamed{Name: row.Name, Platform: row.Platform}
	}
	return out, nil
}

func (s *adminServiceImpl) activeRequestAccounts(ctx context.Context, ids map[int64]struct{}) (map[int64]*activeRequestNamed, error) {
	out := make(map[int64]*activeRequestNamed, len(ids))
	if len(ids) == 0 || s.entClient == nil {
		return out, nil
	}
	rows, err := s.entClient.Account.Query().
		Where(dbaccount.IDIn(idList(ids)...)).
		Select(dbaccount.FieldID, dbaccount.FieldName, dbaccount.FieldPlatform).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query accounts: %w", err)
	}
	for _, row := range rows {
		out[row.ID] = &activeRequestNamed{Name: row.Name, Platform: row.Platform}
	}
	return out, nil
}
