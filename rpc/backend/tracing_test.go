package backend

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/types"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/evm/indexer"
	rpctypes "github.com/cosmos/evm/rpc/types"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	"cosmossdk.io/log/v2"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// oneMsgNonEthCosmosTxBz encodes a non-eth Cosmos tx with exactly one message — a realistic
// stand-in for a MsgExecutePayload carrier that emits derived EVM txs. It uses a vm-module
// MsgUpdateParams (a registered, non-MsgEthereumTx message so it round-trips through the test
// app's interface registry and is never mistaken for a predecessor). Used to pin behaviour
// when a derived target's MsgIndex meets or exceeds len(GetMsgs()) of its 1-message carrier.
func (suite *BackendTestSuite) oneMsgNonEthCosmosTxBz() []byte {
	authority := sdk.AccAddress(common.HexToAddress("0x1111111111111111111111111111111111111111").Bytes()).String()
	builder := suite.backend.ClientCtx.TxConfig.NewTxBuilder()
	suite.Require().NoError(builder.SetMsgs(&evmtypes.MsgUpdateParams{Authority: authority, Params: evmtypes.DefaultParams()}))
	bz, err := suite.backend.ClientCtx.TxConfig.TxEncoder()(builder.GetTx())
	suite.Require().NoError(err)
	return bz
}

func (suite *BackendTestSuite) TestTraceTransaction() {
	msgEthereumTx, _ := suite.buildEthereumTx()
	msgEthereumTx2, _ := suite.buildEthereumTx()

	txHash := msgEthereumTx.AsTransaction().Hash()
	txHash2 := msgEthereumTx2.AsTransaction().Hash()

	txBz := suite.signAndEncodeEthTx(msgEthereumTx)
	txBz2 := suite.signAndEncodeEthTx(msgEthereumTx2)

	// Recompute hashes after signing (From is set by signAndEncodeEthTx).
	txHash = msgEthereumTx.AsTransaction().Hash()
	txHash2 = msgEthereumTx2.AsTransaction().Hash()

	testCases := []struct {
		name          string
		registerMock  func()
		block         *types.Block
		responseBlock []*abci.ExecTxResult
		expPass       bool
	}{
		{
			"fail - tx not found",
			func() {
				client := suite.mockClient()
				query := fmt.Sprintf("%s.%s='%s'", evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyEthereumTxHash, txHash.Hex())
				RegisterTxSearchEmpty(client, query)
			},
			&types.Block{Header: types.Header{Height: 1}, Data: types.Data{Txs: []types.Tx{}}},
			[]*abci.ExecTxResult{
				{
					Code: 0,
					Events: []abci.Event{
						{Type: evmtypes.EventTypeEthereumTx, Attributes: []abci.EventAttribute{
							{Key: "ethereumTxHash", Value: txHash.Hex()},
							{Key: "txIndex", Value: "0"},
							{Key: "amount", Value: "1000"},
							{Key: "txGasUsed", Value: "21000"},
							{Key: "txHash", Value: ""},
							{Key: "recipient", Value: "0x775b87ef5D82ca211811C1a02CE0fE0CA3a455d7"},
						}},
					},
				},
			},
			false,
		},
		{
			"fail - block not found",
			func() {
				client := suite.mockClient()
				RegisterBlockError(client, 1)
			},
			&types.Block{Header: types.Header{Height: 1}, Data: types.Data{Txs: []types.Tx{txBz}}},
			[]*abci.ExecTxResult{
				{
					Code: 0,
					Events: []abci.Event{
						{Type: evmtypes.EventTypeEthereumTx, Attributes: []abci.EventAttribute{
							{Key: "ethereumTxHash", Value: txHash.Hex()},
							{Key: "txIndex", Value: "0"},
							{Key: "amount", Value: "1000"},
							{Key: "txGasUsed", Value: "21000"},
							{Key: "txHash", Value: ""},
							{Key: "recipient", Value: "0x775b87ef5D82ca211811C1a02CE0fE0CA3a455d7"},
						}},
					},
				},
			},
			false,
		},
		{
			"pass - transaction found in a block with multiple transactions",
			func() {
				queryClient := suite.mockQueryClient()
				client := suite.mockClient()
				_, err := RegisterBlockMultipleTxs(client, 1, []types.Tx{txBz, txBz2})
				suite.Require().NoError(err)
				RegisterTraceTransactionWithPredecessors(queryClient, msgEthereumTx, nil)
				RegisterConsensusParams(client, 1)
			},
			&types.Block{Header: types.Header{Height: 1, ChainID: ChainID}, Data: types.Data{Txs: []types.Tx{txBz, txBz2}}},
			[]*abci.ExecTxResult{
				{
					Code: 0,
					Events: []abci.Event{
						{Type: evmtypes.EventTypeEthereumTx, Attributes: []abci.EventAttribute{
							{Key: "ethereumTxHash", Value: txHash.Hex()},
							{Key: "txIndex", Value: "0"},
							{Key: "amount", Value: "1000"},
							{Key: "txGasUsed", Value: "21000"},
							{Key: "txHash", Value: ""},
							{Key: "recipient", Value: "0x775b87ef5D82ca211811C1a02CE0fE0CA3a455d7"},
						}},
					},
				},
				{
					Code: 0,
					Events: []abci.Event{
						{Type: evmtypes.EventTypeEthereumTx, Attributes: []abci.EventAttribute{
							{Key: "ethereumTxHash", Value: txHash2.Hex()},
							{Key: "txIndex", Value: "1"},
							{Key: "amount", Value: "1000"},
							{Key: "txGasUsed", Value: "21000"},
							{Key: "txHash", Value: ""},
							{Key: "recipient", Value: "0x775b87ef5D82ca211811C1a02CE0fE0CA3a455d7"},
						}},
					},
				},
			},
			true,
		},
		{
			"pass - transaction found",
			func() {
				queryClient := suite.mockQueryClient()
				client := suite.mockClient()
				_, err := RegisterBlock(client, 1, txBz)
				suite.Require().NoError(err)
				RegisterTraceTransaction(queryClient, msgEthereumTx)
				RegisterConsensusParams(client, 1)
			},
			&types.Block{Header: types.Header{Height: 1}, Data: types.Data{Txs: []types.Tx{txBz}}},
			[]*abci.ExecTxResult{
				{
					Code: 0,
					Events: []abci.Event{
						{Type: evmtypes.EventTypeEthereumTx, Attributes: []abci.EventAttribute{
							{Key: "ethereumTxHash", Value: txHash.Hex()},
							{Key: "txIndex", Value: "0"},
							{Key: "amount", Value: "1000"},
							{Key: "txGasUsed", Value: "21000"},
							{Key: "txHash", Value: ""},
							{Key: "recipient", Value: "0x775b87ef5D82ca211811C1a02CE0fE0CA3a455d7"},
						}},
					},
				},
			},
			true,
		},
	}

	for _, tc := range testCases {
		suite.Run(fmt.Sprintf("case %s", tc.name), func() {
			suite.SetupTest()
			tc.registerMock()

			suite.backend.Indexer = indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), suite.backend.ClientCtx)
			err := suite.backend.Indexer.IndexBlock(tc.block, tc.responseBlock)
			suite.Require().NoError(err)
			_, err = suite.backend.TraceTransaction(context.Background(), txHash, nil)

			if tc.expPass {
				suite.Require().NoError(err)
			} else {
				suite.Require().Error(err)
			}
		})
	}
}

