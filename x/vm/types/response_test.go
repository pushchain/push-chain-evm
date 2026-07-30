package types_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"

	evmtypes "github.com/cosmos/evm/x/vm/types"
	"github.com/cosmos/gogoproto/proto"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

func createEthTxResult(t *testing.T, hash string, numLogs int, code uint32) *abci.ExecTxResult {
	t.Helper()
	logs := make([]*evmtypes.Log, numLogs)
	for i := 0; i < numLogs; i++ {
		logs[i] = &evmtypes.Log{Data: []byte{byte(i)}}
	}
	response := &evmtypes.MsgEthereumTxResponse{
		Hash: common.BytesToHash([]byte(hash)).String(),
		Logs: logs,
	}
	anyRsp, _ := codectypes.NewAnyWithValue(response)
	txMsgData := &sdk.TxMsgData{
		MsgResponses: []*codectypes.Any{anyRsp},
	}
	data, _ := proto.Marshal(txMsgData)
	return &abci.ExecTxResult{
		Code: code,
		Data: data,
	}
}

func unmarshalTxResponse(t *testing.T, result *abci.ExecTxResult) *evmtypes.MsgEthereumTxResponse {
	t.Helper()
	var txMsgData sdk.TxMsgData
	err := proto.Unmarshal(result.Data, &txMsgData)
	require.NoError(t, err)
	var response evmtypes.MsgEthereumTxResponse
	err = proto.Unmarshal(txMsgData.MsgResponses[0].Value, &response)
	require.NoError(t, err)
	return &response
}

