package backend

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	tmrpctypes "github.com/cometbft/cometbft/rpc/core/types"
	tmtypes "github.com/cometbft/cometbft/types"

	"github.com/cosmos/evm/rpc/backend/mocks"
	rpctypes "github.com/cosmos/evm/rpc/types"
	servertypes "github.com/cosmos/evm/server/types"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	sdkmath "cosmossdk.io/math"
)

// TestDerivedTxReceiptEffectiveGasPrice is a regression test for the negative block
// rewards reported by Blockscout for blocks whose only content is derived txs.
//
// Derived txs are reconstructed from events with zero fee caps, so the generic EIP-1559
// effective-price formula resolves them to 0. A receipt reporting gasUsed > 0 at
// effectiveGasPrice == 0 makes any consumer that models burn as base_fee * gas_used read
// the block as burning more than it collected. The receipt must report the base fee, and
// must agree with the `gasPrice` served by eth_getTransactionByHash for the same tx.
func TestDerivedTxReceiptEffectiveGasPrice(t *testing.T) {
	const (
		height   = int64(100)
		gasUsed  = uint64(98_212)
		gasLimit = uint64(50_000_000)
	)
	baseFee := big.NewInt(1_000_000_000) // 1 gwei, as on donut

	backend := setupMockBackend(t)
	mockEVMQueryClient := backend.QueryClient.QueryClient.(*mocks.EVMQueryClient)
	mockEVMQueryClient.On("BaseFee", mock.Anything, mock.Anything).
		Return(&evmtypes.QueryBaseFeeResponse{BaseFee: ptrInt(sdkmath.NewIntFromBigInt(baseFee))}, nil)

	limit := gasLimit
	additional := &rpctypes.TxResultAdditionalFields{
		Hash:      common.BigToHash(big.NewInt(0xdeadbeef)),
		Type:      evmtypes.DerivedTxType,
		Recipient: common.HexToAddress("0x7e5ac993907bc433046316948fa23b0c9c702664"),
		Sender:    common.HexToAddress("0x5826874ddef35d5f802634e212fbab949cb34f6a"),
		Value:     big.NewInt(0),
		GasUsed:   gasUsed,
		GasLimit:  &limit,
		Nonce:     1,
	}
	ethMsg := backend.parseDerivedTxFromAdditionalFields(additional)
	require.NotNil(t, ethMsg)

	backend.Indexer = &MockIndexer{
		txResults: map[common.Hash]*servertypes.TxResult{
			additional.Hash: {
				Height:     height,
				TxIndex:    0,
				EthTxIndex: 0,
				MsgIndex:   0,
				GasUsed:    gasUsed,
			},
		},
	}

	resBlock := &tmrpctypes.ResultBlock{
		BlockID: tmtypes.BlockID{Hash: common.BigToHash(big.NewInt(0xb10c)).Bytes()},
		Block:   &tmtypes.Block{Header: tmtypes.Header{Height: height}},
	}
	blockRes := &tmrpctypes.ResultBlockResults{
		Height:     height,
		TxsResults: []*abcitypes.ExecTxResult{{Code: 0}},
	}

	receipts, err := backend.ReceiptsFromCometBlock(
		resBlock,
		blockRes,
		[]*evmtypes.MsgEthereumTx{ethMsg},
		[]*rpctypes.TxResultAdditionalFields{additional},
	)
	require.NoError(t, err)
	require.Len(t, receipts, 1)

	require.Equal(t, baseFee, receipts[0].EffectiveGasPrice,
		"derived tx receipt must report the base fee, not 0")
	require.Equal(t, gasUsed, receipts[0].GasUsed)

	// The tx object served for the same derived tx must agree, so that consumers reading
	// either field compute the same (zero) net fee.
	rpcTx, err := rpctypes.NewRPCTransactionFromIncompleteMsg(
		ethMsg,
		common.BytesToHash(resBlock.BlockID.Hash),
		uint64(height),
		0,
		baseFee,
		backend.EvmChainID,
		additional.Hash,
	)
	require.NoError(t, err)
	require.Equal(t, receipts[0].EffectiveGasPrice, rpcTx.GasPrice.ToInt(),
		"eth_getTransactionByHash gasPrice must match the receipt effectiveGasPrice")
}

func ptrInt(i sdkmath.Int) *sdkmath.Int { return &i }