// TestTraceTransactionEthTxIndex verifies that TraceTransaction correctly traces
// a transaction that is not the first in a multi-tx block, using EthTxIndex (not
// TxIndex) as the predecessor-loop bound after the index-domain fix.
func (suite *BackendTestSuite) TestTraceTransactionEthTxIndex() {
	suite.SetupTest()

	msgFirst, _ := suite.buildEthereumTx()
	txBzFirst := suite.signAndEncodeEthTx(msgFirst)
	txHashFirst := msgFirst.AsTransaction().Hash()

	msgTarget, _ := suite.buildEthereumTx()
	txBzTarget := suite.signAndEncodeEthTx(msgTarget)
	txHashTarget := msgTarget.AsTransaction().Hash()

	localBlock := types.MakeBlock(1, []types.Tx{txBzFirst, txBzTarget}, nil, nil)
	localBlock.ChainID = ChainID

	responseBlock := []*abci.ExecTxResult{
		{
			Code: 0,
			Events: []abci.Event{{
				Type: evmtypes.EventTypeEthereumTx,
				Attributes: []abci.EventAttribute{
					{Key: evmtypes.AttributeKeyEthereumTxHash, Value: txHashFirst.Hex()},
					{Key: evmtypes.AttributeKeyTxIndex, Value: "0"},
					{Key: evmtypes.AttributeKeyTxGasUsed, Value: "21000"},
				},
			}},
		},
		{
			Code: 0,
			Events: []abci.Event{{
				Type: evmtypes.EventTypeEthereumTx,
				Attributes: []abci.EventAttribute{
					{Key: evmtypes.AttributeKeyEthereumTxHash, Value: txHashTarget.Hex()},
					{Key: evmtypes.AttributeKeyTxIndex, Value: "1"},
					{Key: evmtypes.AttributeKeyTxGasUsed, Value: "21000"},
				},
			}},
		},
	}

	suite.backend.Indexer = indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), suite.backend.ClientCtx)
	suite.Require().NoError(suite.backend.Indexer.IndexBlock(localBlock, responseBlock))

	queryClient := suite.mockQueryClient()
	client := suite.mockClient()

	_, err := RegisterBlockMultipleTxs(client, 1, []types.Tx{txBzFirst, txBzTarget})
	suite.Require().NoError(err)

	// EthTxIndex=1: the predecessor loop runs once (i=0) and fetches msgFirst.
	RegisterTraceTransactionWithPredecessors(queryClient, msgTarget, []*evmtypes.MsgEthereumTx{msgFirst})
	RegisterConsensusParams(client, 1)

	_, err = suite.backend.TraceTransaction(context.Background(), txHashTarget, nil)
	suite.Require().NoError(err)
}

// ethTxEvent returns a minimal EventTypeEthereumTx event suitable for IndexBlock.
func ethTxEvent(hash string, txIndex string) abci.Event {
	return abci.Event{
		Type: evmtypes.EventTypeEthereumTx,
		Attributes: []abci.EventAttribute{
			{Key: evmtypes.AttributeKeyEthereumTxHash, Value: hash},
			{Key: evmtypes.AttributeKeyTxIndex, Value: txIndex},
			{Key: evmtypes.AttributeKeyTxGasUsed, Value: "21000"},
		},
	}
}

// TestTraceTransactionMultiMsgSameCosmosTarget traces the second of two EVM messages
// packed into a single Cosmos tx slot. The same-Cosmos-tx guard fires on every outer-loop
// iteration, leaving the after-loop to supply the sole predecessor.
//
// Block layout:  slot0=[msg1, msg2=target]
// Expected predecessors: [msg1]
func (suite *BackendTestSuite) TestTraceTransactionMultiMsgSameCosmosTarget() {
	suite.SetupTest()

	msg1, _ := suite.buildEthereumTx()
	msgTarget, _ := suite.buildEthereumTx()
	txBzMulti := suite.buildAndEncodeMultiMsgEthTx(msg1, msgTarget)

	hash1 := msg1.AsTransaction().Hash()
	hashTarget := msgTarget.AsTransaction().Hash()

	localBlock := types.MakeBlock(1, []types.Tx{txBzMulti}, nil, nil)
	localBlock.ChainID = ChainID

	responseBlock := []*abci.ExecTxResult{{
		Code: 0,
		Events: []abci.Event{
			ethTxEvent(hash1.Hex(), "0"),
			ethTxEvent(hashTarget.Hex(), "1"),
		},
	}}

	suite.backend.Indexer = indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), suite.backend.ClientCtx)
	suite.Require().NoError(suite.backend.Indexer.IndexBlock(localBlock, responseBlock))

	queryClient := suite.mockQueryClient()
	client := suite.mockClient()

	_, err := RegisterBlockMultipleTxs(client, 1, []types.Tx{txBzMulti})
	suite.Require().NoError(err)

	RegisterTraceTransactionWithPredecessors(queryClient, msgTarget, []*evmtypes.MsgEthereumTx{msg1})
	RegisterConsensusParams(client, 1)

	_, err = suite.backend.TraceTransaction(context.Background(), hashTarget, nil)
	suite.Require().NoError(err)
}