func TestPatchTxResponses(t *testing.T) {
	testCases := []struct {
		name     string
		input    []*abci.ExecTxResult
		validate func(t *testing.T, result []*abci.ExecTxResult)
	}{
		{
			name:  "empty input",
			input: []*abci.ExecTxResult{},
			validate: func(t *testing.T, result []*abci.ExecTxResult) {
				t.Helper()
				require.Empty(t, result)
			},
		},
		{
			name:  "single tx with no logs is a no-op",
			input: []*abci.ExecTxResult{createEthTxResult(t, "hash1", 0, 0)},
			validate: func(t *testing.T, result []*abci.ExecTxResult) {
				t.Helper()
				require.Len(t, result, 1)
				require.Empty(t, result[0].Events)
			},
		},
		{
			name:  "single tx with logs: log.Index + log.TxIndex rewritten",
			input: []*abci.ExecTxResult{createEthTxResult(t, "hash1", 2, 0)},
			validate: func(t *testing.T, result []*abci.ExecTxResult) {
				t.Helper()
				require.Len(t, result, 1)
				response := unmarshalTxResponse(t, result[0])
				require.Len(t, response.Logs, 2)
				require.Equal(t, uint64(0), response.Logs[0].TxIndex)
				require.Equal(t, uint64(0), response.Logs[0].Index)
				require.Equal(t, uint64(0), response.Logs[1].TxIndex)
				require.Equal(t, uint64(1), response.Logs[1].Index)
			},
		},
		{
			name: "multiple txs with logs: indices monotonic across block",
			input: []*abci.ExecTxResult{
				createEthTxResult(t, "hash1", 2, 0),
				createEthTxResult(t, "hash2", 3, 0),
			},
			validate: func(t *testing.T, result []*abci.ExecTxResult) {
				t.Helper()
				require.Len(t, result, 2)
				response1 := unmarshalTxResponse(t, result[0])
				require.Len(t, response1.Logs, 2)
				require.Equal(t, uint64(0), response1.Logs[0].TxIndex)
				require.Equal(t, uint64(0), response1.Logs[0].Index)
				require.Equal(t, uint64(0), response1.Logs[1].TxIndex)
				require.Equal(t, uint64(1), response1.Logs[1].Index)

				response2 := unmarshalTxResponse(t, result[1])
				require.Len(t, response2.Logs, 3)
				require.Equal(t, uint64(1), response2.Logs[0].TxIndex)
				require.Equal(t, uint64(2), response2.Logs[0].Index)
				require.Equal(t, uint64(1), response2.Logs[1].TxIndex)
				require.Equal(t, uint64(3), response2.Logs[1].Index)
				require.Equal(t, uint64(1), response2.Logs[2].TxIndex)
				require.Equal(t, uint64(4), response2.Logs[2].Index)
			},
		},
		{
			name:  "failed tx is skipped (no index increments)",
			input: []*abci.ExecTxResult{createEthTxResult(t, "hash1", 1, 1)},
			validate: func(t *testing.T, result []*abci.ExecTxResult) {
				t.Helper()
				require.Len(t, result, 1)
				require.Empty(t, result[0].Events)
			},
		},
		{
			name: "mixed success and failed txs: eth tx counter only advances on success",
			input: []*abci.ExecTxResult{
				createEthTxResult(t, "hash1", 1, 0),
				createEthTxResult(t, "hash2", 1, 1),
				createEthTxResult(t, "hash3", 1, 0),
			},
			validate: func(t *testing.T, result []*abci.ExecTxResult) {
				t.Helper()
				require.Len(t, result, 3)

				response1 := unmarshalTxResponse(t, result[0])
				require.Equal(t, uint64(0), response1.Logs[0].TxIndex)
				require.Equal(t, uint64(0), response1.Logs[0].Index)

				require.Empty(t, result[1].Events)

				response3 := unmarshalTxResponse(t, result[2])
				require.Equal(t, uint64(1), response3.Logs[0].TxIndex)
				require.Equal(t, uint64(1), response3.Logs[0].Index)
			},
		},
		{
			name: "existing events are preserved",
			input: func() []*abci.ExecTxResult {
				result := createEthTxResult(t, "hash1", 1, 0)
				result.Events = []abci.Event{
					{Type: "existing_event", Attributes: []abci.EventAttribute{{Key: "key", Value: "value"}}},
				}
				return []*abci.ExecTxResult{result}
			}(),
			validate: func(t *testing.T, result []*abci.ExecTxResult) {
				t.Helper()
				require.Len(t, result, 1)
				require.Len(t, result[0].Events, 1)
				require.Equal(t, "existing_event", result[0].Events[0].Type)
			},
		},
		{
			name: "non-ethereum tx msg response is ignored",
			input: func() []*abci.ExecTxResult {
				anyRsp, _ := codectypes.NewAnyWithValue(&sdk.TxMsgData{})
				txMsgData := &sdk.TxMsgData{MsgResponses: []*codectypes.Any{anyRsp}}
				data, _ := proto.Marshal(txMsgData)
				return []*abci.ExecTxResult{{Code: 0, Data: data}}
			}(),
			validate: func(t *testing.T, result []*abci.ExecTxResult) {
				t.Helper()
				require.Len(t, result, 1)
				require.Empty(t, result[0].Events)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := evmtypes.PatchTxResponses(tc.input)
			require.NoError(t, err)
			tc.validate(t, result)
		})
	}
}

// ethTxEvent builds an ante-style ethereum_tx event with the given cosmos
// position as AttributeKeyTxIndex. Used to simulate what EmitTxHashEvent
// produces from the ante handler.
func ethTxEvent(hash string, cosmosTxIndex uint64) abci.Event {
	return abci.Event{
		Type: evmtypes.EventTypeEthereumTx,
		Attributes: []abci.EventAttribute{
			{Key: evmtypes.AttributeKeyEthereumTxHash, Value: hash},
			{Key: evmtypes.AttributeKeyTxIndex, Value: strconv.FormatUint(cosmosTxIndex, 10)},
		},
	}
}

