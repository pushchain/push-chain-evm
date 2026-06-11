package backend_test

import (
	"context"
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

	"github.com/cosmos/evm/rpc/backend"
	"github.com/cosmos/evm/rpc/backend/mocks"
	rpctypes "github.com/cosmos/evm/rpc/types"
	cosmosevmtypes "github.com/cosmos/evm/types"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	"cosmossdk.io/log"

	"github.com/cosmos/cosmos-sdk/client"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// stubIndexer is a minimal EVMTxIndexer that always returns "not found".
// This simulates a node with enable-indexer=true where derived txs are not stored.
type stubIndexer struct{}

func (s *stubIndexer) LastIndexedBlock() (int64, error)  { return 0, nil }
func (s *stubIndexer) FirstIndexedBlock() (int64, error) { return 0, nil }
func (s *stubIndexer) IndexBlock(_ *cmttypes.Block, _ []*abci.ExecTxResult) error { return nil }
func (s *stubIndexer) GetByTxHash(_ common.Hash) (*cosmosevmtypes.TxResult, error) {
	return nil, errors.New("tx not found")
}
func (s *stubIndexer) GetByBlockAndIndex(_ int64, _ int32) (*cosmosevmtypes.TxResult, error) {
	return nil, errors.New("tx not found")
}
func (s *stubIndexer) IsDerivedTx(_ common.Hash) (bool, error) { return false, nil }

// derivedTxSearchResult builds a fake CometBFT TxSearch response containing
// the ethereum_tx events that a derived tx emits (see x/vm/keeper/call_evm.go).
func derivedTxSearchResult(hash common.Hash, sender, recipient common.Address) *coretypes.ResultTxSearch {
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

	txResult := abci.ExecTxResult{
		Code: 0,
		Events: []abci.Event{
			{Type: evmtypes.EventTypeEthereumTx, Attributes: attrs},
			{Type: "message", Attributes: msgAttrs},
		},
	}

	return &coretypes.ResultTxSearch{
		Txs: []*coretypes.ResultTx{
			{
				Height:   100,
				Index:    0,
				TxResult: txResult,
				Tx:       cmttypes.Tx{},
			},
		},
		TotalCount: 1,
	}
}

// TestGetTxByEthHash_DerivedTxFallthrough verifies the fix: when the KV indexer
// returns "not found" for a derived tx hash, GetTxByEthHash falls through to
// CometBFT and returns TxResultAdditionalFields (proof it found the derived tx).
func TestGetTxByEthHash_DerivedTxFallthrough(t *testing.T) {
	derivedHash := common.HexToHash("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")

	// Mock CometBFT client: TxSearch finds the derived tx by its hash
	mockClient := mocks.NewClient(t)
	expectedQuery := fmt.Sprintf("%s.%s='%s'",
		evmtypes.TypeMsgEthereumTx,
		evmtypes.AttributeKeyEthereumTxHash,
		derivedHash.Hex(),
	)
	mockClient.On("TxSearch",
		context.Background(),
		expectedQuery,
		false, (*int)(nil), (*int)(nil), "",
	).Return(derivedTxSearchResult(derivedHash, sender, recipient), nil)

	b := &backend.Backend{
		Ctx:       context.Background(),
		Indexer:   &stubIndexer{},
		Logger:    log.NewNopLogger(),
		EvmChainID: big.NewInt(1),
		ClientCtx: client.Context{Client: mockClient},
	}

	txResult, additional, err := b.GetTxByEthHash(derivedHash)

	require.NoError(t, err, "derived tx lookup should succeed via CometBFT fallthrough")
	require.NotNil(t, txResult, "TxResult should be populated")
	require.NotNil(t, additional, "TxResultAdditionalFields must be non-nil for a derived tx")
	require.Equal(t, uint64(evmtypes.DerivedTxType), additional.Type, "Type should be DerivedTxType (99)")
	require.Equal(t, sender, additional.Sender, "Sender should match")
	require.Equal(t, recipient, additional.Recipient, "Recipient should match")
}

// TestGetTxByEthHash_NativeTxKVHit verifies native txs still use the fast KV path
// (i.e. the fallthrough does not affect native tx lookups).
func TestGetTxByEthHash_NativeTxKVHit(t *testing.T) {
	nativeHash := common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	// KV indexer returns a result for this hash (native tx)
	kvResult := &cosmosevmtypes.TxResult{Height: 50, TxIndex: 1, EthTxIndex: 0}

	nativeIndexer := &stubNativeIndexer{hash: nativeHash, result: kvResult}

	// CometBFT client should NOT be called for native txs
	mockClient := mocks.NewClient(t) // no .On() means any call panics

	b := &backend.Backend{
		Ctx:        context.Background(),
		Indexer:    nativeIndexer,
		Logger:     log.NewNopLogger(),
		EvmChainID: big.NewInt(1),
		ClientCtx:  client.Context{Client: mockClient},
	}

	txResult, additional, err := b.GetTxByEthHash(nativeHash)

	require.NoError(t, err)
	require.NotNil(t, txResult)
	require.Nil(t, additional, "native tx should have no additional fields")
	require.Equal(t, int64(50), txResult.Height)
}

// stubNativeIndexer returns a result for one specific hash (native tx), errors for everything else.
type stubNativeIndexer struct {
	hash   common.Hash
	result *cosmosevmtypes.TxResult
}

func (s *stubNativeIndexer) LastIndexedBlock() (int64, error)  { return 0, nil }
func (s *stubNativeIndexer) FirstIndexedBlock() (int64, error) { return 0, nil }
func (s *stubNativeIndexer) IndexBlock(_ *cmttypes.Block, _ []*abci.ExecTxResult) error { return nil }
func (s *stubNativeIndexer) GetByTxHash(h common.Hash) (*cosmosevmtypes.TxResult, error) {
	if h == s.hash {
		return s.result, nil
	}
	return nil, errors.New("tx not found")
}
func (s *stubNativeIndexer) GetByBlockAndIndex(_ int64, _ int32) (*cosmosevmtypes.TxResult, error) {
	return nil, errors.New("tx not found")
}
func (s *stubNativeIndexer) IsDerivedTx(_ common.Hash) (bool, error) { return false, nil }

// ---------------------------------------------------------------------------
// GetTransactionReceipt tests
// ---------------------------------------------------------------------------

// buildBackendForReceipt wires up a Backend with all dependencies needed to
// exercise GetTransactionReceipt for a derived tx:
//   - stubIndexer (KV always misses)
//   - mockClient handles TxSearch, Block, BlockResults
//   - mockEVMQueryClient.Params returns an error so ChainID() falls back to EvmChainID
func buildBackendForReceipt(
	t *testing.T,
	derivedHash common.Hash,
	sender, recipient common.Address,
) (*backend.Backend, *mocks.Client) {
	t.Helper()

	mockClient := mocks.NewClient(t)

	// 1. TxSearch — CometBFT finds the derived tx by hash
	expectedQuery := fmt.Sprintf("%s.%s='%s'",
		evmtypes.TypeMsgEthereumTx,
		evmtypes.AttributeKeyEthereumTxHash,
		derivedHash.Hex(),
	)
	mockClient.On("TxSearch",
		mock.Anything, expectedQuery, false,
		(*int)(nil), (*int)(nil), "",
	).Return(derivedTxSearchResult(derivedHash, sender, recipient), nil)

	// 2. Block — CometBlockByNumber fetches the block at height 100
	height := int64(100)
	mockClient.On("Block", mock.Anything, &height).
		Return(&coretypes.ResultBlock{
			Block: &cmttypes.Block{
				Header: cmttypes.Header{Height: 100},
			},
		}, nil)

	// 3. BlockResults — formatTxReceipt fetches block results at height 100
	mockClient.On("BlockResults", mock.Anything, &height).
		Return(&coretypes.ResultBlockResults{
			Height: 100,
			TxsResults: []*abci.ExecTxResult{
				{Code: 0, GasUsed: 21000},
			},
		}, nil)

	// 4. EVMQueryClient mocks:
	//    - Params: BlockNumber() calls this; error makes ChainID() fall back to EvmChainID
	//    - BaseFee: formatTxReceipt calls this for DynamicFeeTx (type 0x2) effective gas price
	mockEVMQueryClient := mocks.NewEVMQueryClient(t)
	mockEVMQueryClient.On("Params", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("no connection"))
	mockEVMQueryClient.On("BaseFee", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("no connection"))

	b := &backend.Backend{
		Ctx:         context.Background(),
		Indexer:     &stubIndexer{},
		Logger:      log.NewNopLogger(),
		EvmChainID:  big.NewInt(1),
		RPCClient:   mockClient,
		ClientCtx:   client.Context{Client: mockClient},
		QueryClient: &rpctypes.QueryClient{QueryClient: mockEVMQueryClient},
	}

	return b, mockClient
}

// TestGetTransactionReceipt_DerivedTx is the end-to-end receipt test.
// It verifies that after the fix a derived tx hash that is absent from the KV
// index still produces a complete receipt via the CometBFT fallback path.
func TestGetTransactionReceipt_DerivedTx(t *testing.T) {
	derivedHash := common.HexToHash("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")

	b, _ := buildBackendForReceipt(t, derivedHash, sender, recipient)

	receipt, err := b.GetTransactionReceipt(derivedHash)

	require.NoError(t, err)
	require.NotNil(t, receipt, "receipt must not be nil — derived tx should now be resolvable")

	// Core fields must be present
	require.NotNil(t, receipt["transactionHash"], "transactionHash must be present")
	require.NotNil(t, receipt["status"], "status must be present")
	require.NotNil(t, receipt["blockNumber"], "blockNumber must be present")
	require.NotNil(t, receipt["gasUsed"], "gasUsed must be present")

	// Sender recovered from msg.From (set directly, no signature recovery needed)
	require.Equal(t, sender, receipt["from"], "from address should match the derived tx sender")

	// Recipient
	to, ok := receipt["to"].(*common.Address)
	require.True(t, ok, "to should be *common.Address")
	require.Equal(t, recipient, *to, "to address should match the derived tx recipient")

	require.NotNil(t, receipt["transactionIndex"])
}

// TestGetTransactionReceipt_DerivedTxNotFound verifies that when neither the
// KV index nor CometBFT can find the hash, the receipt returns nil (no crash).
// GetTransactionReceipt returns early before ChainID() is called, so no
// QueryClient mock is needed here.
func TestGetTransactionReceipt_DerivedTxNotFound(t *testing.T) {
	unknownHash := common.HexToHash("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	mockClient := mocks.NewClient(t)

	// TxSearch finds nothing for the unknown hash
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

	receipt, err := b.GetTransactionReceipt(unknownHash)

	// Must not crash — returns nil, nil per Ethereum spec for unknown hashes
	require.NoError(t, err)
	require.Nil(t, receipt, "unknown hash should return nil receipt, not an error")
}
