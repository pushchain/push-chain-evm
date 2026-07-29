package backend

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtrpctypes "github.com/cometbft/cometbft/rpc/core/types"

	evmtypes "github.com/cosmos/evm/x/vm/types"
)

// TestReindexBlockLogs covers the block-global logIndex pass added for the
// cosmos/evm v0.7.0 upgrade.
//
// Background: v0.7.0 changed statedb.AddLog from
// `log.Index = txConfig.LogIndex + len(s.logs)` (block-global) to
// `log.Index = len(s.logs)` (tx-local), and moved block-global numbering into
// types.PatchTxResponses. That pass only rewrites logs carried in a
// MsgEthereumTxResponse, so it never reaches push-chain's derived txs, whose
// logs are serialized into `tx_log` ABCI events from inside a Cosmos message.
// Without ReindexBlockLogs a block with derived txs emits duplicate logIndex
// values, which is invalid per Ethereum semantics (logIndex is the position of
// the log within the *block*).
func TestReindexBlockLogs(t *testing.T) {
	mkLog := func(idx uint) *ethtypes.Log {
		return &ethtypes.Log{Index: idx}
	}

	t.Run("derived txs with tx-local indices are renumbered block-globally", func(t *testing.T) {
		// Two derived txs, each having restarted its log numbering at 0.
		blockLogs := [][]*ethtypes.Log{
			{mkLog(0), mkLog(1)},
			{mkLog(0), mkLog(1)},
		}

		ReindexBlockLogs(blockLogs)

		require.Equal(t, uint(0), blockLogs[0][0].Index)
		require.Equal(t, uint(1), blockLogs[0][1].Index)
		require.Equal(t, uint(2), blockLogs[1][0].Index, "second tx must continue the block sequence, not restart at 0")
		require.Equal(t, uint(3), blockLogs[1][1].Index)
	})

	t.Run("indices are unique across the whole block", func(t *testing.T) {
		blockLogs := [][]*ethtypes.Log{
			{mkLog(0)},
			{mkLog(0), mkLog(1), mkLog(2)},
			{},
			{mkLog(0), mkLog(1)},
		}

		ReindexBlockLogs(blockLogs)

		seen := map[uint]bool{}
		var total int
		for _, logs := range blockLogs {
			for _, log := range logs {
				require.False(t, seen[log.Index], "duplicate logIndex %d", log.Index)
				seen[log.Index] = true
				total++
			}
		}
		require.Len(t, seen, total)
		for i := 0; i < total; i++ {
			require.True(t, seen[uint(i)], "logIndex %d missing — sequence must be contiguous from 0", i) //#nosec G115
		}
	})

	t.Run("native-only block is left unchanged (no-op in effect)", func(t *testing.T) {
		// PatchTxResponses already numbers native logs block-globally; the pass
		// must reproduce exactly the same sequence.
		blockLogs := [][]*ethtypes.Log{
			{mkLog(0), mkLog(1)},
			{mkLog(2)},
			{mkLog(3), mkLog(4)},
		}

		ReindexBlockLogs(blockLogs)

		require.Equal(t, uint(0), blockLogs[0][0].Index)
		require.Equal(t, uint(1), blockLogs[0][1].Index)
		require.Equal(t, uint(2), blockLogs[1][0].Index)
		require.Equal(t, uint(3), blockLogs[2][0].Index)
		require.Equal(t, uint(4), blockLogs[2][1].Index)
	})

	t.Run("empty block and nil logs are safe", func(t *testing.T) {
		require.NotPanics(t, func() { ReindexBlockLogs(nil) })
		require.NotPanics(t, func() { ReindexBlockLogs([][]*ethtypes.Log{}) })
		require.NotPanics(t, func() { ReindexBlockLogs([][]*ethtypes.Log{{nil}}) })

		// A nil entry must not consume an index.
		blockLogs := [][]*ethtypes.Log{{nil, mkLog(9)}}
		ReindexBlockLogs(blockLogs)
		require.Equal(t, uint(0), blockLogs[0][1].Index)
	})
}

// TestGetLogsFromBlockResultsDerivedLogIndex is the end-to-end regression test:
// a block whose logs come only from derived txs (tx_log ABCI events, no
// MsgEthereumTxResponse) must still report unique, contiguous logIndex values.
func TestGetLogsFromBlockResultsDerivedLogIndex(t *testing.T) {
	txHashA := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000aa")
	txHashB := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000bb")

	// Derived logs are marshaled at emit time with tx-local Index values, which is
	// exactly what this pass has to correct.
	mkLogEvent := func(logTxHash common.Hash, localIndex uint64) abci.Event {
		bz, err := json.Marshal(&evmtypes.Log{
			Address: common.HexToAddress("0x00000000000000000000000000000000000000c0").Hex(),
			TxHash:  logTxHash.Hex(),
			Index:   localIndex,
		})
		require.NoError(t, err)
		return abci.Event{Type: evmtypes.EventTypeTxLog, Attributes: []abci.EventAttribute{
			{Key: evmtypes.AttributeKeyTxLog, Value: string(bz)},
		}}
	}

	blockRes := &cmtrpctypes.ResultBlockResults{
		Height: 7,
		TxsResults: []*abci.ExecTxResult{
			// Cosmos tx 0: one derived tx emitting 2 logs (local 0,1)
			{Events: []abci.Event{mkLogEvent(txHashA, 0), mkLogEvent(txHashA, 1)}},
			// Cosmos tx 1: another derived tx, log numbering restarted at 0
			{Events: []abci.Event{mkLogEvent(txHashB, 0)}},
		},
	}

	blockLogs, err := GetLogsFromBlockResults(blockRes)
	require.NoError(t, err)
	require.Len(t, blockLogs, 2)

	var flat []*ethtypes.Log
	for _, logs := range blockLogs {
		flat = append(flat, logs...)
	}
	require.Len(t, flat, 3)

	// Without the fix these would be 0, 1, 0 — a duplicate logIndex in one block.
	require.Equal(t, uint(0), flat[0].Index)
	require.Equal(t, uint(1), flat[1].Index)
	require.Equal(t, uint(2), flat[2].Index, "logs of the second derived tx must continue the block sequence")

	for _, log := range flat {
		require.Equal(t, uint64(7), log.BlockNumber)
	}
}