func eventTxIndex(t *testing.T, res *abci.ExecTxResult) string {
	t.Helper()
	for _, ev := range res.Events {
		if ev.Type != evmtypes.EventTypeEthereumTx {
			continue
		}
		for _, a := range ev.Attributes {
			if a.Key == evmtypes.AttributeKeyTxIndex {
				return a.Value
			}
		}
	}
	t.Fatalf("no %s event with %s attribute", evmtypes.EventTypeEthereumTx, evmtypes.AttributeKeyTxIndex)
	return ""
}

// TestPatchTxResponses_MixedBlockEventRewrite asserts that in a block where a
// non-EVM cosmos tx sits between EVM txs, the ante-emitted ethereum_tx events
// get their AttributeKeyTxIndex rewritten from cosmos-position to eth-only
// rank so the indexer's EthTxIndex (and downstream receipt.TransactionIndex)
// matches log.TxIndex.
func TestPatchTxResponses_MixedBlockEventRewrite(t *testing.T) {
	eth0 := createEthTxResult(t, "hash0", 1, 0)
	eth0.Events = []abci.Event{ethTxEvent("0xaa", 0)}

	nonEVM := &abci.ExecTxResult{Code: 0}

	eth2 := createEthTxResult(t, "hash2", 1, 0)
	eth2.Events = []abci.Event{ethTxEvent("0xbb", 2)}

	result, err := evmtypes.PatchTxResponses([]*abci.ExecTxResult{eth0, nonEVM, eth2})
	require.NoError(t, err)
	require.Len(t, result, 3)

	require.Equal(t, "0", eventTxIndex(t, result[0]))
	require.Equal(t, "1", eventTxIndex(t, result[2]),
		"second eth tx's event should be rewritten from cosmos-pos 2 to eth-only rank 1")

	resp0 := unmarshalTxResponse(t, result[0])
	require.Equal(t, uint64(0), resp0.Logs[0].TxIndex)
	resp2 := unmarshalTxResponse(t, result[2])
	require.Equal(t, uint64(1), resp2.Logs[0].TxIndex,
		"log.TxIndex must agree with the event attribute so receipt.TransactionIndex == log.transactionIndex")
}

// TestPatchTxResponses_NonTxMsgDataDoesNotHaltBlock asserts that a successful
// non-EVM tx carrying a res.Data payload that isn't an sdk.TxMsgData is
// skipped rather than causing the whole block to error out.
func TestPatchTxResponses_NonTxMsgDataDoesNotHaltBlock(t *testing.T) {
	nonEVM := &abci.ExecTxResult{Code: 0, Data: []byte{0xff, 0xff, 0xff}}

	eth := createEthTxResult(t, "hash1", 1, 0)

	result, err := evmtypes.PatchTxResponses([]*abci.ExecTxResult{nonEVM, eth})
	require.NoError(t, err)
	require.Len(t, result, 2)

	require.Equal(t, []byte{0xff, 0xff, 0xff}, result[0].Data, "non-EVM data must be left untouched")

	resp := unmarshalTxResponse(t, result[1])
	require.Equal(t, uint64(0), resp.Logs[0].TxIndex,
		"eth tx counter must start from 0 when preceding non-EVM tx is skipped")
}

// TestPatchTxResponses_ExceedBlockGasLimitAdvancesCounter asserts that an
// ExceedBlockGasLimit failure — which the indexer still includes in the eth
// rank space — advances the eth-only counter.
func TestPatchTxResponses_ExceedBlockGasLimitAdvancesCounter(t *testing.T) {
	failed := &abci.ExecTxResult{
		Code:   1,
		Log:    evmtypes.ExceedBlockGasLimitError,
		Events: []abci.Event{ethTxEvent("0xaa", 0)},
	}

	eth1 := createEthTxResult(t, "hash1", 1, 0)
	eth1.Events = []abci.Event{ethTxEvent("0xbb", 1)}

	result, err := evmtypes.PatchTxResponses([]*abci.ExecTxResult{failed, eth1})
	require.NoError(t, err)

	require.Equal(t, "0", eventTxIndex(t, result[0]))
	require.Equal(t, "1", eventTxIndex(t, result[1]))

	resp1 := unmarshalTxResponse(t, result[1])
	require.Equal(t, uint64(1), resp1.Logs[0].TxIndex)
}

