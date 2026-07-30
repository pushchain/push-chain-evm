package types

import (
	"encoding/json"
	"strconv"

	abci "github.com/cometbft/cometbft/abci/types"

	"github.com/cosmos/gogoproto/proto"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// PatchTxResponses rewrites block-cumulative / eth-only indices that cannot
// be computed per-tx under BlockSTM:
//
//   - log.Index (cumulative across the block) and log.TxIndex (eth-only tx
//     counter) inside MsgEthereumTxResponse payloads.
//   - AttributeKeyTxIndex on ante-emitted ethereum_tx events, rewritten from
//     ctx.TxIndex() (cosmos-level position) to the eth-only counter so the
//     indexer's stored EthTxIndex — which becomes receipt.TransactionIndex
//     in RPC receipts — stays aligned with log.TxIndex in mixed-tx blocks.
//   - log.Index / log.TxIndex inside tx_log events (push-chain derived txs,
//     which carry their logs in ABCI events rather than a MsgEthereumTxResponse).
//
// Must be invoked once per block on the full ExecTxResult slice produced by
// the TxRunner. Gated on TxSucessOrExpectedFailure to match the indexer's and
// RPC backend's eth-rank inclusion rule — unexpected failures don't consume
// an eth-tx rank.
func PatchTxResponses(input []*abci.ExecTxResult) ([]*abci.ExecTxResult, error) {
	var (
		ethTxIndex uint64
		logIndex   uint64
	)
	for _, res := range input {
		if !TxSucessOrExpectedFailure(res) {
			continue
		}

		// base is this result's first eth-tx rank; rewriteEthTxEventIndex numbers
		// every ethereum_tx event in the result base, base+1, ... — native and
		// derived alike.
		base := ethTxIndex
		rewritten := rewriteEthTxEventIndex(res.Events, base)

		if res.Code != 0 {
			ethTxIndex = base + uint64(rewritten) //#nosec G115 -- int overflow is not a concern here
			continue
		}

		var txMsgData sdk.TxMsgData
		if err := proto.Unmarshal(res.Data, &txMsgData); err != nil {
			ethTxIndex = base + uint64(rewritten) //#nosec G115 -- int overflow is not a concern here
			continue
		}

		dataDirty := false
		for i, rsp := range txMsgData.MsgResponses {
			var response MsgEthereumTxResponse
			if rsp.TypeUrl != "/"+proto.MessageName(&response) {
				continue
			}
			if err := proto.Unmarshal(rsp.Value, &response); err != nil {
				return nil, err
			}

			if len(response.Logs) > 0 {
				for _, log := range response.Logs {
					log.TxIndex = ethTxIndex
					log.Index = logIndex
					logIndex++
				}

				anyRsp, err := codectypes.NewAnyWithValue(&response)
				if err != nil {
					return nil, err
				}
				txMsgData.MsgResponses[i] = anyRsp
				dataDirty = true
			}

			ethTxIndex++
		}

		if dataDirty {
			data, err := proto.Marshal(&txMsgData)
			if err != nil {
				return nil, err
			}
			res.Data = data
		}

		// push-chain: a derived tx executes inside a cosmos message and carries its
		// logs in tx_log events, so it produces no MsgEthereumTxResponse and the
		// loop above never reaches it. Patch those logs with the same block-global
		// counters, keyed off the eth-tx rank its ethereum_tx event just received.
		if err := patchDerivedTxLogEvents(res.Events, base, &logIndex); err != nil {
			return nil, err
		}

		// Every ethereum_tx event in this result consumes an eth-tx rank — including
		// derived ones, which contribute no MsgEthereumTxResponse and therefore never
		// advanced the counter in the loop above. Without this a derived-only cosmos
		// tx would leave ethTxIndex untouched and the next tx would reuse its ranks.
		if end := base + uint64(rewritten); end > ethTxIndex { //#nosec G115 -- int overflow is not a concern here
			ethTxIndex = end
		}
	}
	return input, nil
}

// patchDerivedTxLogEvents rewrites log.Index / log.TxIndex inside the tx_log
// events of a single ExecTxResult, which is where push-chain's derived txs carry
// their logs (upstream natives carry them in a MsgEthereumTxResponse instead, and
// never emit tx_log events).
//
// Logs are serialized at emit time in x/vm/keeper.DerivedEVMCallWithData, before
// block-global numbering is known: statedb.AddLog assigns log.Index from the
// per-StateDB counter (restarting at 0 for every derived call) and log.TxIndex
// from ctx.TxIndex() (the cosmos position, not the eth rank). This pass replaces
// both with their block-global values.
//
// base is the eth-tx rank assigned to this result's first ethereum_tx event;
// logIndex is the running block-cumulative log counter, advanced in place.
// Events are emitted as (ethereum_tx, tx_log, message) per derived tx, so each
// tx_log belongs to the most recent ethereum_tx — that ordering is what pairs a
// log with its rank.
func patchDerivedTxLogEvents(events []abci.Event, base uint64, logIndex *uint64) error {
	ethSeen := -1
	for eIdx := range events {
		switch events[eIdx].Type {
		case EventTypeEthereumTx:
			ethSeen++
		case EventTypeTxLog:
			if ethSeen < 0 {
				// tx_log without a preceding ethereum_tx: nothing to key a rank off.
				continue
			}
			for aIdx := range events[eIdx].Attributes {
				if events[eIdx].Attributes[aIdx].Key != AttributeKeyTxLog {
					continue
				}
				var log Log
				if err := json.Unmarshal([]byte(events[eIdx].Attributes[aIdx].Value), &log); err != nil {
					// Not a log payload we own; leave it untouched rather than
					// failing the whole block.
					continue
				}
				log.TxIndex = base + uint64(ethSeen) //#nosec G115 -- int overflow is not a concern here
				log.Index = *logIndex
				*logIndex++

				bz, err := json.Marshal(&log)
				if err != nil {
					return err
				}
				events[eIdx].Attributes[aIdx].Value = string(bz)
			}
		}
	}
	return nil
}

// rewriteEthTxEventIndex rewrites AttributeKeyTxIndex on every ethereum_tx
// event in events to start, start+1, ... and returns the number of events
// rewritten.
func rewriteEthTxEventIndex(events []abci.Event, start uint64) int {
	n := 0
	for eIdx := range events {
		if events[eIdx].Type != EventTypeEthereumTx {
			continue
		}
		for aIdx := range events[eIdx].Attributes {
			if events[eIdx].Attributes[aIdx].Key == AttributeKeyTxIndex {
				events[eIdx].Attributes[aIdx].Value = strconv.FormatUint(start+uint64(n), 10) //#nosec G115 -- int overflow is not a concern here
				n++
				break
			}
		}
	}
	return n
}
