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

// TestNewRPCTransactionFromIncompleteMsgGasPrice is a regression test for the negative
// block rewards reported by Blockscout for blocks whose only content is derived txs.
//
// Derived txs are reconstructed with zero fee caps, so reporting their raw price yields
// gasPrice == 0 while the receipt still reports gasUsed > 0. Blockscout derives a block's
// reward as Σ(gas_used * gas_price) − base_fee_per_gas * Σ(gas_used), which goes negative
// for such blocks even though they move no value. A mined derived tx must therefore report
// exactly the block base fee — the price of a tx that adds no priority tip — so that
// arithmetic nets to zero.
func TestNewRPCTransactionFromIncompleteMsgGasPrice(t *testing.T) {
	to := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	txHash := common.BigToHash(big.NewInt(1))
	blockHash := common.BigToHash(big.NewInt(2))
	baseFee := big.NewInt(1_000_000_000) // 1 gwei

	// Derived txs are reconstructed as EIP-1559 txs with zero fee caps.
	newMsg := func() *evmtypes.MsgEthereumTx {
		inner := ethtypes.NewTx(&ethtypes.DynamicFeeTx{
			ChainID:   big.NewInt(1),
			Nonce:     0,
			GasFeeCap: big.NewInt(0),
			GasTipCap: big.NewInt(0),
			Gas:       60000,
			To:        &to,
			Value:     big.NewInt(0),
		})
		msg := &evmtypes.MsgEthereumTx{}
		msg.FromEthereumTx(inner)
		msg.From = common.BytesToAddress([]byte("sender")).Bytes()
		return msg
	}

	t.Run("mined tx reports the block base fee", func(t *testing.T) {
		rpcTx, err := NewRPCTransactionFromIncompleteMsg(
			newMsg(), blockHash, 7, 0, baseFee, big.NewInt(1), txHash,
		)
		require.NoError(t, err)
		require.NotNil(t, rpcTx.GasPrice)
		require.Equal(t, baseFee, rpcTx.GasPrice.ToInt(),
			"gasPrice must be the base fee so tx fees and burnt fees cancel out")
	})

	t.Run("mined tx with unknown base fee falls back to the tx price", func(t *testing.T) {
		rpcTx, err := NewRPCTransactionFromIncompleteMsg(
			newMsg(), blockHash, 7, 0, nil, big.NewInt(1), txHash,
		)
		require.NoError(t, err)
		require.NotNil(t, rpcTx.GasPrice)
		require.Equal(t, big.NewInt(0), rpcTx.GasPrice.ToInt())
	})

	t.Run("unmined tx falls back to the tx price", func(t *testing.T) {
		rpcTx, err := NewRPCTransactionFromIncompleteMsg(
			newMsg(), common.Hash{}, 0, 0, baseFee, big.NewInt(1), txHash,
		)
		require.NoError(t, err)
		require.NotNil(t, rpcTx.GasPrice)
		require.Equal(t, big.NewInt(0), rpcTx.GasPrice.ToInt())
	})

	t.Run("supplied base fee is not aliased", func(t *testing.T) {
		bf := big.NewInt(1_000_000_000)
		rpcTx, err := NewRPCTransactionFromIncompleteMsg(
			newMsg(), blockHash, 7, 0, bf, big.NewInt(1), txHash,
		)
		require.NoError(t, err)
		rpcTx.GasPrice.ToInt().SetInt64(42)
		require.Equal(t, big.NewInt(1_000_000_000), bf, "caller's baseFee must not be mutated")
	})
}
