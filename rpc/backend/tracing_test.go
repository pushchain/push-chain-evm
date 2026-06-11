package backend

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"

	abci "github.com/cometbft/cometbft/abci/types"
	tmrpctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cometbft/cometbft/types"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/evm/crypto/ethsecp256k1"
	"github.com/cosmos/evm/indexer"
	"github.com/cosmos/evm/rpc/backend/mocks"
	rpctypes "github.com/cosmos/evm/rpc/types"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	"cosmossdk.io/log"

	"github.com/cosmos/cosmos-sdk/crypto"
)

func (suite *BackendTestSuite) TestTraceTransaction() {
	msgEthereumTx, _ := suite.buildEthereumTx()
	msgEthereumTx2, _ := suite.buildEthereumTx()

	txHash := msgEthereumTx.AsTransaction().Hash()
	txHash2 := msgEthereumTx2.AsTransaction().Hash()

	priv, _ := ethsecp256k1.GenerateKey()
	from := common.BytesToAddress(priv.PubKey().Address().Bytes())
	armor := crypto.EncryptArmorPrivKey(priv, "", "eth_secp256k1")
	_ = suite.backend.clientCtx.Keyring.ImportPrivKey("test_key", armor, "")

	ethSigner := ethtypes.LatestSigner(suite.backend.ChainConfig())

	txEncoder := suite.backend.clientCtx.TxConfig.TxEncoder()

	msgEthereumTx.From = from.String()
	_ = msgEthereumTx.Sign(ethSigner, suite.signer)

	baseDenom := evmtypes.GetEVMCoinDenom()

	tx, _ := msgEthereumTx.BuildTx(suite.backend.clientCtx.TxConfig.NewTxBuilder(), baseDenom)
	txBz, _ := txEncoder(tx)

	msgEthereumTx2.From = from.String()
	_ = msgEthereumTx2.Sign(ethSigner, suite.signer)

	tx2, _ := msgEthereumTx.BuildTx(suite.backend.clientCtx.TxConfig.NewTxBuilder(), baseDenom)
	txBz2, _ := txEncoder(tx2)

	testCases := []struct {
		name          string
		registerMock  func()
		block         *types.Block
		responseBlock []*abci.ExecTxResult
		expResult     interface{}
		expPass       bool
	}{
		{
			"fail - tx not found",
			func() {
				client := suite.backend.clientCtx.Client.(*mocks.Client)
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
			nil,
			false,
		},
		{
			"fail - block not found",
			func() {
				// var header metadata.MD
				client := suite.backend.clientCtx.Client.(*mocks.Client)
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
			map[string]interface{}{"test": "hello"},
			false,
		},
		{
			"pass - transaction found in a block with multiple transactions",
			func() {
				var (
					queryClient       = suite.backend.queryClient.QueryClient.(*mocks.EVMQueryClient)
					client            = suite.backend.clientCtx.Client.(*mocks.Client)
					height      int64 = 1
				)
				_, err := RegisterBlockMultipleTxs(client, height, []types.Tx{txBz, txBz2})
				suite.Require().NoError(err)
				RegisterTraceTransactionWithPredecessors(queryClient, msgEthereumTx, nil)
				RegisterConsensusParams(client, height)
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
			map[string]interface{}{"test": "hello"},
			true,
		},
		{
			"pass - transaction found",
			func() {
				var (
					queryClient       = suite.backend.queryClient.QueryClient.(*mocks.EVMQueryClient)
					client            = suite.backend.clientCtx.Client.(*mocks.Client)
					height      int64 = 1
				)
				_, err := RegisterBlock(client, height, txBz)
				suite.Require().NoError(err)
				RegisterTraceTransaction(queryClient, msgEthereumTx)
				RegisterConsensusParams(client, height)
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
			map[string]interface{}{"test": "hello"},
			true,
		},
	}

	for _, tc := range testCases {
		suite.Run(fmt.Sprintf("case %s", tc.name), func() {
			suite.SetupTest() // reset test and queries
			tc.registerMock()

			db := dbm.NewMemDB()
			suite.backend.indexer = indexer.NewKVIndexer(db, log.NewNopLogger(), suite.backend.clientCtx)

			err := suite.backend.indexer.IndexBlock(tc.block, tc.responseBlock)
			suite.Require().NoError(err)
			txResult, err := suite.backend.TraceTransaction(txHash, nil)

			if tc.expPass {
				suite.Require().NoError(err)
				suite.Require().Equal(tc.expResult, txResult)
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

	// Build two signed transactions; hashes computed AFTER signing so they match
	// what IndexBlock stores.
	msgFirst, _ := suite.buildEthereumTx()
	txBzFirst := suite.signAndEncodeEthTx(msgFirst)
	txHashFirst := msgFirst.AsTransaction().Hash()

	msgTarget, _ := suite.buildEthereumTx()
	txBzTarget := suite.signAndEncodeEthTx(msgTarget)
	txHashTarget := msgTarget.AsTransaction().Hash()

	// Block with two Cosmos txs: EthTxIndex == TxIndex == 0 for msgFirst,
	// EthTxIndex == TxIndex == 1 for msgTarget.
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

	db := dbm.NewMemDB()
	suite.backend.indexer = indexer.NewKVIndexer(db, log.NewNopLogger(), suite.backend.clientCtx)
	err := suite.backend.indexer.IndexBlock(localBlock, responseBlock)
	suite.Require().NoError(err)

	queryClient := suite.backend.queryClient.QueryClient.(*mocks.EVMQueryClient)
	client := suite.backend.clientCtx.Client.(*mocks.Client)

	_, err = RegisterBlockMultipleTxs(client, 1, []types.Tx{txBzFirst, txBzTarget})
	suite.Require().NoError(err)

	// EthTxIndex=1: the predecessor loop runs once (i=0) and fetches msgFirst
	// (TxIndex=0, MsgIndex=0). With the Gap-1 fix the message AT MsgIndex is
	// added directly, so msgFirst is a predecessor of msgTarget.
	RegisterTraceTransactionWithPredecessors(queryClient, msgTarget, []*evmtypes.MsgEthereumTx{msgFirst})
	RegisterConsensusParams(client, 1)

	txResult, err := suite.backend.TraceTransaction(txHashTarget, nil)
	suite.Require().NoError(err)
	suite.Require().Equal(map[string]interface{}{"test": "hello"}, txResult)
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
// packed into a single Cosmos tx slot (TxIndex=0 for both). The same-Cosmos-tx guard
// must fire on every outer-loop iteration, leaving the after-loop to supply the sole
// predecessor.
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

	db := dbm.NewMemDB()
	suite.backend.indexer = indexer.NewKVIndexer(db, log.NewNopLogger(), suite.backend.clientCtx)
	suite.Require().NoError(suite.backend.indexer.IndexBlock(localBlock, responseBlock))

	queryClient := suite.backend.queryClient.QueryClient.(*mocks.EVMQueryClient)
	client := suite.backend.clientCtx.Client.(*mocks.Client)

	_, err := RegisterBlockMultipleTxs(client, 1, []types.Tx{txBzMulti})
	suite.Require().NoError(err)

	// Outer loop (i=0): GetTxByTxIndex→TxIndex=0 == target.TxIndex=0 → skipped by
	// same-Cosmos-tx guard. After-loop (index=1, i=0): adds msg1.
	RegisterTraceTransactionWithPredecessors(queryClient, msgTarget, []*evmtypes.MsgEthereumTx{msg1})
	RegisterConsensusParams(client, 1)

	txResult, err := suite.backend.TraceTransaction(hashTarget, nil)
	suite.Require().NoError(err)
	suite.Require().Equal(map[string]interface{}{"test": "hello"}, txResult)
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

	db := dbm.NewMemDB()
	suite.backend.indexer = indexer.NewKVIndexer(db, log.NewNopLogger(), suite.backend.clientCtx)
	suite.Require().NoError(suite.backend.indexer.IndexBlock(localBlock, responseBlock))

	queryClient := suite.backend.queryClient.QueryClient.(*mocks.EVMQueryClient)
	client := suite.backend.clientCtx.Client.(*mocks.Client)

	_, err := RegisterBlockMultipleTxs(client, 1, []types.Tx{txBzMulti})
	suite.Require().NoError(err)

	// Outer loop i=0,1: TxIndex=0==target.TxIndex=0 → both skipped.
	// After-loop (index=2): adds msg1 (i=0) and msg2 (i=1).
	RegisterTraceTransactionWithPredecessors(queryClient, msgTarget, []*evmtypes.MsgEthereumTx{msg1, msg2})
	RegisterConsensusParams(client, 1)

	txResult, err := suite.backend.TraceTransaction(hashTarget, nil)
	suite.Require().NoError(err)
	suite.Require().Equal(map[string]interface{}{"test": "hello"}, txResult)
}

// TestTraceTransactionMultiMsgCosmosAsPredecessor traces a single-message target
// whose sole predecessor is a two-message Cosmos tx. Both messages of the predecessor
// tx must appear in the predecessor list — this validates the Gap-5 fix (direct add
// at MsgIndex instead of the old inner loop that ran j<MsgIndex and missed the last
// message).
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

	db := dbm.NewMemDB()
	suite.backend.indexer = indexer.NewKVIndexer(db, log.NewNopLogger(), suite.backend.clientCtx)
	suite.Require().NoError(suite.backend.indexer.IndexBlock(localBlock, responseBlock))

	queryClient := suite.backend.queryClient.QueryClient.(*mocks.EVMQueryClient)
	client := suite.backend.clientCtx.Client.(*mocks.Client)

	_, err := RegisterBlockMultipleTxs(client, 1, []types.Tx{txBzPred, txBzTarget})
	suite.Require().NoError(err)

	// Outer loop i=0: adds msg1 (TxIndex=0, MsgIndex=0).
	// Outer loop i=1: adds msg2 (TxIndex=0, MsgIndex=1) — validates Gap-5 fix.
	// After-loop: MsgIndex=0, no iterations.
	RegisterTraceTransactionWithPredecessors(queryClient, msgTarget, []*evmtypes.MsgEthereumTx{msg1, msg2})
	RegisterConsensusParams(client, 1)

	txResult, err := suite.backend.TraceTransaction(hashTarget, nil)
	suite.Require().NoError(err)
	suite.Require().Equal(map[string]interface{}{"test": "hello"}, txResult)
}

// TestTraceTransactionThreeTxBlock exercises the full predecessor-assembly path across
// three Cosmos tx slots: a 1-msg slot, a 2-msg slot, and a 1-msg target slot. All six
// fix points interact: loop bound uses EthTxIndex (not TxIndex), Cosmos array accesses
// use predecessorTx.TxIndex, same-slot guard fires for none of them, and each outer
// iteration adds exactly the message at its MsgIndex.
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

	db := dbm.NewMemDB()
	suite.backend.indexer = indexer.NewKVIndexer(db, log.NewNopLogger(), suite.backend.clientCtx)
	suite.Require().NoError(suite.backend.indexer.IndexBlock(localBlock, responseBlock))

	queryClient := suite.backend.queryClient.QueryClient.(*mocks.EVMQueryClient)
	client := suite.backend.clientCtx.Client.(*mocks.Client)

	_, err := RegisterBlockMultipleTxs(client, 1, []types.Tx{txBz1, txBzPred2, txBzTarget})
	suite.Require().NoError(err)

	// i=0 → msg1 (slot0, MsgIndex=0)
	// i=1 → msg2 (slot1, MsgIndex=0)
	// i=2 → msg3 (slot1, MsgIndex=1) — validates Gap-5 (direct add at MsgIndex)
	// After-loop: MsgIndex=0, no iterations.
	RegisterTraceTransactionWithPredecessors(queryClient, msgTarget, []*evmtypes.MsgEthereumTx{msg1, msg2, msg3})
	RegisterConsensusParams(client, 1)

	txResult, err := suite.backend.TraceTransaction(hashTarget, nil)
	suite.Require().NoError(err)
	suite.Require().Equal(map[string]interface{}{"test": "hello"}, txResult)
}

// derivedTxEvt builds an EventTypeEthereumTx event for a derived transaction
// (txType=99). txIndex is the EthTxIndex, sender/recipient are hex-encoded addresses.
// The attribute count exceeds 2 so ParseTxResult picks eventFormat1 and calls newTx.
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

// TestTraceTransactionDerivedTxAsPredecessor verifies that a derived tx (EVM execution
// triggered internally by a Cosmos module, stored only as ABCI events with no Cosmos
// tx envelope) is correctly reconstructed and prepended to the predecessor list when it
// precedes the EVM target in Ethereum execution order.
//
// Block layout:  slot0=[msg1_evm], slot1=[non-EVM → DerivedTx1], slot2=[msgTarget_evm]
// EthTxIndex:   msg1=0, DerivedTx1=1, msgTarget=2
//
// The KV indexer assigns EthTxIndex=1 to msgTarget (it skips the non-EVM slot and
// doesn't count derived txs). To force the correct EthTxIndex=2, the test does NOT
// index slot 2; msgTarget is resolved via TxSearch instead. DerivedTx1 is also found
// via TxSearch (KV never stores derived txs).
//
// Expected predecessors: [msg1, DerivedTx1_synthetic]
func (suite *BackendTestSuite) TestTraceTransactionDerivedTxAsPredecessor() {
	suite.SetupTest()

	// Build slot-0 EVM tx (indexed in KV at EthTxIndex=0).
	msg1, _ := suite.buildEthereumTx()
	txBz1 := suite.signAndEncodeEthTx(msg1)
	hash1 := msg1.AsTransaction().Hash()

	// Build slot-2 EVM target (NOT indexed in KV so TxSearch is used).
	msgTarget, _ := suite.buildEthereumTx()
	txBzTarget := suite.signAndEncodeEthTx(msgTarget)
	hashTarget := msgTarget.AsTransaction().Hash()

	// Slot-1 is a non-EVM Cosmos tx that triggers the derived tx.
	// An empty tx builder produces a valid, decodable Cosmos tx with no messages.
	dummyTxBz, err := suite.backend.clientCtx.TxConfig.TxEncoder()(
		suite.backend.clientCtx.TxConfig.NewTxBuilder().GetTx(),
	)
	suite.Require().NoError(err)

	// Derived tx parameters.
	hashDerived := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	senderAddr := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	recipientAddr := common.HexToAddress("0xabcdef1234567890abcdef1234567890abcdef12")
	gasLimitVal := uint64(50000)

	// Index a 2-slot block (slot0=EVM, slot1=non-EVM) so KV stores only msg1@EthTxIndex=0.
	// slot2 (msgTarget) is deliberately omitted so the KV never assigns it a wrong index.
	localBlock := types.MakeBlock(1, []types.Tx{txBz1, dummyTxBz}, nil, nil)
	localBlock.ChainID = ChainID
	indexResults := []*abci.ExecTxResult{
		{Code: 0, Events: []abci.Event{ethTxEvent(hash1.Hex(), "0")}},
		{Code: 0, Events: []abci.Event{}},
	}
	db := dbm.NewMemDB()
	suite.backend.indexer = indexer.NewKVIndexer(db, log.NewNopLogger(), suite.backend.clientCtx)
	suite.Require().NoError(suite.backend.indexer.IndexBlock(localBlock, indexResults))

	queryClient := suite.backend.queryClient.QueryClient.(*mocks.EVMQueryClient)
	client := suite.backend.clientCtx.Client.(*mocks.Client)

	// Actual block has 3 slots.
	_, err = RegisterBlockMultipleTxs(client, 1, []types.Tx{txBz1, dummyTxBz, txBzTarget})
	suite.Require().NoError(err)

	// GetTxByEthHash(hashTarget): KV miss → TxSearch by ethereum hash.
	// Returns target with EthTxIndex=2 (the true execution count including DerivedTx1).
	targetHashQuery := fmt.Sprintf("%s.%s='%s'",
		evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyEthereumTxHash, hashTarget.Hex())
	RegisterTxSearchWithResult(client, targetHashQuery, 1, 2, txBzTarget,
		[]abci.Event{ethTxEvent(hashTarget.Hex(), "2")})

	// GetTxByTxIndex(1, 1): KV miss → TxSearch by eth tx index.
	// Returns DerivedTx1 with txAdditional (type=99).
	derivedIdxQuery := fmt.Sprintf("tx.height=%d AND %s.%s=%d",
		1, evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyTxIndex, 1)
	RegisterTxSearchWithResult(client, derivedIdxQuery, 1, 1, nil,
		[]abci.Event{derivedTxEvt(hashDerived.Hex(), 1, senderAddr.Hex(), recipientAddr.Hex(), gasLimitVal)})

	// Compute the expected MsgEthereumTx that parseDerivedTxFromAdditionalFields produces
	// from the TxSearch result, so the TraceTx mock can match it exactly.
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

	// Outer loop: i=0→msg1 (KV hit), i=1→DerivedTx1 (KV miss, derived branch).
	// After-loop: MsgIndex=0, 0 normal-msg iterations, additional=nil.
	RegisterTraceTransactionWithPredecessors(queryClient, msgTarget, []*evmtypes.MsgEthereumTx{msg1, derivedMsg})
	RegisterConsensusParams(client, 1)

	txResult, err := suite.backend.TraceTransaction(hashTarget, nil)
	suite.Require().NoError(err)
	suite.Require().Equal(map[string]interface{}{"test": "hello"}, txResult)
}

// TestTraceTransactionDerivedTxAsTarget verifies that TraceTransaction can trace a
// derived tx (the target is itself a derived EVM execution, not a Cosmos MsgEthereumTx).
// The derived tx is reconstructed from TxSearch event attributes; the after-loop scans
// block results for intra-slot derived-tx predecessors, finds the target as the first
// entry, and immediately stops — so only the outer-loop predecessor (msg1) is included.
//
// Block layout:  slot0=[msg1_evm], slot1=[non-EVM → DerivedTx1=target]
// EthTxIndex:   msg1=0, DerivedTx1=1
//
// Expected predecessors: [msg1]
func (suite *BackendTestSuite) TestTraceTransactionDerivedTxAsTarget() {
	suite.SetupTest()

	// Slot-0 EVM predecessor, indexed in KV at EthTxIndex=0.
	msg1, _ := suite.buildEthereumTx()
	txBz1 := suite.signAndEncodeEthTx(msg1)
	hash1 := msg1.AsTransaction().Hash()

	// Slot-1: non-EVM Cosmos tx that triggers the derived target.
	dummyTxBz, err := suite.backend.clientCtx.TxConfig.TxEncoder()(
		suite.backend.clientCtx.TxConfig.NewTxBuilder().GetTx(),
	)
	suite.Require().NoError(err)

	// Derived target tx parameters.
	hashDerivedTarget := common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	senderAddr := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	recipientAddr := common.HexToAddress("0xabcdef1234567890abcdef1234567890abcdef12")
	gasLimitVal := uint64(50000)

	// KV index: only slot 0 (msg1); slot 1 is non-EVM and skipped by the indexer.
	localBlock := types.MakeBlock(1, []types.Tx{txBz1, dummyTxBz}, nil, nil)
	localBlock.ChainID = ChainID
	indexResults := []*abci.ExecTxResult{
		{Code: 0, Events: []abci.Event{ethTxEvent(hash1.Hex(), "0")}},
		{Code: 0, Events: []abci.Event{}},
	}
	db := dbm.NewMemDB()
	suite.backend.indexer = indexer.NewKVIndexer(db, log.NewNopLogger(), suite.backend.clientCtx)
	suite.Require().NoError(suite.backend.indexer.IndexBlock(localBlock, indexResults))

	queryClient := suite.backend.queryClient.QueryClient.(*mocks.EVMQueryClient)
	client := suite.backend.clientCtx.Client.(*mocks.Client)

	_, err = RegisterBlockMultipleTxs(client, 1, []types.Tx{txBz1, dummyTxBz})
	suite.Require().NoError(err)

	// GetTxByEthHash(hashDerivedTarget): KV miss (derived txs are never indexed) →
	// TxSearch by ethereum hash. Returns the derived tx with type=99 and txAdditional.
	targetHashQuery := fmt.Sprintf("%s.%s='%s'",
		evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyEthereumTxHash, hashDerivedTarget.Hex())
	RegisterTxSearchWithResult(client, targetHashQuery, 1, 1, nil,
		[]abci.Event{derivedTxEvt(hashDerivedTarget.Hex(), 1, senderAddr.Hex(), recipientAddr.Hex(), gasLimitVal)})

	// BlockResults for the after-loop derived-tx predecessor scan.
	// The scan finds hashDerivedTarget as the first entry in slot 1 and breaks immediately
	// (it is the target, not a predecessor), so no additional predecessors are added.
	RegisterBlockResultsWithTxs(client, 1, []*abci.ExecTxResult{
		{Code: 0, Events: []abci.Event{ethTxEvent(hash1.Hex(), "0")}},
		{Code: 0, Events: []abci.Event{derivedTxEvt(hashDerivedTarget.Hex(), 1, senderAddr.Hex(), recipientAddr.Hex(), gasLimitVal)}},
	})

	// Compute the Msg that parseDerivedTxFromAdditionalFields will produce for the target.
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

	// Outer loop: i=0→msg1 (KV hit, TxIndex=0 != target.TxIndex=1, added).
	// After-loop: MsgIndex=0 → 0 normal-msg iterations; derived-tx scan breaks at target.
	RegisterTraceTransactionWithPredecessors(queryClient, derivedTargetMsg, []*evmtypes.MsgEthereumTx{msg1})
	RegisterConsensusParams(client, 1)

	txResult, err := suite.backend.TraceTransaction(hashDerivedTarget, nil)
	suite.Require().NoError(err)
	suite.Require().Equal(map[string]interface{}{"test": "hello"}, txResult)
}

func (suite *BackendTestSuite) TestTraceBlock() {
	msgEthTx, bz := suite.buildEthereumTx()
	emptyBlock := types.MakeBlock(1, []types.Tx{}, nil, nil)
	emptyBlock.ChainID = ChainID
	filledBlock := types.MakeBlock(1, []types.Tx{bz}, nil, nil)
	filledBlock.ChainID = ChainID
	resBlockEmpty := tmrpctypes.ResultBlock{Block: emptyBlock, BlockID: emptyBlock.LastBlockID}
	resBlockFilled := tmrpctypes.ResultBlock{Block: filledBlock, BlockID: filledBlock.LastBlockID}

	testCases := []struct {
		name            string
		registerMock    func()
		expTraceResults []*evmtypes.TxTraceResult
		resBlock        *tmrpctypes.ResultBlock
		config          *evmtypes.TraceConfig
		expPass         bool
	}{
		{
			"pass - no transaction returning empty array",
			func() {},
			[]*evmtypes.TxTraceResult{},
			&resBlockEmpty,
			&evmtypes.TraceConfig{},
			true,
		},
		{
			"fail - cannot unmarshal data",
			func() {
				queryClient := suite.backend.queryClient.QueryClient.(*mocks.EVMQueryClient)
				client := suite.backend.clientCtx.Client.(*mocks.Client)
				RegisterTraceBlock(queryClient, []*evmtypes.MsgEthereumTx{msgEthTx})
				RegisterConsensusParams(client, 1)
			},
			[]*evmtypes.TxTraceResult{},
			&resBlockFilled,
			&evmtypes.TraceConfig{},
			false,
		},
	}

	for _, tc := range testCases {
		suite.Run(fmt.Sprintf("case %s", tc.name), func() {
			suite.SetupTest() // reset test and queries
			tc.registerMock()

			traceResults, err := suite.backend.TraceBlock(1, tc.config, tc.resBlock)

			if tc.expPass {
				suite.Require().NoError(err)
				suite.Require().Equal(tc.expTraceResults, traceResults)
			} else {
				suite.Require().Error(err)
			}
		})
	}
}
