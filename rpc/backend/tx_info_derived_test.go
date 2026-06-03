package backend_test

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	cmttypes "github.com/cometbft/cometbft/types"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/cosmos/evm/rpc/backend"
	"github.com/cosmos/evm/rpc/backend/mocks"
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

// derivedTxSearchResult builds a fake CometBFT TxSearch response containing
// the ethereum_tx events that a derived tx emits (see x/vm/keeper/call_evm.go).
func derivedTxSearchResult(hash common.Hash, sender, recipient common.Address) *coretypes.ResultTxSearch {
	attrs := []abci.EventAttribute{
		{Key: "amount", Value: "0"},
		{Key: evmtypes.AttributeKeyEthereumTxHash, Value: hash.Hex()},
		{Key: evmtypes.AttributeKeyTxIndex, Value: fmt.Sprintf("%d", evmtypes.DerivedTxIndex)},
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