// TestTraceTransactionMultiMsgTargetIsThird traces the third of three EVM messages
// packed into a single Cosmos tx. The outer loop skips all same-slot entries; the
// after-loop adds both msg1 and msg2.
//
// Block layout:  slot0=[msg1, msg2, msg3=target]
// Expected predecessors: [msg1, msg2]
func (suite *BackendTestSuite) TestTraceTransactionMultiMsgTargetIsThird() {
	suite.SetupTest()

	msg1, _ := suite.buildEthereumTx()
	msg2, _ := suite.buildEthereumTx()
	msgTarget, _ := suite.buildEthereumTx()
	txBzMulti := suite.buildAndEncodeMultiMsgEthTx(msg1, msg2, msgTarget)

	hash1 := msg1.AsTransaction().Hash()
	hash2 := msg2.AsTransaction().Hash()
	hashTarget := msgTarget.AsTransaction().Hash()

	localBlock := types.MakeBlock(1, []types.Tx{txBzMulti}, nil, nil)
	localBlock.ChainID = ChainID

	responseBlock := []*abci.ExecTxResult{{
		Code: 0,
		Events: []abci.Event{
			ethTxEvent(hash1.Hex(), "0"),
			ethTxEvent(hash2.Hex(), "1"),
			ethTxEvent(hashTarget.Hex(), "2"),
		},
	}}

	suite.backend.Indexer = indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), suite.backend.ClientCtx)
	suite.Require().NoError(suite.backend.Indexer.IndexBlock(localBlock, responseBlock))

	queryClient := suite.mockQueryClient()
	client := suite.mockClient()

	_, err := RegisterBlockMultipleTxs(client, 1, []types.Tx{txBzMulti})
	suite.Require().NoError(err)

	RegisterTraceTransactionWithPredecessors(queryClient, msgTarget, []*evmtypes.MsgEthereumTx{msg1, msg2})
	RegisterConsensusParams(client, 1)

	_, err = suite.backend.TraceTransaction(context.Background(), hashTarget, nil)
	suite.Require().NoError(err)
}

// TestTraceTransactionMultiMsgCosmosAsPredecessor traces a single-message target whose
// sole predecessor is a two-message Cosmos tx. Both messages must appear in the
// predecessor list — validates the fix that adds the message AT MsgIndex directly
// instead of the old inner loop that ran j<MsgIndex and missed the last message.
//
// Block layout:  slot0=[msg1, msg2], slot1=[msgTarget]
// Expected predecessors: [msg1, msg2]
func (suite *BackendTestSuite) TestTraceTransactionMultiMsgCosmosAsPredecessor() {
	suite.SetupTest()

	msg1, _ := suite.buildEthereumTx()
	msg2, _ := suite.buildEthereumTx()
	msgTarget, _ := suite.buildEthereumTx()

	txBzPred := suite.buildAndEncodeMultiMsgEthTx(msg1, msg2)
	txBzTarget := suite.signAndEncodeEthTx(msgTarget)

	hash1 := msg1.AsTransaction().Hash()
	hash2 := msg2.AsTransaction().Hash()
	hashTarget := msgTarget.AsTransaction().Hash()

	localBlock := types.MakeBlock(1, []types.Tx{txBzPred, txBzTarget}, nil, nil)
	localBlock.ChainID = ChainID

	responseBlock := []*abci.ExecTxResult{
		{
			Code:   0,
			Events: []abci.Event{ethTxEvent(hash1.Hex(), "0"), ethTxEvent(hash2.Hex(), "1")},
		},
		{
			Code:   0,
			Events: []abci.Event{ethTxEvent(hashTarget.Hex(), "2")},
		},
	}

	suite.backend.Indexer = indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), suite.backend.ClientCtx)
	suite.Require().NoError(suite.backend.Indexer.IndexBlock(localBlock, responseBlock))

	queryClient := suite.mockQueryClient()
	client := suite.mockClient()

	_, err := RegisterBlockMultipleTxs(client, 1, []types.Tx{txBzPred, txBzTarget})
	suite.Require().NoError(err)

	RegisterTraceTransactionWithPredecessors(queryClient, msgTarget, []*evmtypes.MsgEthereumTx{msg1, msg2})
	RegisterConsensusParams(client, 1)

	_, err = suite.backend.TraceTransaction(context.Background(), hashTarget, nil)
	suite.Require().NoError(err)
}

// TestTraceTransactionThreeTxBlock exercises the full predecessor-assembly path across
// three Cosmos tx slots: a 1-msg slot, a 2-msg slot, and a 1-msg target slot.
//
// Block layout:  slot0=[msg1], slot1=[msg2, msg3], slot2=[msgTarget]
// Expected predecessors: [msg1, msg2, msg3]
func (suite *BackendTestSuite) TestTraceTransactionThreeTxBlock() {
	suite.SetupTest()

	msg1, _ := suite.buildEthereumTx()
	msg2, _ := suite.buildEthereumTx()
	msg3, _ := suite.buildEthereumTx()
	msgTarget, _ := suite.buildEthereumTx()

	txBz1 := suite.signAndEncodeEthTx(msg1)
	txBzPred2 := suite.buildAndEncodeMultiMsgEthTx(msg2, msg3)
	txBzTarget := suite.signAndEncodeEthTx(msgTarget)

	hash1 := msg1.AsTransaction().Hash()
	hash2 := msg2.AsTransaction().Hash()
	hash3 := msg3.AsTransaction().Hash()
	hashTarget := msgTarget.AsTransaction().Hash()

	localBlock := types.MakeBlock(1, []types.Tx{txBz1, txBzPred2, txBzTarget}, nil, nil)
	localBlock.ChainID = ChainID

	responseBlock := []*abci.ExecTxResult{
		{Code: 0, Events: []abci.Event{ethTxEvent(hash1.Hex(), "0")}},
		{Code: 0, Events: []abci.Event{ethTxEvent(hash2.Hex(), "1"), ethTxEvent(hash3.Hex(), "2")}},
		{Code: 0, Events: []abci.Event{ethTxEvent(hashTarget.Hex(), "3")}},
	}

	suite.backend.Indexer = indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), suite.backend.ClientCtx)
	suite.Require().NoError(suite.backend.Indexer.IndexBlock(localBlock, responseBlock))

	queryClient := suite.mockQueryClient()
	client := suite.mockClient()

	_, err := RegisterBlockMultipleTxs(client, 1, []types.Tx{txBz1, txBzPred2, txBzTarget})
	suite.Require().NoError(err)

	RegisterTraceTransactionWithPredecessors(queryClient, msgTarget, []*evmtypes.MsgEthereumTx{msg1, msg2, msg3})
	RegisterConsensusParams(client, 1)

	_, err = suite.backend.TraceTransaction(context.Background(), hashTarget, nil)
	suite.Require().NoError(err)
}

