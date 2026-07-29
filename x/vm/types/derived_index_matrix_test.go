package types_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"

	evmtypes "github.com/cosmos/evm/x/vm/types"
	"github.com/cosmos/gogoproto/proto"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// This file is a combination matrix for the two block-global counters that
// PatchTxResponses owns:
//
//   - the eth-tx rank  (ethereum_tx event AttributeKeyTxIndex, and log.TxIndex)
//   - the log index    (log.Index, cumulative across the whole block)
//
// push-chain has two kinds of EVM transaction that must share one rank space and
// one log space:
//
//   - native  — a MsgEthereumTx; logs travel in a MsgEthereumTxResponse (res.Data)
//   - derived — executed inside a cosmos message (x/uexecutor); logs travel in
//     tx_log ABCI events, and there is no MsgEthereumTxResponse at all
//
// Both are emitted with purely tx-local numbers (statedb.AddLog restarts
// log.Index at 0 per StateDB and sets log.TxIndex to the cosmos position), so
// every value asserted here is one that PatchTxResponses must have rewritten.

// ---------------------------------------------------------------------------
// block-level extraction + invariants
// ---------------------------------------------------------------------------

// ethRanks returns the AttributeKeyTxIndex of every ethereum_tx event in a
// result, in emission order.
func ethRanks(t *testing.T, res *abci.ExecTxResult) []uint64 {
	t.Helper()
	var out []uint64
	for _, ev := range res.Events {
		if ev.Type != evmtypes.EventTypeEthereumTx {
			continue
		}
		for _, attr := range ev.Attributes {
			if attr.Key != evmtypes.AttributeKeyTxIndex {
				continue
			}
			v, err := strconv.ParseUint(attr.Value, 10, 64)
			require.NoError(t, err)
			out = append(out, v)
			break
		}
	}
	return out
}

// blockLog is one EVM log as it would be surfaced to JSON-RPC, tagged with the
// kind of tx that produced it.
type blockLog struct {
	Index   uint64
	TxIndex uint64
	Kind    string // "native" | "derived"
}

// collectBlockLogs walks a patched block in order and returns every log from
// both carriers: MsgEthereumTxResponse payloads (native) and tx_log events
// (derived).
func collectBlockLogs(t *testing.T, results []*abci.ExecTxResult) []blockLog {
	t.Helper()
	var out []blockLog
	for _, res := range results {
		// native logs
		if len(res.Data) > 0 {
			if resp := tryUnmarshalEthResponse(t, res); resp != nil {
				for _, l := range resp.Logs {
					out = append(out, blockLog{Index: l.Index, TxIndex: l.TxIndex, Kind: "native"})
				}
			}
		}
		// derived logs
		for _, ev := range res.Events {
			if ev.Type != evmtypes.EventTypeTxLog {
				continue
			}
			for _, attr := range ev.Attributes {
				if attr.Key != evmtypes.AttributeKeyTxLog {
					continue
				}
				var l evmtypes.Log
				require.NoError(t, json.Unmarshal([]byte(attr.Value), &l))
				out = append(out, blockLog{Index: l.Index, TxIndex: l.TxIndex, Kind: "derived"})
			}
		}
	}
	return out
}

// tryUnmarshalEthResponse returns the first MsgEthereumTxResponse in a result,
// or nil when the result carries none (a derived-only or non-EVM cosmos tx).
func tryUnmarshalEthResponse(_ *testing.T, res *abci.ExecTxResult) *evmtypes.MsgEthereumTxResponse {
	var txMsgData sdk.TxMsgData
	if err := proto.Unmarshal(res.Data, &txMsgData); err != nil {
		return nil
	}
	if len(txMsgData.MsgResponses) == 0 {
		return nil
	}
	var response evmtypes.MsgEthereumTxResponse
	if txMsgData.MsgResponses[0].TypeUrl != "/"+proto.MessageName(&response) {
		return nil
	}
	if err := proto.Unmarshal(txMsgData.MsgResponses[0].Value, &response); err != nil {
		return nil
	}
	return &response
}

// assertLogIndicesContiguous asserts the block's logIndex values are exactly
// 0..n-1 in order — the Ethereum definition of logIndex (unique within the
// block). A duplicate here is the exact bug this suite guards.
func assertLogIndicesContiguous(t *testing.T, logs []blockLog) {
	t.Helper()
	for i, l := range logs {
		require.Equal(t, uint64(i), l.Index, //#nosec G115
			"log %d (%s) has logIndex %d; block logIndex must be contiguous from 0", i, l.Kind, l.Index)
	}
}

// assertRanksContiguous asserts the eth-tx ranks across the block are 0..n-1:
// every eth tx — native or derived — consumes exactly one rank, none reused.
func assertRanksContiguous(t *testing.T, ranks []uint64) {
	t.Helper()
	for i, r := range ranks {
		require.Equal(t, uint64(i), r, //#nosec G115
			"eth tx %d has rank %d; ranks must be contiguous from 0 with no reuse", i, r)
	}
}

