package backend

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"

	evmtypes "github.com/cosmos/evm/x/vm/types"
)

// TestDerivedTxLogsFromEvents is a regression test for F-2026-17775 (inconsistent log
// reporting for failed derived transactions across JSON-RPC endpoints). Both the
// single-receipt path (GetTransactionReceipt) and the block-receipt path
// (ReceiptsFromCometBlock) resolve derived-tx logs through this one helper, which matches
// tx_log events by TxHash — so a derived tx reports the same logs on every endpoint, and a
// failed derived tx (empty tx_log) consistently reports none.
func TestDerivedTxLogsFromEvents(t *testing.T) {
	txHash := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000aa")
	otherHash := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000bb")

	mkLogEvent := func(logTxHash common.Hash, addr common.Address) abci.Event {
		bz, err := json.Marshal(&evmtypes.Log{Address: addr.Hex(), TxHash: logTxHash.Hex()})
		require.NoError(t, err)
		return abci.Event{Type: evmtypes.EventTypeTxLog, Attributes: []abci.EventAttribute{
			{Key: evmtypes.AttributeKeyTxLog, Value: string(bz)},
		}}
	}

	t.Run("successful derived tx returns its matching log", func(t *testing.T) {
		addr := common.HexToAddress("0x00000000000000000000000000000000000000c0")
		logs, err := derivedTxLogsFromEvents([]abci.Event{mkLogEvent(txHash, addr)}, txHash, 7)
		require.NoError(t, err)
		require.Len(t, logs, 1)
		require.Equal(t, addr, logs[0].Address)
		require.Equal(t, txHash, logs[0].TxHash)
		require.Equal(t, uint64(7), logs[0].BlockNumber)
	})

	t.Run("failed derived tx (empty tx_log) returns no logs and no error", func(t *testing.T) {
		// A failed derived execution still emits a (paired) tx_log event, but with no logs.
		events := []abci.Event{{Type: evmtypes.EventTypeTxLog, Attributes: []abci.EventAttribute{}}}
		logs, err := derivedTxLogsFromEvents(events, txHash, 7)
		require.NoError(t, err)
		require.Empty(t, logs)
	})

	t.Run("logs are matched by tx hash, no cross-tx leakage", func(t *testing.T) {
		// Another derived tx's log in the same block must not be attributed to txHash.
		events := []abci.Event{mkLogEvent(otherHash, common.HexToAddress("0x00000000000000000000000000000000000000d0"))}
		logs, err := derivedTxLogsFromEvents(events, txHash, 7)
		require.NoError(t, err)
		require.Empty(t, logs)
	})
}