// derivedTxEvt builds an EventTypeEthereumTx event for a derived transaction (txType=99).
func derivedTxEvt(hash string, txIndex int, sender string, recipient string, gasLimit uint64) abci.Event {
	return abci.Event{
		Type: evmtypes.EventTypeEthereumTx,
		Attributes: []abci.EventAttribute{
			{Key: evmtypes.AttributeKeyEthereumTxHash, Value: hash},
			{Key: evmtypes.AttributeKeyTxIndex, Value: fmt.Sprintf("%d", txIndex)},
			{Key: evmtypes.AttributeKeyTxGasUsed, Value: "21000"},
			{Key: evmtypes.AttributeKeyTxType, Value: fmt.Sprintf("%d", evmtypes.DerivedTxType)},
			{Key: rpctypes.SenderType, Value: sender},
			{Key: evmtypes.AttributeKeyRecipient, Value: recipient},
			{Key: evmtypes.AttributeKeyTxGasLimit, Value: fmt.Sprintf("%d", gasLimit)},
		},
	}
}

// TestTraceTransactionDerivedTxAsPredecessor verifies that a derived tx is correctly
// reconstructed and prepended to the predecessor list when it precedes the EVM target.
//
// Block layout:  slot0=[msg1_evm], slot1=[non-EVM → DerivedTx1], slot2=[msgTarget_evm]
// EthTxIndex:   msg1=0, DerivedTx1=1, msgTarget=2
// Expected predecessors: [msg1, DerivedTx1_synthetic]
func (suite *BackendTestSuite) TestTraceTransactionDerivedTxAsPredecessor() {
	suite.SetupTest()

	msg1, _ := suite.buildEthereumTx()
	txBz1 := suite.signAndEncodeEthTx(msg1)
	hash1 := msg1.AsTransaction().Hash()

	msgTarget, _ := suite.buildEthereumTx()
	txBzTarget := suite.signAndEncodeEthTx(msgTarget)
	hashTarget := msgTarget.AsTransaction().Hash()

	// Slot-1: non-EVM Cosmos tx (empty, no messages).
	dummyTxBz, err := suite.backend.ClientCtx.TxConfig.TxEncoder()(
		suite.backend.ClientCtx.TxConfig.NewTxBuilder().GetTx(),
	)
	suite.Require().NoError(err)

	hashDerived := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	senderAddr := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	recipientAddr := common.HexToAddress("0xabcdef1234567890abcdef1234567890abcdef12")
	gasLimitVal := uint64(50000)

	// Index only slots 0+1; msgTarget (slot2) is deliberately omitted so the KV
	// doesn't assign it a wrong EthTxIndex.
	localBlock := types.MakeBlock(1, []types.Tx{txBz1, dummyTxBz}, nil, nil)
	localBlock.ChainID = ChainID
	indexResults := []*abci.ExecTxResult{
		{Code: 0, Events: []abci.Event{ethTxEvent(hash1.Hex(), "0")}},
		{Code: 0, Events: []abci.Event{}},
	}
	suite.backend.Indexer = indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), suite.backend.ClientCtx)
	suite.Require().NoError(suite.backend.Indexer.IndexBlock(localBlock, indexResults))

	queryClient := suite.mockQueryClient()
	client := suite.mockClient()

	// Actual block has 3 slots.
	_, err = RegisterBlockMultipleTxs(client, 1, []types.Tx{txBz1, dummyTxBz, txBzTarget})
	suite.Require().NoError(err)

	// GetTxByEthHash(hashTarget): KV miss → TxSearch. Returns EthTxIndex=2.
	targetHashQuery := fmt.Sprintf("%s.%s='%s'",
		evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyEthereumTxHash, hashTarget.Hex())
	RegisterTxSearchWithResult(client, targetHashQuery, 1, 2, txBzTarget,
		[]abci.Event{ethTxEvent(hashTarget.Hex(), "2")})

	// GetTxByTxIndex(1, 1): KV miss → TxSearch by eth tx index. Returns DerivedTx1.
	derivedIdxQuery := fmt.Sprintf("tx.height=%d AND %s.%s=%d",
		1, evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyTxIndex, 1)
	RegisterTxSearchWithResult(client, derivedIdxQuery, 1, 1, nil,
		[]abci.Event{derivedTxEvt(hashDerived.Hex(), 1, senderAddr.Hex(), recipientAddr.Hex(), gasLimitVal)})

	// Build the expected derived MsgEthereumTx that parseDerivedTxFromAdditionalFields produces.
	derivedAdditional := &rpctypes.TxResultAdditionalFields{
		Hash:      hashDerived,
		Sender:    senderAddr,
		Recipient: recipientAddr,
		GasUsed:   21000,
		GasLimit:  &gasLimitVal,
		Type:      evmtypes.DerivedTxType,
	}
	derivedMsg := suite.backend.parseDerivedTxFromAdditionalFields(derivedAdditional)
	suite.Require().NotNil(derivedMsg)

	RegisterTraceTransactionWithPredecessors(queryClient, msgTarget, []*evmtypes.MsgEthereumTx{msg1, derivedMsg})
	RegisterConsensusParams(client, 1)

	_, err = suite.backend.TraceTransaction(context.Background(), hashTarget, nil)
	suite.Require().NoError(err)
}