// TestPatchTxResponses_UnexpectedFailureIsSkipped asserts that a failure that
// is NOT ExceedBlockGasLimit is skipped entirely — events are not rewritten
// and the eth-only counter is not advanced — matching the indexer's and RPC
// backend's TxSucessOrExpectedFailure gate.
func TestPatchTxResponses_UnexpectedFailureIsSkipped(t *testing.T) {
	eth0 := createEthTxResult(t, "hash0", 1, 0)
	eth0.Events = []abci.Event{ethTxEvent("0xaa", 0)}

	rejected := &abci.ExecTxResult{
		Code:   1,
		Log:    "some unrelated ante error",
		Events: []abci.Event{ethTxEvent("0xbb", 1)},
	}

	eth2 := createEthTxResult(t, "hash2", 1, 0)
	eth2.Events = []abci.Event{ethTxEvent("0xcc", 2)}

	result, err := evmtypes.PatchTxResponses([]*abci.ExecTxResult{eth0, rejected, eth2})
	require.NoError(t, err)

	require.Equal(t, "0", eventTxIndex(t, result[0]))
	require.Equal(t, "1", eventTxIndex(t, result[1]),
		"rejected tx's event should retain its original cosmos-position value (not rewritten)")
	require.Equal(t, "1", eventTxIndex(t, result[2]),
		"third eth tx's rank should be 1 (rejected tx did not consume a rank)")

	resp2 := unmarshalTxResponse(t, result[2])
	require.Equal(t, uint64(1), resp2.Logs[0].TxIndex)
}

func TestPatchTxResponses_LogIndex(t *testing.T) {
	input := []*abci.ExecTxResult{
		createEthTxResult(t, "hash1", 2, 0),
		createEthTxResult(t, "hash2", 3, 0),
		createEthTxResult(t, "hash3", 1, 0),
	}
	result, err := evmtypes.PatchTxResponses(input)
	require.NoError(t, err)
	expectedLogIndexes := [][]uint64{
		{0, 1},
		{2, 3, 4},
		{5},
	}
	for txIdx, expectedIndexes := range expectedLogIndexes {
		response := unmarshalTxResponse(t, result[txIdx])
		require.Len(t, response.Logs, len(expectedIndexes))
		for logIdx, expectedIndex := range expectedIndexes {
			require.Equal(t, expectedIndex, response.Logs[logIdx].Index)
			require.Equal(t, uint64(txIdx), response.Logs[logIdx].TxIndex) //#nosec G115
		}
	}
}

func TestPatchTxResponses_DualEthereumTxEvents(t *testing.T) {
	failed := &abci.ExecTxResult{
		Code: 1,
		Log:  evmtypes.ExceedBlockGasLimitError,
		Events: []abci.Event{
			ethTxEvent("0xaa", 0),
			{Type: evmtypes.EventTypeEthereumTx, Attributes: []abci.EventAttribute{
				{Key: evmtypes.AttributeKeyEthereumTxHash, Value: "0xaa"},
			}},
		},
	}

	eth := createEthTxResult(t, "hash1", 1, 0)
	eth.Events = []abci.Event{ethTxEvent("0xbb", 1)}

	result, err := evmtypes.PatchTxResponses([]*abci.ExecTxResult{failed, eth})
	require.NoError(t, err)

	require.Equal(t, "0", eventTxIndex(t, result[0]))
	require.Equal(t, "1", eventTxIndex(t, result[1]))

	resp := unmarshalTxResponse(t, result[1])
	require.Equal(t, uint64(1), resp.Logs[0].TxIndex)
}

