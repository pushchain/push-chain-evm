package indexer_test

import (
	"math/big"
	"strconv"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cmttypes "github.com/cometbft/cometbft/types"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/evm/crypto/ethsecp256k1"
	"github.com/cosmos/evm/indexer"
	"github.com/cosmos/evm/testutil/constants"
	"github.com/cosmos/evm/testutil/integration/os/network"
	utiltx "github.com/cosmos/evm/testutil/tx"
	"github.com/cosmos/evm/x/vm/types"

	"cosmossdk.io/log"

	"github.com/cosmos/cosmos-sdk/client"
)

func TestKVIndexer(t *testing.T) {
	priv, err := ethsecp256k1.GenerateKey()
	require.NoError(t, err)
	from := common.BytesToAddress(priv.PubKey().Address().Bytes())
	signer := utiltx.NewSigner(priv)
	ethSigner := ethtypes.LatestSignerForChainID(nil)

	to := common.BigToAddress(big.NewInt(1))
	ethTxParams := types.EvmTxArgs{
		Nonce:    0,
		To:       &to,
		Amount:   big.NewInt(1000),
		GasLimit: 21000,
	}
	tx := types.NewTx(&ethTxParams)
	tx.From = from.Hex()
	require.NoError(t, tx.Sign(ethSigner, signer))
	txHash := tx.AsTransaction().Hash()

	nw := network.New()
	encodingConfig := nw.GetEncodingConfig()
	clientCtx := client.Context{}.WithTxConfig(encodingConfig.TxConfig).WithCodec(encodingConfig.Codec)

	// build cosmos-sdk wrapper tx
	tmTx, err := tx.BuildTx(clientCtx.TxConfig.NewTxBuilder(), constants.ExampleAttoDenom)
	require.NoError(t, err)
	txBz, err := clientCtx.TxConfig.TxEncoder()(tmTx)
	require.NoError(t, err)

	// build an invalid wrapper tx
	builder := clientCtx.TxConfig.NewTxBuilder()
	require.NoError(t, builder.SetMsgs(tx))
	tmTx2 := builder.GetTx()
	txBz2, err := clientCtx.TxConfig.TxEncoder()(tmTx2)
	require.NoError(t, err)

	testCases := []struct {
		name        string
		block       *cmttypes.Block
		blockResult []*abci.ExecTxResult
		expSuccess  bool
	}{
		{
			"success, format 1",
			&cmttypes.Block{Header: cmttypes.Header{Height: 1}, Data: cmttypes.Data{Txs: []cmttypes.Tx{txBz}}},
			[]*abci.ExecTxResult{
				{
					Code: 0,
					Events: []abci.Event{
						{Type: types.EventTypeEthereumTx, Attributes: []abci.EventAttribute{
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
		{
			"success, format 2",
			&cmttypes.Block{Header: cmttypes.Header{Height: 1}, Data: cmttypes.Data{Txs: []cmttypes.Tx{txBz}}},
			[]*abci.ExecTxResult{
				{
					Code: 0,
					Events: []abci.Event{
						{Type: types.EventTypeEthereumTx, Attributes: []abci.EventAttribute{
							{Key: "ethereumTxHash", Value: txHash.Hex()},
							{Key: "txIndex", Value: "0"},
						}},
						{Type: types.EventTypeEthereumTx, Attributes: []abci.EventAttribute{
							{Key: "amount", Value: "1000"},
							{Key: "txGasUsed", Value: "21000"},
							{Key: "txHash", Value: "14A84ED06282645EFBF080E0B7ED80D8D8D6A36337668A12B5F229F81CDD3F57"},
							{Key: "recipient", Value: "0x775b87ef5D82ca211811C1a02CE0fE0CA3a455d7"},
						}},
					},
				},
			},
			true,
		},
		{
			"success, exceed block gas limit",
			&cmttypes.Block{Header: cmttypes.Header{Height: 1}, Data: cmttypes.Data{Txs: []cmttypes.Tx{txBz}}},
			[]*abci.ExecTxResult{
				{
					Code:   11,
					Log:    "out of gas in location: block gas meter; gasWanted: 21000",
					Events: []abci.Event{},
				},
			},
			true,
		},
		{
			"fail, failed eth tx",
			&cmttypes.Block{Header: cmttypes.Header{Height: 1}, Data: cmttypes.Data{Txs: []cmttypes.Tx{txBz}}},
			[]*abci.ExecTxResult{
				{
					Code:   15,
					Log:    "nonce mismatch",
					Events: []abci.Event{},
				},
			},
			false,
		},
		{
			"fail, invalid events",
			&cmttypes.Block{Header: cmttypes.Header{Height: 1}, Data: cmttypes.Data{Txs: []cmttypes.Tx{txBz}}},
			[]*abci.ExecTxResult{
				{
					Code:   0,
					Events: []abci.Event{},
				},
			},
			false,
		},
		{
			"fail, not eth tx",
			&cmttypes.Block{Header: cmttypes.Header{Height: 1}, Data: cmttypes.Data{Txs: []cmttypes.Tx{txBz2}}},
			[]*abci.ExecTxResult{
				{
					Code:   0,
					Events: []abci.Event{},
				},
			},
			false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			db := dbm.NewMemDB()
			idxer := indexer.NewKVIndexer(db, log.NewNopLogger(), clientCtx)

			err = idxer.IndexBlock(tc.block, tc.blockResult)
			require.NoError(t, err)
			if !tc.expSuccess {
				first, err := idxer.FirstIndexedBlock()
				require.NoError(t, err)
				require.Equal(t, int64(-1), first)

				last, err := idxer.LastIndexedBlock()
				require.NoError(t, err)
				require.Equal(t, int64(-1), last)
			} else {
				first, err := idxer.FirstIndexedBlock()
				require.NoError(t, err)
				require.Equal(t, tc.block.Header.Height, first)

				last, err := idxer.LastIndexedBlock()
				require.NoError(t, err)
				require.Equal(t, tc.block.Header.Height, last)

				res1, err := idxer.GetByTxHash(txHash)
				require.NoError(t, err)
				require.NotNil(t, res1)
				res2, err := idxer.GetByBlockAndIndex(1, 0)
				require.NoError(t, err)
				require.Equal(t, res1, res2)
			}
		})
	}
}

// TestKVIndexerDerivedTxs verifies that derived EVM txs (internal executions emitted
// only as events, with txType=DerivedTxType) are indexed by hash and block index just
// like standard MsgEthereumTx txs, and that they share a single eth-tx index sequence
// with standard txs in the same block.
func TestKVIndexerDerivedTxs(t *testing.T) {
	priv, err := ethsecp256k1.GenerateKey()
	require.NoError(t, err)
	from := common.BytesToAddress(priv.PubKey().Address().Bytes())
	signer := utiltx.NewSigner(priv)
	ethSigner := ethtypes.LatestSignerForChainID(nil)

	to := common.BigToAddress(big.NewInt(1))
	stdTx := types.NewTx(&types.EvmTxArgs{Nonce: 0, To: &to, Amount: big.NewInt(1000), GasLimit: 21000})
	stdTx.From = from.Hex()
	require.NoError(t, stdTx.Sign(ethSigner, signer))
	stdHash := stdTx.AsTransaction().Hash()

	nw := network.New()
	encodingConfig := nw.GetEncodingConfig()
	clientCtx := client.Context{}.WithTxConfig(encodingConfig.TxConfig).WithCodec(encodingConfig.Codec)

	// standard eth wrapper tx (recognized as eth via the ethereum extension option)
	stdWrapper, err := stdTx.BuildTx(clientCtx.TxConfig.NewTxBuilder(), constants.ExampleAttoDenom)
	require.NoError(t, err)
	stdBz, err := clientCtx.TxConfig.TxEncoder()(stdWrapper)
	require.NoError(t, err)

	// non-eth Cosmos tx wrapper (no eth extension) — the carrier for a derived tx
	builder := clientCtx.TxConfig.NewTxBuilder()
	require.NoError(t, builder.SetMsgs(stdTx))
	nonEthBz, err := clientCtx.TxConfig.TxEncoder()(builder.GetTx())
	require.NoError(t, err)

	derivedHash := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000deadbeef")

	gas := func(v int64) string { return strconv.FormatInt(v, 10) }
	idx := func(v int32) string { return strconv.FormatInt(int64(v), 10) }

	// derivedResult builds a successful tx result whose events describe one derived EVM
	// tx (ethereum_tx + tx_log + message{txType=DerivedTxType}) at the given eth txIndex.
	derivedResult := func(hash common.Hash, txIndex int32, gasUsed int64) *abci.ExecTxResult {
		return &abci.ExecTxResult{
			Code:    0,
			GasUsed: gasUsed,
			Events: []abci.Event{
				{Type: types.EventTypeEthereumTx, Attributes: []abci.EventAttribute{
					{Key: types.AttributeKeyEthereumTxHash, Value: hash.Hex()},
					{Key: types.AttributeKeyTxIndex, Value: idx(txIndex)},
					{Key: types.AttributeKeyTxGasUsed, Value: gas(gasUsed)},
					{Key: types.AttributeKeyRecipient, Value: to.Hex()},
				}},
				{Type: types.EventTypeTxLog, Attributes: []abci.EventAttribute{}},
				{Type: "message", Attributes: []abci.EventAttribute{
					{Key: "module", Value: "evm"},
					{Key: "sender", Value: from.Hex()},
					{Key: types.AttributeKeyTxType, Value: strconv.FormatUint(uint64(types.DerivedTxType), 10)},
				}},
			},
		}
	}

	// standardResult builds a successful tx result for a normal MsgEthereumTx. GasUsed is
	// set on the result because ParseTxResult overwrites a single non-derived tx's gas
	// with result.GasUsed (the derived path keeps the event-reported gas instead).
	standardResult := func(hash common.Hash, txIndex int32, gasUsed int64) *abci.ExecTxResult {
		return &abci.ExecTxResult{
			Code:    0,
			GasUsed: gasUsed,
			Events: []abci.Event{
				{Type: types.EventTypeEthereumTx, Attributes: []abci.EventAttribute{
					{Key: types.AttributeKeyEthereumTxHash, Value: hash.Hex()},
					{Key: types.AttributeKeyTxIndex, Value: idx(txIndex)},
					{Key: types.AttributeKeyTxGasUsed, Value: gas(gasUsed)},
					{Key: types.AttributeKeyRecipient, Value: to.Hex()},
				}},
			},
		}
	}

	t.Run("derived tx is indexed by hash and block index", func(t *testing.T) {
		db := dbm.NewMemDB()
		idxer := indexer.NewKVIndexer(db, log.NewNopLogger(), clientCtx)

		block := &cmttypes.Block{Header: cmttypes.Header{Height: 1}, Data: cmttypes.Data{Txs: []cmttypes.Tx{nonEthBz}}}
		require.NoError(t, idxer.IndexBlock(block, []*abci.ExecTxResult{derivedResult(derivedHash, 0, 50000)}))

		// Resolvable by hash — without indexing derived txs this lookup misses.
		res, err := idxer.GetByTxHash(derivedHash)
		require.NoError(t, err)
		require.NotNil(t, res)
		require.Equal(t, int32(0), res.EthTxIndex)
		require.Equal(t, uint64(50000), res.GasUsed)
		require.False(t, res.Failed)

		// ...and by block index, returning the same record.
		byIdx, err := idxer.GetByBlockAndIndex(1, 0)
		require.NoError(t, err)
		require.Equal(t, res, byIdx)

		// marked derived so the RPC backend rebuilds its additional fields from events
		isDerived, err := idxer.IsDerivedTx(derivedHash)
		require.NoError(t, err)
		require.True(t, isDerived)
	})

	t.Run("derived and standard txs share one eth-tx index sequence", func(t *testing.T) {
		db := dbm.NewMemDB()
		idxer := indexer.NewKVIndexer(db, log.NewNopLogger(), clientCtx)

		// Block order: derived tx (Cosmos tx 0) then standard tx (Cosmos tx 1). With #18
		// the keeper advances the eth txIndex for the derived tx, so the standard tx
		// emits txIndex=1. The indexer must mirror that by counting the derived tx — else
		// the standard tx is stored under index 0 and block-and-index lookups diverge.
		block := &cmttypes.Block{
			Header: cmttypes.Header{Height: 1},
			Data:   cmttypes.Data{Txs: []cmttypes.Tx{nonEthBz, stdBz}},
		}
		results := []*abci.ExecTxResult{
			derivedResult(derivedHash, 0, 50000),
			standardResult(stdHash, 1, 21000),
		}
		require.NoError(t, idxer.IndexBlock(block, results))

		// derived → eth index 0 (Cosmos tx 0), standard → eth index 1 (Cosmos tx 1)
		dByHash, err := idxer.GetByTxHash(derivedHash)
		require.NoError(t, err)
		require.Equal(t, int32(0), dByHash.EthTxIndex)
		require.Equal(t, uint32(0), dByHash.TxIndex)
		require.Equal(t, uint64(50000), dByHash.GasUsed)

		sByHash, err := idxer.GetByTxHash(stdHash)
		require.NoError(t, err)
		require.Equal(t, int32(1), sByHash.EthTxIndex)
		require.Equal(t, uint32(1), sByHash.TxIndex)
		require.Equal(t, uint64(21000), sByHash.GasUsed)

		// block-and-index lookups resolve to the same records (no collision/divergence)
		d0, err := idxer.GetByBlockAndIndex(1, 0)
		require.NoError(t, err)
		require.Equal(t, dByHash, d0)

		s1, err := idxer.GetByBlockAndIndex(1, 1)
		require.NoError(t, err)
		require.Equal(t, sByHash, s1)

		// only the derived tx carries the derived marker
		isDerived, err := idxer.IsDerivedTx(derivedHash)
		require.NoError(t, err)
		require.True(t, isDerived)
		isStdDerived, err := idxer.IsDerivedTx(stdHash)
		require.NoError(t, err)
		require.False(t, isStdDerived)
	})
}