// TestTraceTransactionDerivedTxAsTarget verifies that TraceTransaction can trace a
// derived tx (the target is itself a derived EVM execution, not a Cosmos MsgEthereumTx).
//
// Block layout:  slot0=[msg1_evm], slot1=[non-EVM → DerivedTx1=target]
// EthTxIndex:   msg1=0, DerivedTx1=1
// Expected predecessors: [msg1]
func (suite *BackendTestSuite) TestTraceTransactionDerivedTxAsTarget() {
	suite.SetupTest()

	msg1, _ := suite.buildEthereumTx()
	txBz1 := suite.signAndEncodeEthTx(msg1)
	hash1 := msg1.AsTransaction().Hash()

	// Slot-1: non-EVM Cosmos tx that triggers the derived target.
	dummyTxBz, err := suite.backend.ClientCtx.TxConfig.TxEncoder()(
		suite.backend.ClientCtx.TxConfig.NewTxBuilder().GetTx(),
	)
	suite.Require().NoError(err)

	hashDerivedTarget := common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	senderAddr := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	recipientAddr := common.HexToAddress("0xabcdef1234567890abcdef1234567890abcdef12")
	gasLimitVal := uint64(50000)

	// KV index: only slot 0 (msg1); slot 1 is non-EVM and skipped.
	localBlock := types.MakeBlock(1, []types.Tx{txBz1, dummyTxBz}, nil, nil)
	localBlock.ChainID = ChainID
	indexResults := []*abci.ExecTxResult{
		{Code: 0, Events: []abci.Event{ethTxEvent(hash1.Hex(), "0")}},
		{Code: 0, Events: []abci.Event{}},
	}
	suite.backend.Indexer = indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), suite.backend.ClientCtx)
	suite.Require().NoError(suite.backend.Indexer.IndexBlock(localBlock, indexResults))

	queryClient := suite.mockQueryClient()
	client := suite.mockClient()

	_, err = RegisterBlockMultipleTxs(client, 1, []types.Tx{txBz1, dummyTxBz})
	suite.Require().NoError(err)

	// GetTxByEthHash(hashDerivedTarget): KV miss → TxSearch. Returns derived tx with type=99.
	targetHashQuery := fmt.Sprintf("%s.%s='%s'",
		evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyEthereumTxHash, hashDerivedTarget.Hex())
	RegisterTxSearchWithResult(client, targetHashQuery, 1, 1, nil,
		[]abci.Event{derivedTxEvt(hashDerivedTarget.Hex(), 1, senderAddr.Hex(), recipientAddr.Hex(), gasLimitVal)})

	// BlockResults for the after-loop derived-tx predecessor scan (slot1 contains target).
	RegisterBlockResultsWithTxs(client, 1, []*abci.ExecTxResult{
		{Code: 0, Events: []abci.Event{ethTxEvent(hash1.Hex(), "0")}},
		{Code: 0, Events: []abci.Event{derivedTxEvt(hashDerivedTarget.Hex(), 1, senderAddr.Hex(), recipientAddr.Hex(), gasLimitVal)}},
	})

	// Build the expected target Msg.
	derivedAdditional := &rpctypes.TxResultAdditionalFields{
		Hash:      hashDerivedTarget,
		Sender:    senderAddr,
		Recipient: recipientAddr,
		GasUsed:   21000,
		GasLimit:  &gasLimitVal,
		Type:      evmtypes.DerivedTxType,
	}
	derivedTargetMsg := suite.backend.parseDerivedTxFromAdditionalFields(derivedAdditional)
	suite.Require().NotNil(derivedTargetMsg)

	// Outer loop: i=0 → msg1 (KV hit, TxIndex=0 != target.TxIndex=1, added).
	// After-loop: derived scan finds target immediately and breaks — no extra predecessors.
	RegisterTraceTransactionWithPredecessors(queryClient, derivedTargetMsg, []*evmtypes.MsgEthereumTx{msg1})
	RegisterConsensusParams(client, 1)

	_, err = suite.backend.TraceTransaction(context.Background(), hashDerivedTarget, nil)
	suite.Require().NoError(err)
}

// TestTraceTransactionDerivedTargetInMultiDerivedCosmosTx proves the residual
// Cosmos/Ethereum index-domain bug in the TraceTransaction after-loop (F-2026-17754).
//
// A single Cosmos tx emits MULTIPLE derived EVM txs (e.g. deployUEA + … + executePayload),
// so one Cosmos slot holds derived txs at MsgIndex 0,1,2,… while the Cosmos tx itself has
// 0/1 actual messages. The after-loop iterates `tx.GetMsgs()[0:transaction.MsgIndex]`,
// treating the DERIVED position as a COSMOS-message index — so tracing the 3rd derived tx
// (MsgIndex=2) indexes past the Cosmos message array and panics.
//
// Block layout: slot0 = one non-EVM Cosmos tx that produced 3 derived EVM txs.
// EthTxIndex:   D0=0, D1=1, D2=2 (target); all share TxIndex=0, MsgIndex=0/1/2.
func (suite *BackendTestSuite) TestTraceTransactionDerivedTargetInMultiDerivedCosmosTx() {
	suite.SetupTest()

	// One non-EVM Cosmos tx (no embedded MsgEthereumTx) that produced 3 derived EVM txs.
	dummyTxBz, err := suite.backend.ClientCtx.TxConfig.TxEncoder()(
		suite.backend.ClientCtx.TxConfig.NewTxBuilder().GetTx(),
	)
	suite.Require().NoError(err)

	sender := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	recipient := common.HexToAddress("0xabcdef1234567890abcdef1234567890abcdef12")
	gasLimit := uint64(50000)
	hashD0 := common.HexToHash("0xaa00000000000000000000000000000000000000000000000000000000000000")
	hashD1 := common.HexToHash("0xbb00000000000000000000000000000000000000000000000000000000000000")
	hashD2 := common.HexToHash("0xcc00000000000000000000000000000000000000000000000000000000000000") // target (3rd derived)

	// Empty indexer → lookups fall through to CometBFT TxSearch.
	suite.backend.Indexer = indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), suite.backend.ClientCtx)

	client := suite.mockClient()
	_, err = RegisterBlockMultipleTxs(client, 1, []types.Tx{dummyTxBz}) // single Cosmos slot
	suite.Require().NoError(err)

	// GetTxByEthHash(D2): all 3 derived events are in slot 0; D2 is the 3rd → MsgIndex=2, EthTxIndex=2.
	targetQuery := fmt.Sprintf("%s.%s='%s'",
		evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyEthereumTxHash, hashD2.Hex())
	RegisterTxSearchWithResult(client, targetQuery, 1, 0, nil, []abci.Event{
		derivedTxEvt(hashD0.Hex(), 0, sender.Hex(), recipient.Hex(), gasLimit),
		derivedTxEvt(hashD1.Hex(), 1, sender.Hex(), recipient.Hex(), gasLimit),
		derivedTxEvt(hashD2.Hex(), 2, sender.Hex(), recipient.Hex(), gasLimit),
	})

	// Outer predecessor loop runs for eth-indices 0 and 1; let those miss (they live in the
	// target's own Cosmos slot and are skipped anyway). The panic is in the after-loop.
	for i := 0; i < 2; i++ {
		idxQuery := fmt.Sprintf("tx.height=%d AND %s.%s=%d",
			1, evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyTxIndex, i)
		RegisterTxSearchEmpty(client, idxQuery)
	}

	// With the after-loop now skipped for derived targets, the derived block reconstructs the
	// intra-slot predecessors (D0, D1) of D2 from slot-0 events instead.
	RegisterBlockResultsWithTxs(client, 1, []*abci.ExecTxResult{
		{Code: 0, Events: []abci.Event{
			derivedTxEvt(hashD0.Hex(), 0, sender.Hex(), recipient.Hex(), gasLimit),
			derivedTxEvt(hashD1.Hex(), 1, sender.Hex(), recipient.Hex(), gasLimit),
			derivedTxEvt(hashD2.Hex(), 2, sender.Hex(), recipient.Hex(), gasLimit),
		}},
	})

	queryClient := suite.mockQueryClient()
	RegisterTraceTransactionWithPredecessors(queryClient, nil, nil)
	RegisterConsensusParams(client, 1)

	// Tracing the 3rd derived EVM tx must not panic and must succeed. On the buggy version it
	// panics: the after-loop runs `tx.GetMsgs()[0]` on the (0-message) Cosmos tx with
	// transaction.MsgIndex=2.
	var traceErr error
	suite.Require().NotPanics(func() {
		_, traceErr = suite.backend.TraceTransaction(context.Background(), hashD2, nil)
	}, "tracing the 3rd derived EVM tx of a multi-derived Cosmos tx must not panic (F-2026-17754)")
	suite.Require().NoError(traceErr)
}