func TestPatchTxResponses_ZeroLogEthTx(t *testing.T) {
	input := []*abci.ExecTxResult{
		createEthTxResult(t, "hash1", 2, 0),
		createEthTxResult(t, "hash2", 0, 0),
		createEthTxResult(t, "hash3", 1, 0),
	}
	result, err := evmtypes.PatchTxResponses(input)
	require.NoError(t, err)

	resp0 := unmarshalTxResponse(t, result[0])
	require.Len(t, resp0.Logs, 2)
	require.Equal(t, uint64(0), resp0.Logs[0].Index)
	require.Equal(t, uint64(1), resp0.Logs[1].Index)

	resp2 := unmarshalTxResponse(t, result[2])
	require.Len(t, resp2.Logs, 1)
	require.Equal(t, uint64(2), resp2.Logs[0].Index, "zero-log tx must not advance log.Index")
	require.Equal(t, uint64(2), resp2.Logs[0].TxIndex, "zero-log tx must still take an eth-tx rank")
}

// derivedTxEvents builds the (ethereum_tx, tx_log) event pair that
// DerivedEVMCallWithData emits for one derived tx. Logs are serialized at emit
// time with tx-local indices, which is what PatchTxResponses must correct:
// statedb.AddLog restarts log.Index at 0 for every derived call and sets
// log.TxIndex to the cosmos position.
func derivedTxEvents(t *testing.T, hash string, cosmosTxIndex uint64, numLogs int) []abci.Event {
	t.Helper()
	events := []abci.Event{ethTxEvent(hash, cosmosTxIndex)}

	attrs := make([]abci.EventAttribute, numLogs)
	for i := 0; i < numLogs; i++ {
		bz, err := json.Marshal(&evmtypes.Log{
			TxHash:  hash,
			Index:   uint64(i),     //#nosec G115 -- tx-local index, as emitted
			TxIndex: cosmosTxIndex, // cosmos position, as emitted
		})
		require.NoError(t, err)
		attrs[i] = abci.EventAttribute{Key: evmtypes.AttributeKeyTxLog, Value: string(bz)}
	}
	events = append(events, abci.Event{Type: evmtypes.EventTypeTxLog, Attributes: attrs})
	return events
}

// derivedLogs pulls the patched logs back out of a result's tx_log events.
func derivedLogs(t *testing.T, res *abci.ExecTxResult) []evmtypes.Log {
	t.Helper()
	var out []evmtypes.Log
	for _, ev := range res.Events {
		if ev.Type != evmtypes.EventTypeTxLog {
			continue
		}
		for _, attr := range ev.Attributes {
			if attr.Key != evmtypes.AttributeKeyTxLog {
				continue
			}
			var log evmtypes.Log
			require.NoError(t, json.Unmarshal([]byte(attr.Value), &log))
			out = append(out, log)
		}
	}
	return out
}

// derivedTxResult builds a successful cosmos tx result that carries derived txs
// only: its Data has no MsgEthereumTxResponse, exactly like a uexecutor tx.
func derivedTxResult(t *testing.T, events []abci.Event) *abci.ExecTxResult {
	t.Helper()
	data, err := proto.Marshal(&sdk.TxMsgData{MsgResponses: []*codectypes.Any{}})
	require.NoError(t, err)
	return &abci.ExecTxResult{Code: 0, Data: data, Events: events}
}

