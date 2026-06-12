package types

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/stretchr/testify/require"

	evmtypes "github.com/cosmos/evm/x/vm/types"
)

// TestNewRPCTransactionFromIncompleteMsgGas is a regression test for F-2026-17752.
//
// A derived tx's reconstructed RPC `gas` field must report the transaction gas limit —
// consistent with standard txs (NewRPCTransaction uses tx.Gas()) and the Ethereum
// JSON-RPC spec — not the consumed gas (which belongs in the receipt `gasUsed`). It falls
// back to GasUsed only for legacy derived txs that recorded no txGasLimit attribute.
func TestNewRPCTransactionFromIncompleteMsgGas(t *testing.T) {
	const gasUsed = uint64(21000)
	limit := uint64(60000)
	zero := uint64(0)

	msg := &evmtypes.MsgEthereumTx{
		From: common.BytesToAddress([]byte("sender")).Hex(),
		Hash: common.BigToHash(big.NewInt(1)).Hex(),
	}

	testCases := []struct {
		name     string
		gasLimit *uint64
		expGas   hexutil.Uint64
	}{
		{"gas limit present -> reports the limit", &limit, hexutil.Uint64(limit)},
		{"gas limit nil -> falls back to gasUsed", nil, hexutil.Uint64(gasUsed)},
		{"gas limit zero -> falls back to gasUsed", &zero, hexutil.Uint64(gasUsed)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			additional := &TxResultAdditionalFields{
				GasUsed:  gasUsed,
				GasLimit: tc.gasLimit,
				Value:    big.NewInt(0),
			}

			rpcTx, err := NewRPCTransactionFromIncompleteMsg(
				msg, common.Hash{}, 0, 0, big.NewInt(1), big.NewInt(1), additional,
			)
			require.NoError(t, err)
			require.Equal(t, tc.expGas, rpcTx.Gas)
		})
	}
}