// TestTraceTransactionFirstDerivedTargetInMultiDerivedCosmosTx is the passing boundary to
// the multi-derived repro: tracing the FIRST derived EVM tx (MsgIndex=0) of a multi-derived
// Cosmos tx works — the after-loop's standard-message slice runs zero times — and the
// predecessor set is correctly empty. (The 3rd derived tx of the same Cosmos tx panics.)
func (suite *BackendTestSuite) TestTraceTransactionFirstDerivedTargetInMultiDerivedCosmosTx() {
	suite.SetupTest()

	dummyTxBz, err := suite.backend.ClientCtx.TxConfig.TxEncoder()(
		suite.backend.ClientCtx.TxConfig.NewTxBuilder().GetTx(),
	)
	suite.Require().NoError(err)

	sender := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	recipient := common.HexToAddress("0xabcdef1234567890abcdef1234567890abcdef12")
	gasLimit := uint64(50000)
	hashD0 := common.HexToHash("0xd000000000000000000000000000000000000000000000000000000000000000")
	hashD1 := common.HexToHash("0xd100000000000000000000000000000000000000000000000000000000000000")
	hashD2 := common.HexToHash("0xd200000000000000000000000000000000000000000000000000000000000000")
	derivedEvents := []abci.Event{
		derivedTxEvt(hashD0.Hex(), 0, sender.Hex(), recipient.Hex(), gasLimit),
		derivedTxEvt(hashD1.Hex(), 1, sender.Hex(), recipient.Hex(), gasLimit),
		derivedTxEvt(hashD2.Hex(), 2, sender.Hex(), recipient.Hex(), gasLimit),
	}

	suite.backend.Indexer = indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), suite.backend.ClientCtx)

	queryClient := suite.mockQueryClient()
	client := suite.mockClient()
	_, err = RegisterBlockMultipleTxs(client, 1, []types.Tx{dummyTxBz})
	suite.Require().NoError(err)

	// GetTxByEthHash(D0): the 3 derived events are in slot 0; D0 is first → MsgIndex=0, EthTxIndex=0.
	targetQuery := fmt.Sprintf("%s.%s='%s'", evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyEthereumTxHash, hashD0.Hex())
	RegisterTxSearchWithResult(client, targetQuery, 1, 0, nil, derivedEvents)
	// After-loop derived scan reads BlockResults for slot 0.
	RegisterBlockResultsWithTxs(client, 1, []*abci.ExecTxResult{{Code: 0, Events: derivedEvents}})

	var captured *evmtypes.QueryTraceTxRequest
	RegisterTraceTransactionCapture(queryClient, &captured)
	RegisterConsensusParams(client, 1)

	_, err = suite.backend.TraceTransaction(context.Background(), hashD0, nil)
	suite.Require().NoError(err)
	suite.Require().NotNil(captured)
	suite.Require().Empty(captured.Predecessors, "first derived tx of a Cosmos tx has no predecessors")
}

// TestTraceTransactionEvmTargetWithMultiDerivedPredecessors verifies a normal EVM target
// whose predecessors are 3 derived EVM txs emitted by a single earlier Cosmos tx. The
// (fixed) outer loop must enumerate all three by eth-index; this is the working path that
// contrasts the broken derived-target after-loop.
func (suite *BackendTestSuite) TestTraceTransactionEvmTargetWithMultiDerivedPredecessors() {
	suite.SetupTest()

	dummyTxBz, err := suite.backend.ClientCtx.TxConfig.TxEncoder()(
		suite.backend.ClientCtx.TxConfig.NewTxBuilder().GetTx(),
	)
	suite.Require().NoError(err)

	msgTarget, _ := suite.buildEthereumTx()
	txBzTarget := suite.signAndEncodeEthTx(msgTarget)
	hashTarget := msgTarget.AsTransaction().Hash()

	sender := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	recipient := common.HexToAddress("0xabcdef1234567890abcdef1234567890abcdef12")
	gasLimit := uint64(50000)
	derivedHashes := []common.Hash{
		common.HexToHash("0xe000000000000000000000000000000000000000000000000000000000000000"),
		common.HexToHash("0xe100000000000000000000000000000000000000000000000000000000000000"),
		common.HexToHash("0xe200000000000000000000000000000000000000000000000000000000000000"),
	}

	suite.backend.Indexer = indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), suite.backend.ClientCtx)

	queryClient := suite.mockQueryClient()
	client := suite.mockClient()
	_, err = RegisterBlockMultipleTxs(client, 1, []types.Tx{dummyTxBz, txBzTarget})
	suite.Require().NoError(err)

	// GetTxByEthHash(target): standard EVM tx at eth-index 3, Cosmos slot 1.
	targetQuery := fmt.Sprintf("%s.%s='%s'", evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyEthereumTxHash, hashTarget.Hex())
	RegisterTxSearchWithResult(client, targetQuery, 1, 1, txBzTarget, []abci.Event{ethTxEvent(hashTarget.Hex(), "3")})

	// Outer predecessor loop i=0,1,2 → the 3 derived txs (all in Cosmos slot 0).
	for i, h := range derivedHashes {
		idxQuery := fmt.Sprintf("tx.height=%d AND %s.%s=%d", 1, evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyTxIndex, i)
		RegisterTxSearchWithResult(client, idxQuery, 1, 0, nil,
			[]abci.Event{derivedTxEvt(h.Hex(), i, sender.Hex(), recipient.Hex(), gasLimit)})
	}

	var captured *evmtypes.QueryTraceTxRequest
	RegisterTraceTransactionCapture(queryClient, &captured)
	RegisterConsensusParams(client, 1)

	_, err = suite.backend.TraceTransaction(context.Background(), hashTarget, nil)
	suite.Require().NoError(err)
	suite.Require().NotNil(captured)

	suite.Require().Len(captured.Predecessors, len(derivedHashes),
		"all 3 derived txs of the earlier Cosmos tx must be assembled as predecessors")
}