// TestPatchTxResponses_DerivedTxLogIndex asserts that logs carried in tx_log
// events (push-chain derived txs) receive block-global log.Index values, and
// their log.TxIndex is the eth rank rather than the cosmos position.
//
// Before this was handled, every derived tx restarted log.Index at 0, producing
// duplicate logIndex values within a block.
func TestPatchTxResponses_DerivedTxLogIndex(t *testing.T) {
	// cosmos tx 0: derived tx with 2 logs; cosmos tx 1: derived tx with 1 log.
	res0 := derivedTxResult(t, derivedTxEvents(t, "0xaa", 0, 2))
	res1 := derivedTxResult(t, derivedTxEvents(t, "0xbb", 1, 1))

	result, err := evmtypes.PatchTxResponses([]*abci.ExecTxResult{res0, res1})
	require.NoError(t, err)

	logs0 := derivedLogs(t, result[0])
	require.Len(t, logs0, 2)
	require.Equal(t, uint64(0), logs0[0].Index)
	require.Equal(t, uint64(1), logs0[1].Index)

	logs1 := derivedLogs(t, result[1])
	require.Len(t, logs1, 1)
	require.Equal(t, uint64(2), logs1[0].Index,
		"second derived tx must continue the block log sequence, not restart at 0")

	// eth ranks, not cosmos positions
	require.Equal(t, uint64(0), logs0[0].TxIndex)
	require.Equal(t, uint64(1), logs1[0].TxIndex)
	require.Equal(t, "0", eventTxIndex(t, result[0]))
	require.Equal(t, "1", eventTxIndex(t, result[1]))
}

// TestPatchTxResponses_DerivedTxAdvancesEthRank asserts that a successful
// derived-only cosmos tx consumes eth-tx ranks. It contributes no
// MsgEthereumTxResponse, so without explicit advancement the counter would stay
// put and the following tx would reuse the same rank.
func TestPatchTxResponses_DerivedTxAdvancesEthRank(t *testing.T) {
	// cosmos tx 0: one derived tx. cosmos tx 1: a native eth tx.
	derived := derivedTxResult(t, derivedTxEvents(t, "0xaa", 0, 1))
	native := createEthTxResult(t, "hash1", 1, 0)
	native.Events = []abci.Event{ethTxEvent("0xbb", 1)}

	result, err := evmtypes.PatchTxResponses([]*abci.ExecTxResult{derived, native})
	require.NoError(t, err)

	require.Equal(t, "0", eventTxIndex(t, result[0]), "derived tx takes eth rank 0")
	require.Equal(t, "1", eventTxIndex(t, result[1]),
		"native tx must take rank 1 — the derived tx consumed rank 0")

	resp := unmarshalTxResponse(t, result[1])
	require.Equal(t, uint64(1), resp.Logs[0].TxIndex,
		"native log.TxIndex must match its receipt's eth rank")
	require.Equal(t, uint64(1), resp.Logs[0].Index,
		"native log must continue the block log sequence after the derived tx's log")
}

// TestPatchTxResponses_MultipleDerivedTxsInOneCosmosTx asserts that several
// derived txs emitted by a single cosmos message each get their own eth rank
// and contiguous log indices.
func TestPatchTxResponses_MultipleDerivedTxsInOneCosmosTx(t *testing.T) {
	events := append(
		derivedTxEvents(t, "0xaa", 0, 1),
		derivedTxEvents(t, "0xbb", 0, 2)...,
	)
	res := derivedTxResult(t, events)
	next := createEthTxResult(t, "hash1", 1, 0)
	next.Events = []abci.Event{ethTxEvent("0xcc", 1)}

	result, err := evmtypes.PatchTxResponses([]*abci.ExecTxResult{res, next})
	require.NoError(t, err)

	logs := derivedLogs(t, result[0])
	require.Len(t, logs, 3)
	require.Equal(t, uint64(0), logs[0].Index)
	require.Equal(t, uint64(1), logs[1].Index)
	require.Equal(t, uint64(2), logs[2].Index)

	// first derived tx is rank 0, second is rank 1
	require.Equal(t, uint64(0), logs[0].TxIndex)
	require.Equal(t, uint64(1), logs[1].TxIndex)
	require.Equal(t, uint64(1), logs[2].TxIndex)

	// the following native tx must start at rank 2
	require.Equal(t, "2", eventTxIndex(t, result[1]))
	require.Equal(t, uint64(3), unmarshalTxResponse(t, result[1]).Logs[0].Index)
}
