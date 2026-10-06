package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestRedeemCodeFromService_ExposesNotesForAllTypes(t *testing.T) {
	for _, typ := range []string{service.RedeemTypeBalance, "admin_balance", "subscription"} {
		out := RedeemCodeFromService(&service.RedeemCode{Code: "R-1", Type: typ, Notes: "进群福利"})
		require.NotNil(t, out.Notes, typ)
		require.Equal(t, "进群福利", *out.Notes, typ)
	}

	empty := RedeemCodeFromService(&service.RedeemCode{Code: "R-2", Type: service.RedeemTypeBalance})
	require.Nil(t, empty.Notes, "empty notes must be omitted")
}