// TestTraceTransactionDerivedTargetWithMixedPredecessors traces a derived target (D3, the
// 3rd derived tx of its Cosmos tx) with MIXED predecessors: a standard EVM tx in an earlier
// Cosmos slot (assembled by the outer loop) AND the two prior derived txs of its own Cosmos
// tx (assembled by the derived-event scan). Correct predecessors: [EVM, D1, D2]. Before the
// F-2026-17754 fix this panicked: the standard-message after-loop ran at MsgIndex=2 over the
// empty Cosmos message array. The fix skips that loop for derived targets.
func (suite *BackendTestSuite) TestTraceTransactionDerivedTargetWithMixedPredecessors() {
	suite.SetupTest()

	msgEvm, _ := suite.buildEthereumTx()
	evmTxBz := suite.signAndEncodeEthTx(msgEvm)
	evmHash := msgEvm.AsTransaction().Hash()

	dummyTxBz, err := suite.backend.ClientCtx.TxConfig.TxEncoder()(
		suite.backend.ClientCtx.TxConfig.NewTxBuilder().GetTx(),
	)
	suite.Require().NoError(err)

	sender := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	recipient := common.HexToAddress("0xabcdef1234567890abcdef1234567890abcdef12")
	gl := uint64(50000)
	hashD1 := common.HexToHash("0xf100000000000000000000000000000000000000000000000000000000000000")
	hashD2 := common.HexToHash("0xf200000000000000000000000000000000000000000000000000000000000000")
	hashD3 := common.HexToHash("0xf300000000000000000000000000000000000000000000000000000000000000") // target

	suite.backend.Indexer = indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), suite.backend.ClientCtx)
	queryClient := suite.mockQueryClient()
	client := suite.mockClient()
	_, err = RegisterBlockMultipleTxs(client, 1, []types.Tx{evmTxBz, dummyTxBz}) // slot0=EVM, slot1=derived carrier
	suite.Require().NoError(err)

	d3Query := fmt.Sprintf("%s.%s='%s'", evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyEthereumTxHash, hashD3.Hex())
	RegisterTxSearchWithResult(client, d3Query, 1, 1, nil, []abci.Event{
		derivedTxEvt(hashD1.Hex(), 1, sender.Hex(), recipient.Hex(), gl),
		derivedTxEvt(hashD2.Hex(), 2, sender.Hex(), recipient.Hex(), gl),
		derivedTxEvt(hashD3.Hex(), 3, sender.Hex(), recipient.Hex(), gl),
	})
	// Outer loop eth-index 0 → EVM (slot0, added); eth-indices 1,2 → D1,D2 (slot1 = target's slot, skipped).
	q0 := fmt.Sprintf("tx.height=%d AND %s.%s=%d", 1, evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyTxIndex, 0)
	RegisterTxSearchWithResult(client, q0, 1, 0, evmTxBz, []abci.Event{ethTxEvent(evmHash.Hex(), "0")})
	q1 := fmt.Sprintf("tx.height=%d AND %s.%s=%d", 1, evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyTxIndex, 1)
	RegisterTxSearchWithResult(client, q1, 1, 1, nil, []abci.Event{derivedTxEvt(hashD1.Hex(), 1, sender.Hex(), recipient.Hex(), gl)})
	q2 := fmt.Sprintf("tx.height=%d AND %s.%s=%d", 1, evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyTxIndex, 2)
	RegisterTxSearchWithResult(client, q2, 1, 1, nil, []abci.Event{derivedTxEvt(hashD2.Hex(), 2, sender.Hex(), recipient.Hex(), gl)})

	RegisterBlockResultsWithTxs(client, 1, []*abci.ExecTxResult{
		{Code: 0, Events: []abci.Event{ethTxEvent(evmHash.Hex(), "0")}},
		{Code: 0, Events: []abci.Event{
			derivedTxEvt(hashD1.Hex(), 1, sender.Hex(), recipient.Hex(), gl),
			derivedTxEvt(hashD2.Hex(), 2, sender.Hex(), recipient.Hex(), gl),
			derivedTxEvt(hashD3.Hex(), 3, sender.Hex(), recipient.Hex(), gl),
		}},
	})

	var captured *evmtypes.QueryTraceTxRequest
	RegisterTraceTransactionCapture(queryClient, &captured)
	RegisterConsensusParams(client, 1)

	_, err = suite.backend.TraceTransaction(context.Background(), hashD3, nil)
	suite.Require().NoError(err)
	suite.Require().NotNil(captured)
	suite.Require().Len(captured.Predecessors, 3, "predecessors must be [EVM, D1, D2]")
}

// TestTraceTransactionSecondDerivedTargetOneMsgCarrier — with a realistic 1-message Cosmos
// carrier, tracing the 2nd derived tx (MsgIndex=1) succeeds: the standard after-loop is now
// skipped for derived targets, and the derived-event scan supplies the predecessor. Before
// the fix the loop ran tx.GetMsgs()[0] (still in bounds for a 1-message carrier, so the 2nd
// derived tx happened not to panic — the threshold is the 3rd). Predecessor: [D1].
func (suite *BackendTestSuite) TestTraceTransactionSecondDerivedTargetOneMsgCarrier() {
	suite.SetupTest()
	carrierBz := suite.oneMsgNonEthCosmosTxBz()

	sender := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	recipient := common.HexToAddress("0xabcdef1234567890abcdef1234567890abcdef12")
	gl := uint64(50000)
	hashD1 := common.HexToHash("0xc100000000000000000000000000000000000000000000000000000000000000")
	hashD2 := common.HexToHash("0xc200000000000000000000000000000000000000000000000000000000000000") // target (2nd)

	suite.backend.Indexer = indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), suite.backend.ClientCtx)
	queryClient := suite.mockQueryClient()
	client := suite.mockClient()
	_, err := RegisterBlockMultipleTxs(client, 1, []types.Tx{carrierBz})
	suite.Require().NoError(err)

	d2Query := fmt.Sprintf("%s.%s='%s'", evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyEthereumTxHash, hashD2.Hex())
	RegisterTxSearchWithResult(client, d2Query, 1, 0, nil, []abci.Event{
		derivedTxEvt(hashD1.Hex(), 0, sender.Hex(), recipient.Hex(), gl),
		derivedTxEvt(hashD2.Hex(), 1, sender.Hex(), recipient.Hex(), gl),
	})
	q0 := fmt.Sprintf("tx.height=%d AND %s.%s=%d", 1, evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyTxIndex, 0)
	RegisterTxSearchWithResult(client, q0, 1, 0, nil, []abci.Event{derivedTxEvt(hashD1.Hex(), 0, sender.Hex(), recipient.Hex(), gl)})
	RegisterBlockResultsWithTxs(client, 1, []*abci.ExecTxResult{
		{Code: 0, Events: []abci.Event{
			derivedTxEvt(hashD1.Hex(), 0, sender.Hex(), recipient.Hex(), gl),
			derivedTxEvt(hashD2.Hex(), 1, sender.Hex(), recipient.Hex(), gl),
		}},
	})

	var captured *evmtypes.QueryTraceTxRequest
	RegisterTraceTransactionCapture(queryClient, &captured)
	RegisterConsensusParams(client, 1)

	_, err = suite.backend.TraceTransaction(context.Background(), hashD2, nil)
	suite.Require().NoError(err)
	suite.Require().NotNil(captured)
	suite.Require().Len(captured.Predecessors, 1, "only the 1st derived tx (D1) precedes D2")
}