// allRanks flattens the eth-tx ranks of a whole block, in order.
func allRanks(t *testing.T, results []*abci.ExecTxResult) []uint64 {
	t.Helper()
	var out []uint64
	for _, res := range results {
		out = append(out, ethRanks(t, res)...)
	}
	return out
}

// ---------------------------------------------------------------------------
// scenarios
// ---------------------------------------------------------------------------

func TestDerivedIndexMatrix(t *testing.T) {
	t.Run("one cosmos tx with several derived txs", func(t *testing.T) {
		// 3 derived txs in a single cosmos message, 2 + 0 + 1 logs.
		events := derivedTxEvents(t, "0xa1", 0, 2)
		events = append(events, derivedTxEvents(t, "0xa2", 0, 0)...)
		events = append(events, derivedTxEvents(t, "0xa3", 0, 1)...)

		results, err := evmtypes.PatchTxResponses([]*abci.ExecTxResult{derivedTxResult(t, events)})
		require.NoError(t, err)

		assertRanksContiguous(t, allRanks(t, results)) // 0,1,2 — the zero-log tx still takes one
		logs := collectBlockLogs(t, results)
		require.Len(t, logs, 3)
		assertLogIndicesContiguous(t, logs)

		// logs belong to the 1st and 3rd derived tx → ranks 0 and 2
		require.Equal(t, uint64(0), logs[0].TxIndex)
		require.Equal(t, uint64(0), logs[1].TxIndex)
		require.Equal(t, uint64(2), logs[2].TxIndex)
	})

	t.Run("several cosmos txs each carrying derived txs", func(t *testing.T) {
		r0 := derivedTxResult(t, derivedTxEvents(t, "0xb1", 0, 1))
		r1 := derivedTxResult(t, derivedTxEvents(t, "0xb2", 1, 2))
		r2 := derivedTxResult(t, derivedTxEvents(t, "0xb3", 2, 1))

		results, err := evmtypes.PatchTxResponses([]*abci.ExecTxResult{r0, r1, r2})
		require.NoError(t, err)

		assertRanksContiguous(t, allRanks(t, results)) // 0,1,2
		logs := collectBlockLogs(t, results)
		require.Len(t, logs, 4)
		assertLogIndicesContiguous(t, logs) // 0,1,2,3 across cosmos tx boundaries

		require.Equal(t, []uint64{0, 1, 1, 2}, []uint64{
			logs[0].TxIndex, logs[1].TxIndex, logs[2].TxIndex, logs[3].TxIndex,
		})
	})

	t.Run("mixed block: native, derived, non-EVM, native", func(t *testing.T) {
		native0 := createEthTxResult(t, "hash0", 2, 0)
		native0.Events = []abci.Event{ethTxEvent("0xc0", 0)}

		derived := derivedTxResult(t, derivedTxEvents(t, "0xc1", 1, 1))

		// a plain cosmos tx: no EVM events, no MsgEthereumTxResponse
		nonEVM := &abci.ExecTxResult{Code: 0, Data: nil}

		native1 := createEthTxResult(t, "hash1", 1, 0)
		native1.Events = []abci.Event{ethTxEvent("0xc2", 3)}

		results, err := evmtypes.PatchTxResponses(
			[]*abci.ExecTxResult{native0, derived, nonEVM, native1})
		require.NoError(t, err)

		// ranks: native0=0, derived=1, native1=2 (non-EVM consumes none)
		assertRanksContiguous(t, allRanks(t, results))

		logs := collectBlockLogs(t, results)
		require.Len(t, logs, 4)
		assertLogIndicesContiguous(t, logs) // 0,1 native | 2 derived | 3 native

		require.Equal(t, "native", logs[0].Kind)
		require.Equal(t, "derived", logs[2].Kind)
		require.Equal(t, uint64(0), logs[0].TxIndex)
		require.Equal(t, uint64(0), logs[1].TxIndex)
		require.Equal(t, uint64(1), logs[2].TxIndex, "derived log must carry the eth rank, not the cosmos position")
		require.Equal(t, uint64(2), logs[3].TxIndex)
	})

	t.Run("interleaved native and derived", func(t *testing.T) {
		n0 := createEthTxResult(t, "hash0", 1, 0)
		n0.Events = []abci.Event{ethTxEvent("0xd0", 0)}
		d0 := derivedTxResult(t, derivedTxEvents(t, "0xd1", 1, 1))
		n1 := createEthTxResult(t, "hash1", 1, 0)
		n1.Events = []abci.Event{ethTxEvent("0xd2", 2)}
		d1 := derivedTxResult(t, derivedTxEvents(t, "0xd3", 3, 1))

		results, err := evmtypes.PatchTxResponses([]*abci.ExecTxResult{n0, d0, n1, d1})
		require.NoError(t, err)

		assertRanksContiguous(t, allRanks(t, results)) // 0,1,2,3
		logs := collectBlockLogs(t, results)
		require.Len(t, logs, 4)
		assertLogIndicesContiguous(t, logs)
		for i, l := range logs {
			require.Equal(t, uint64(i), l.TxIndex, //#nosec G115
				"each tx here emits exactly one log, so rank and logIndex advance together")
		}
	})

	t.Run("derived tx that emitted no logs still consumes a rank", func(t *testing.T) {
		d := derivedTxResult(t, derivedTxEvents(t, "0xe0", 0, 0))
		n := createEthTxResult(t, "hash0", 1, 0)
		n.Events = []abci.Event{ethTxEvent("0xe1", 1)}

		results, err := evmtypes.PatchTxResponses([]*abci.ExecTxResult{d, n})
		require.NoError(t, err)

		assertRanksContiguous(t, allRanks(t, results)) // derived=0, native=1
		logs := collectBlockLogs(t, results)
		require.Len(t, logs, 1)
		require.Equal(t, uint64(0), logs[0].Index, "log numbering is unaffected by a zero-log tx")
		require.Equal(t, uint64(1), logs[0].TxIndex, "but the rank was consumed by the derived tx")
	})

	t.Run("unexpected failure between derived txs consumes no rank", func(t *testing.T) {
		d0 := derivedTxResult(t, derivedTxEvents(t, "0xf0", 0, 1))
		// a normal failure (not ExceedBlockGasLimit) is outside the eth rank space
		bad := &abci.ExecTxResult{Code: 5, Events: []abci.Event{ethTxEvent("0xf1", 1)}}
		d1 := derivedTxResult(t, derivedTxEvents(t, "0xf2", 2, 1))

		results, err := evmtypes.PatchTxResponses([]*abci.ExecTxResult{d0, bad, d1})
		require.NoError(t, err)

		logs := collectBlockLogs(t, results)
		require.Len(t, logs, 2)
		assertLogIndicesContiguous(t, logs)
		require.Equal(t, uint64(0), logs[0].TxIndex)
		require.Equal(t, uint64(1), logs[1].TxIndex,
			"the skipped failure must not consume a rank between the two derived txs")
	})

	t.Run("expected ExceedBlockGasLimit failure does consume a rank", func(t *testing.T) {
		d0 := derivedTxResult(t, derivedTxEvents(t, "0x10", 0, 1))
		exceeded := &abci.ExecTxResult{
			Code:   1,
			Log:    evmtypes.ExceedBlockGasLimitError,
			Events: []abci.Event{ethTxEvent("0x11", 1)},
		}
		d1 := derivedTxResult(t, derivedTxEvents(t, "0x12", 2, 1))

		results, err := evmtypes.PatchTxResponses([]*abci.ExecTxResult{d0, exceeded, d1})
		require.NoError(t, err)

		assertRanksContiguous(t, allRanks(t, results)) // 0, 1 (exceeded), 2
		logs := collectBlockLogs(t, results)
		require.Len(t, logs, 2)
		assertLogIndicesContiguous(t, logs)
		require.Equal(t, uint64(0), logs[0].TxIndex)
		require.Equal(t, uint64(2), logs[1].TxIndex,
			"the gas-limit failure keeps its rank, so the next derived tx is rank 2")
	})

	t.Run("large block stays contiguous", func(t *testing.T) {
		var input []*abci.ExecTxResult
		for i := 0; i < 10; i++ {
			if i%2 == 0 {
				n := createEthTxResult(t, "hash"+strconv.Itoa(i), 2, 0)
				n.Events = []abci.Event{ethTxEvent("0x"+strconv.Itoa(i), uint64(i))} //#nosec G115
				input = append(input, n)
				continue
			}
			ev := derivedTxEvents(t, "0xd"+strconv.Itoa(i), uint64(i), 1) //#nosec G115
			ev = append(ev, derivedTxEvents(t, "0xe"+strconv.Itoa(i), uint64(i), 2)...)
			input = append(input, derivedTxResult(t, ev))
		}

		results, err := evmtypes.PatchTxResponses(input)
		require.NoError(t, err)

		assertRanksContiguous(t, allRanks(t, results))
		logs := collectBlockLogs(t, results)
		// 5 native × 2 logs + 5 × (1 + 2) derived logs
		require.Len(t, logs, 25)
		assertLogIndicesContiguous(t, logs)

		// every log's TxIndex must be a rank that actually exists in the block
		maxRank := uint64(len(allRanks(t, results)) - 1) //#nosec G115
		for _, l := range logs {
			require.LessOrEqual(t, l.TxIndex, maxRank)
		}
	})
}
