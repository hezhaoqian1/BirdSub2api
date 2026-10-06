//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminService_GenerateRedeemCodes_PersistsTrimmedNotes(t *testing.T) {
	repo := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
	svc := &adminServiceImpl{redeemCodeRepo: repo}

	codes, err := svc.GenerateRedeemCodes(context.Background(), &GenerateRedeemCodesInput{
		Count: 2,
		Type:  RedeemTypeBalance,
		Value: 5,
		Notes: "  进群福利，感谢支持  ",
	})
	require.NoError(t, err)
	require.Len(t, codes, 2)
	require.Len(t, repo.created, 2)
	for i := range codes {
		require.Equal(t, "进群福利，感谢支持", codes[i].Notes)
		require.Equal(t, "进群福利，感谢支持", repo.created[i].Notes)
	}
}