// TestTraceTransactionThirdDerivedTargetOneMsgCarrier — same 1-message carrier as above, but
// tracing the 3rd derived tx (MsgIndex=2). This is the threshold the previous test approaches:
// before the fix the after-loop indexed GetMsgs()[1] on the length-1 Cosmos tx and panicked
// (F-2026-17754). With the loop skipped for derived targets it now succeeds; the two prior
// derived txs come from the derived-event scan. Predecessors: [D1, D2].
func (suite *BackendTestSuite) TestTraceTransactionThirdDerivedTargetOneMsgCarrier() {
	suite.SetupTest()
	carrierBz := suite.oneMsgNonEthCosmosTxBz()

	sender := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	recipient := common.HexToAddress("0xabcdef1234567890abcdef1234567890abcdef12")
	gl := uint64(50000)
	hashD1 := common.HexToHash("0x9100000000000000000000000000000000000000000000000000000000000000")
	hashD2 := common.HexToHash("0x9200000000000000000000000000000000000000000000000000000000000000")
	hashD3 := common.HexToHash("0x9300000000000000000000000000000000000000000000000000000000000000") // target (3rd)

	suite.backend.Indexer = indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), suite.backend.ClientCtx)
	queryClient := suite.mockQueryClient()
	client := suite.mockClient()
	_, err := RegisterBlockMultipleTxs(client, 1, []types.Tx{carrierBz})
	suite.Require().NoError(err)

	d3Query := fmt.Sprintf("%s.%s='%s'", evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyEthereumTxHash, hashD3.Hex())
	RegisterTxSearchWithResult(client, d3Query, 1, 0, nil, []abci.Event{
		derivedTxEvt(hashD1.Hex(), 0, sender.Hex(), recipient.Hex(), gl),
		derivedTxEvt(hashD2.Hex(), 1, sender.Hex(), recipient.Hex(), gl),
		derivedTxEvt(hashD3.Hex(), 2, sender.Hex(), recipient.Hex(), gl),
	})
	q0 := fmt.Sprintf("tx.height=%d AND %s.%s=%d", 1, evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyTxIndex, 0)
	RegisterTxSearchWithResult(client, q0, 1, 0, nil, []abci.Event{derivedTxEvt(hashD1.Hex(), 0, sender.Hex(), recipient.Hex(), gl)})
	q1 := fmt.Sprintf("tx.height=%d AND %s.%s=%d", 1, evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyTxIndex, 1)
	RegisterTxSearchWithResult(client, q1, 1, 0, nil, []abci.Event{derivedTxEvt(hashD2.Hex(), 1, sender.Hex(), recipient.Hex(), gl)})
	RegisterBlockResultsWithTxs(client, 1, []*abci.ExecTxResult{
		{Code: 0, Events: []abci.Event{
			derivedTxEvt(hashD1.Hex(), 0, sender.Hex(), recipient.Hex(), gl),
			derivedTxEvt(hashD2.Hex(), 1, sender.Hex(), recipient.Hex(), gl),
			derivedTxEvt(hashD3.Hex(), 2, sender.Hex(), recipient.Hex(), gl),
		}},
	})

	var captured *evmtypes.QueryTraceTxRequest
	RegisterTraceTransactionCapture(queryClient, &captured)
	RegisterConsensusParams(client, 1)

	_, err = suite.backend.TraceTransaction(context.Background(), hashD3, nil)
	suite.Require().NoError(err)
	suite.Require().NotNil(captured)
	suite.Require().Len(captured.Predecessors, 2, "D1 and D2 precede D3")
}

// TestTraceTransactionDerivedTargetViaKVIndexerHit traces a derived target resolved via the
// KV indexer HIT path (not tx_search): the tx is indexed, so GetTxByEthHash returns it and
// derivedTxAdditionalFields rebuilds its fields from BlockResults. No TxSearch is mocked, so
// a regression that falls through to tx_search would fail on an unexpected mock call.
func (suite *BackendTestSuite) TestTraceTransactionDerivedTargetViaKVIndexerHit() {
	suite.SetupTest()

	dummyTxBz, err := suite.backend.ClientCtx.TxConfig.TxEncoder()(
		suite.backend.ClientCtx.TxConfig.NewTxBuilder().GetTx(),
	)
	suite.Require().NoError(err)

	sender := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	recipient := common.HexToAddress("0xabcdef1234567890abcdef1234567890abcdef12")
	gl := uint64(50000)
	hashD0 := common.HexToHash("0xb000000000000000000000000000000000000000000000000000000000000000")
	derivedEvents := []abci.Event{derivedTxEvt(hashD0.Hex(), 0, sender.Hex(), recipient.Hex(), gl)}

	// Index the derived tx so GetTxByEthHash hits the KV indexer.
	localBlock := types.MakeBlock(1, []types.Tx{dummyTxBz}, nil, nil)
	localBlock.ChainID = ChainID
	suite.backend.Indexer = indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), suite.backend.ClientCtx)
	suite.Require().NoError(suite.backend.Indexer.IndexBlock(localBlock, []*abci.ExecTxResult{{Code: 0, Events: derivedEvents}}))

	queryClient := suite.mockQueryClient()
	client := suite.mockClient()
	_, err = RegisterBlockMultipleTxs(client, 1, []types.Tx{dummyTxBz})
	suite.Require().NoError(err)
	// BlockResults backs both derivedTxAdditionalFields (KV-hit reconstruction) and the after-loop.
	RegisterBlockResultsWithTxs(client, 1, []*abci.ExecTxResult{{Code: 0, Events: derivedEvents}})

	var captured *evmtypes.QueryTraceTxRequest
	RegisterTraceTransactionCapture(queryClient, &captured)
	RegisterConsensusParams(client, 1)

	_, err = suite.backend.TraceTransaction(context.Background(), hashD0, nil)
	suite.Require().NoError(err)
	suite.Require().NotNil(captured)
	suite.Require().Empty(captured.Predecessors, "only derived tx in the block has no predecessors")
}
