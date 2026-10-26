package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ActiveRequestsResponse 是用户/账号"实时并发"弹窗的数据：当前占用、上限、排队数和在途请求明细。
type ActiveRequestsResponse struct {
	Current int                         `json:"current"`
	Max     int                         `json:"max"`
	Waiting int                         `json:"waiting"`
	Items   []service.ActiveRequestView `json:"items"`
}

// GetActiveRequests 查看用户当前占用并发的在途请求（分组、Key、模型、所在账号）。
// GET /api/v1/admin/users/:id/active-requests
func (h *UserHandler) GetActiveRequests(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}
	ctx := c.Request.Context()
	user, err := h.adminService.GetUser(ctx, userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := ActiveRequestsResponse{Max: user.Concurrency, Items: []service.ActiveRequestView{}}
	if h.concurrencyService == nil {
		response.Success(c, out)
		return
	}
	if loads, loadErr := h.concurrencyService.GetUsersLoadBatch(ctx, []service.UserWithConcurrency{{ID: userID, MaxConcurrency: user.Concurrency}}); loadErr == nil {
		if load := loads[userID]; load != nil {
			out.Waiting = load.WaitingCount
		}
	}
	// 并发不限的用户不占用户槽位，要从账号槽位里找回他的请求。
	snapshot, err := h.concurrencyService.ListUserSlotHolders(ctx, userID, user.Concurrency <= 0)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// 与明细同口径计数，不限并发的用户也能看到实际在跑多少个。
	out.Current = len(snapshot.Slots)
	if out.Items, err = h.adminService.DescribeActiveSlots(ctx, snapshot); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}

// GetActiveRequests 查看账号当前占用并发的在途请求（用户、Key、分组、模型）。
// GET /api/v1/admin/accounts/:id/active-requests
func (h *AccountHandler) GetActiveRequests(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	ctx := c.Request.Context()
	account, err := h.adminService.GetAccount(ctx, accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := ActiveRequestsResponse{Max: account.Concurrency, Items: []service.ActiveRequestView{}}
	if h.concurrencyService == nil {
		response.Success(c, out)
		return
	}
	if waiting, waitErr := h.concurrencyService.GetAccountWaitingCount(ctx, accountID); waitErr == nil {
		out.Waiting = waiting
	}
	snapshot, err := h.concurrencyService.ListAccountSlotHolders(ctx, accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out.Current = len(snapshot.Slots)
	if out.Items, err = h.adminService.DescribeActiveSlots(ctx, snapshot); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}
