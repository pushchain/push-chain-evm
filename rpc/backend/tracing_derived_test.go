package backend_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/cosmos/evm/encoding"
	"github.com/cosmos/evm/rpc/backend"
	"github.com/cosmos/evm/rpc/backend/mocks"
	rpctypes "github.com/cosmos/evm/rpc/types"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	"cosmossdk.io/log"

	"github.com/cosmos/cosmos-sdk/client"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// minimalEncodedTx returns valid Cosmos tx bytes that decode to a tx with zero
// messages. TraceTransaction needs decodable bytes at blk.Block.Txs[TxIndex]
// even for derived txs; the messages are never accessed in the success path.
func minimalEncodedTx(t *testing.T) []byte {
	t.Helper()
	cfg := encoding.MakeConfig(1)
	txb := cfg.TxConfig.NewTxBuilder()
	bz, err := cfg.TxConfig.TxEncoder()(txb.GetTx())
	require.NoError(t, err)
	return bz
}

// derivedBlockResults returns ResultBlockResults whose TxsResults[0] carries
// the same ethereum_tx events that a derived tx emits on chain.
func derivedBlockResults(hash common.Hash, sender, recipient common.Address) *coretypes.ResultBlockResults {
	attrs := []abci.EventAttribute{
		{Key: "amount", Value: "0"},
		{Key: evmtypes.AttributeKeyEthereumTxHash, Value: hash.Hex()},
		{Key: evmtypes.AttributeKeyTxIndex, Value: "0"},
		{Key: evmtypes.AttributeKeyTxGasUsed, Value: "21000"},
		{Key: evmtypes.AttributeKeyRecipient, Value: recipient.Hex()},
		{Key: evmtypes.AttributeKeyTxData, Value: "0x"},
		{Key: evmtypes.AttributeKeyTxNonce, Value: "0"},
		{Key: evmtypes.AttributeKeyTxGasLimit, Value: "100000"},
	}
	msgAttrs := []abci.EventAttribute{
		{Key: string(sdk.AttributeKeySender), Value: sender.Hex()},
		{Key: evmtypes.AttributeKeyTxType, Value: fmt.Sprintf("%d", evmtypes.DerivedTxType)},
	}
	return &coretypes.ResultBlockResults{
		Height: 100,
		TxsResults: []*abci.ExecTxResult{
			{
				Code: 0,
				Events: []abci.Event{
					{Type: evmtypes.EventTypeEthereumTx, Attributes: attrs},
					{Type: "message", Attributes: msgAttrs},
				},
				GasUsed: 21000,
			},
		},
	}
}

// TestTraceTransaction_DerivedTx is the end-to-end test for debug_traceTransaction
// on a derived tx hash. It verifies that:
//   - GetTxByEthHash falls through to CometBFT when the KV index misses
//   - TraceTransaction correctly builds the ethMessage from additional fields
//   - TraceTx gRPC is called with the right Msg (correct sender / recipient)
//   - The trace result is returned without error
func TestTraceTransaction_DerivedTx(t *testing.T) {
	derivedHash := common.HexToHash("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")
	height := int64(100)

	txBytes := minimalEncodedTx(t)

	mockClient := mocks.NewClient(t)
	mockEVMQueryClient := mocks.NewEVMQueryClient(t)

	// 1. GetTxByEthHash: KV miss → TxSearch finds it
	expectedQuery := fmt.Sprintf("%s.%s='%s'",
		evmtypes.TypeMsgEthereumTx,
		evmtypes.AttributeKeyEthereumTxHash,
		derivedHash.Hex(),
	)
	mockClient.On("TxSearch",
		mock.Anything, expectedQuery, false,
		(*int)(nil), (*int)(nil), "",
	).Return(derivedTxSearchResult(derivedHash, sender, recipient), nil)

	// 2. CometBlockByNumber: returns block containing our encoded tx
	mockClient.On("Block", mock.Anything, &height).
		Return(&coretypes.ResultBlock{
			Block: &cmttypes.Block{
				Header: cmttypes.Header{Height: height},
				Data:   cmttypes.Data{Txs: cmttypes.Txs{txBytes}},
			},
		}, nil)

	// 3. BlockResults: called twice — once in the additional!=nil branch
	//    (to find in-tx predecessors) and once in formatTxReceipt if reached.
	//    We use .Maybe() so extra calls are silently ignored.
	mockClient.On("BlockResults", mock.Anything, &height).
		Return(derivedBlockResults(derivedHash, sender, recipient), nil).Maybe()

	// 4. ConsensusParams: required for BlockMaxGas in TraceTxRequest
	mockClient.On("ConsensusParams", mock.Anything, &height).
		Return(&coretypes.ResultConsensusParams{
			ConsensusParams: cmttypes.ConsensusParams{
				Block: cmttypes.BlockParams{MaxGas: 10_000_000},
			},
		}, nil)

	// 5. TraceTx: the gRPC call that actually runs the trace
	traceOutput := `{"gas":21000,"failed":false,"returnValue":"","structLogs":[]}`
	mockEVMQueryClient.On("TraceTx", mock.Anything, mock.MatchedBy(func(req *evmtypes.QueryTraceTxRequest) bool {
		// Core assertions on what TraceTransaction sends to the EVM module:
		// - Msg must be non-nil (reconstructed from additional fields)
		// - Predecessors must be empty (single derived tx, TxIndex=0)
		// - Block height must match
		return req.Msg != nil &&
			len(req.Predecessors) == 0 &&
			req.BlockNumber == height
	}), mock.Anything).
		Return(&evmtypes.QueryTraceTxResponse{Data: []byte(traceOutput)}, nil)

	// 6. Params: BlockNumber() calls this; returning error makes ChainID() fall back to EvmChainID
	mockEVMQueryClient.On("Params", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("no connection")).Maybe()

	cfg := encoding.MakeConfig(1)
	b := &backend.Backend{
		Ctx:         context.Background(),
		Indexer:     &stubIndexer{},
		Logger:      log.NewNopLogger(),
		EvmChainID:  big.NewInt(1),
		RPCClient:   mockClient,
		ClientCtx:   client.Context{Client: mockClient, TxConfig: cfg.TxConfig},
		QueryClient: &rpctypes.QueryClient{QueryClient: mockEVMQueryClient},
	}

	result, err := b.TraceTransaction(derivedHash, nil)

	require.NoError(t, err, "TraceTransaction should succeed for a derived tx")
	require.NotNil(t, result, "trace result must not be nil")

	// Verify the trace output is what TraceTx returned
	resultBytes, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(resultBytes), "structLogs",
		"trace result should contain structLogs from the TraceTx response")
}

// TestTraceTransaction_DerivedTxNotFound verifies that when neither the KV index
// nor CometBFT can find the derived hash, TraceTransaction returns an error.
func TestTraceTransaction_DerivedTxNotFound(t *testing.T) {
	unknownHash := common.HexToHash("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	mockClient := mocks.NewClient(t)

	// TxSearch finds nothing
	mockClient.On("TxSearch", mock.Anything, mock.Anything, false,
		(*int)(nil), (*int)(nil), "").
		Return(&coretypes.ResultTxSearch{Txs: nil, TotalCount: 0}, nil)

	b := &backend.Backend{
		Ctx:        context.Background(),
		Indexer:    &stubIndexer{},
		Logger:     log.NewNopLogger(),
		EvmChainID: big.NewInt(1),
		RPCClient:  mockClient,
		ClientCtx:  client.Context{Client: mockClient},
	}

	result, err := b.TraceTransaction(unknownHash, nil)

	require.Error(t, err, "unknown derived hash should return an error")
	require.Nil(t, result)
}
