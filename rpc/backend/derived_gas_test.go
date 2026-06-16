package backend

import (
	"testing"

	"github.com/stretchr/testify/require"

	rpctypes "github.com/cosmos/evm/rpc/types"
)

// TestGasForDerivedEthTx is a regression test for F-2026-17752 (derived RPC transaction
// used GasUsed as the gas field). A derived tx's RPC `gas` field must report the declared
// gas limit, not gas consumed. gasForDerivedEthTx prefers GasLimit when set, falls back to
// GasUsed*2 for older txs that predate GasLimit emission, and yields 0 when neither exists.
func TestGasForDerivedEthTx(t *testing.T) {
	u := func(v uint64) *uint64 { return &v }
	cases := []struct {
		name     string
		limit    *uint64
		used     uint64
		expected uint64
	}{
		{"prefers gas limit when present", u(60_000), 21_000, 60_000},
		{"nil gas limit falls back to 2x gas used", nil, 21_000, 42_000},
		{"zero gas limit falls back to 2x gas used", u(0), 21_000, 42_000},
		{"no limit and no gas used yields zero", nil, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gasForDerivedEthTx(&rpctypes.TxResultAdditionalFields{
				GasLimit: tc.limit,
				GasUsed:  tc.used,
			})
			require.Equal(t, tc.expected, got)
		})
	}
}
