package indexer_test

import (
	"math/big"
	"os"
	"strconv"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cmttypes "github.com/cometbft/cometbft/types"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/evm/crypto/ethsecp256k1"
	"github.com/cosmos/evm/encoding"
	"github.com/cosmos/evm/indexer"
	"github.com/cosmos/evm/testutil/constants"
	utiltx "github.com/cosmos/evm/testutil/tx"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	"cosmossdk.io/log/v2"

	"github.com/cosmos/cosmos-sdk/client"
)

func TestMain(m *testing.M) {
	cfg := evmtypes.NewEVMConfigurator()
	cfg.ResetTestConfig()
	ethCfg := evmtypes.DefaultChainConfig(constants.ExampleChainID.EVMChainID)
	if err := evmtypes.SetChainConfig(ethCfg); err != nil {
		panic(err)
	}
	coinInfo := constants.ExampleChainCoinInfo[constants.ExampleChainID]
	if err := evmtypes.NewEVMConfigurator().WithEVMCoinInfo(coinInfo).Configure(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// TestKVIndexerDerivedTxs is a regression test for F-2026-17740 (KV indexer path missed
// derived transactions) and F-2026-17745 (derived txs shared a constant txIndex). It indexes
// a block containing a derived EVM tx — an internal execution recorded only as Tendermint
// events with no embedded MsgEthereumTx — and asserts the tx is resolvable by hash and block
// index, carries the derived marker, and shares ONE eth-tx index sequence with a standard
// MsgEthereumTx in the same block. Self-contained: uses encoding.MakeConfig, no test network.
func TestKVIndexerDerivedTxs(t *testing.T) {
	priv, err := ethsecp256k1.GenerateKey()
	require.NoError(t, err)
	from := common.BytesToAddress(priv.PubKey().Address().Bytes())
	signer := utiltx.NewSigner(priv)
	ethSigner := ethtypes.LatestSignerForChainID(nil)

	to := common.BigToAddress(big.NewInt(1))
	stdTx := evmtypes.NewTx(&evmtypes.EvmTxArgs{Nonce: 0, To: &to, Amount: big.NewInt(1000), GasLimit: 21000})
	stdTx.From = from.Bytes()
	require.NoError(t, stdTx.Sign(ethSigner, signer))
	stdHash := stdTx.AsTransaction().Hash()

	encodingConfig := encoding.MakeConfig(constants.ExampleChainID.EVMChainID)
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

	// derivedResult builds a successful tx result whose events describe one derived EVM tx
	// (ethereum_tx + tx_log + message{txType=DerivedTxType}) at the given eth txIndex.
	derivedResult := func(hash common.Hash, txIndex int32, gasUsed int64) *abci.ExecTxResult {
		return &abci.ExecTxResult{
			Code:    0,
			GasUsed: gasUsed,
			Events: []abci.Event{
				{Type: evmtypes.EventTypeEthereumTx, Attributes: []abci.EventAttribute{
					{Key: evmtypes.AttributeKeyEthereumTxHash, Value: hash.Hex()},
					{Key: evmtypes.AttributeKeyTxIndex, Value: idx(txIndex)},
					{Key: evmtypes.AttributeKeyTxGasUsed, Value: gas(gasUsed)},
					{Key: evmtypes.AttributeKeyRecipient, Value: to.Hex()},
				}},
				{Type: evmtypes.EventTypeTxLog, Attributes: []abci.EventAttribute{}},
				{Type: "message", Attributes: []abci.EventAttribute{
					{Key: "module", Value: "evm"},
					{Key: "sender", Value: from.Hex()},
					{Key: evmtypes.AttributeKeyTxType, Value: strconv.FormatUint(uint64(evmtypes.DerivedTxType), 10)},
				}},
			},
		}
	}

	// standardResult builds a successful tx result for a normal MsgEthereumTx.
	standardResult := func(hash common.Hash, txIndex int32, gasUsed int64) *abci.ExecTxResult {
		return &abci.ExecTxResult{
			Code:    0,
			GasUsed: gasUsed,
			Events: []abci.Event{
				{Type: evmtypes.EventTypeEthereumTx, Attributes: []abci.EventAttribute{
					{Key: evmtypes.AttributeKeyEthereumTxHash, Value: hash.Hex()},
					{Key: evmtypes.AttributeKeyTxIndex, Value: idx(txIndex)},
					{Key: evmtypes.AttributeKeyTxGasUsed, Value: gas(gasUsed)},
					{Key: evmtypes.AttributeKeyRecipient, Value: to.Hex()},
				}},
			},
		}
	}

	t.Run("derived tx is indexed by hash and block index (F-2026-17740)", func(t *testing.T) {
		idxer := indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), clientCtx)

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

		// marked derived so the RPC backend rebuilds its additional fields from events.
		isDerived, err := idxer.IsDerivedTx(derivedHash)
		require.NoError(t, err)
		require.True(t, isDerived)
	})

	t.Run("derived and standard txs share one eth-tx index sequence (F-2026-17745)", func(t *testing.T) {
		idxer := indexer.NewKVIndexer(dbm.NewMemDB(), log.NewNopLogger(), clientCtx)

		// Block order: derived tx (Cosmos tx 0) then standard tx (Cosmos tx 1). The keeper
		// advances the eth txIndex for the derived tx, so the standard tx emits txIndex=1.
		// The indexer must mirror that by counting the derived tx — else the standard tx is
		// stored under index 0 and block-and-index lookups diverge.
		block := &cmttypes.Block{
			Header: cmttypes.Header{Height: 1},
			Data:   cmttypes.Data{Txs: []cmttypes.Tx{nonEthBz, stdBz}},
		}
		results := []*abci.ExecTxResult{
			derivedResult(derivedHash, 0, 50000),
			standardResult(stdHash, 1, 21000),
		}
		require.NoError(t, idxer.IndexBlock(block, results))

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

		// block-and-index lookups resolve to the same records (no collision/divergence).
		d0, err := idxer.GetByBlockAndIndex(1, 0)
		require.NoError(t, err)
		require.Equal(t, dByHash, d0)

		s1, err := idxer.GetByBlockAndIndex(1, 1)
		require.NoError(t, err)
		require.Equal(t, sByHash, s1)

		// only the derived tx carries the derived marker.
		isDerived, err := idxer.IsDerivedTx(derivedHash)
		require.NoError(t, err)
		require.True(t, isDerived)
		isStdDerived, err := idxer.IsDerivedTx(stdHash)
		require.NoError(t, err)
		require.False(t, isStdDerived)
	})
}
