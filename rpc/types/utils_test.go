package types

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"

	evmtypes "github.com/cosmos/evm/x/vm/types"
)

// TestNewRPCTransactionFromIncompleteMsgGas is a regression test for F-2026-17752 (derived
// RPC transaction used GasUsed as the gas field).
//
// A derived tx's reconstructed RPC `gas` field must report the transaction gas limit —
// consistent with standard txs (NewRPCTransaction uses tx.Gas()) and the Ethereum JSON-RPC
// spec — not the consumed gas (which belongs in the receipt `gasUsed`). On v0.5.x the
// limit-vs-gasUsed resolution lives in the backend reconstruction (gasForDerivedEthTx,
// covered by rpc/backend/derived_gas_test.go); this asserts the RPC builder faithfully
// reports the reconstructed tx's gas limit and the supplied tx hash.
func TestNewRPCTransactionFromIncompleteMsgGas(t *testing.T) {
	const gasLimit = uint64(60000)
	sender := common.BytesToAddress([]byte("sender"))
	to := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	txHash := common.BigToHash(big.NewInt(1))

	// Build a valid incomplete derived msg whose reconstructed tx carries the gas limit.
	inner := ethtypes.NewTx(&ethtypes.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(1),
		Gas:      gasLimit,
		To:       &to,
		Value:    big.NewInt(0),
	})
	msg := &evmtypes.MsgEthereumTx{}
	msg.FromEthereumTx(inner)
	msg.From = sender.Bytes()

	rpcTx, err := NewRPCTransactionFromIncompleteMsg(
		msg, common.Hash{}, 0, 0, big.NewInt(1), big.NewInt(1), txHash,
	)
	require.NoError(t, err)
	require.Equal(t, hexutil.Uint64(gasLimit), rpcTx.Gas, "gas field must report the tx gas limit, not gasUsed")
	require.Equal(t, txHash, rpcTx.Hash, "hash must be the supplied derived tx hash")
	require.Equal(t, sender, rpcTx.From)
}
