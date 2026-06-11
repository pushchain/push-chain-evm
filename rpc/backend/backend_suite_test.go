package backend

import (
	"bufio"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/suite"

	cmtrpctypes "github.com/cometbft/cometbft/rpc/core/types"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/evm/crypto/hd"
	"github.com/cosmos/evm/encoding"
	"github.com/cosmos/evm/indexer"
	"github.com/cosmos/evm/rpc/backend/mocks"
	rpctypes "github.com/cosmos/evm/rpc/types"
	"github.com/cosmos/evm/testutil/constants"
	testnetwork "github.com/cosmos/evm/testutil/integration/os/network"
	utiltx "github.com/cosmos/evm/testutil/tx"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	"github.com/cosmos/cosmos-sdk/client"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/server"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
)

type BackendTestSuite struct {
	suite.Suite

	backend *Backend
	from    common.Address
	acc     sdk.AccAddress
	signer  keyring.Signer
}

func TestBackendTestSuite(t *testing.T) {
	suite.Run(t, new(BackendTestSuite))
}

var ChainID = constants.ExampleChainID

// SetupTest is executed before every BackendTestSuite test
func (suite *BackendTestSuite) SetupTest() {
	ctx := server.NewDefaultContext()
	ctx.Viper.Set("telemetry.global-labels", []interface{}{})

	baseDir := suite.T().TempDir()
	nodeDirName := "node"
	clientDir := filepath.Join(baseDir, nodeDirName, "evmoscli")
	keyRing, err := suite.generateTestKeyring(clientDir)
	if err != nil {
		panic(err)
	}

	// Create Account with set sequence
	suite.acc = sdk.AccAddress(utiltx.GenerateAddress().Bytes())
	accounts := map[string]client.TestAccount{}
	accounts[suite.acc.String()] = client.TestAccount{
		Address: suite.acc,
		Num:     uint64(1),
		Seq:     uint64(1),
	}

	from, priv := utiltx.NewAddrKey()
	suite.from = from
	suite.signer = utiltx.NewSigner(priv)
	suite.Require().NoError(err)

	nw := testnetwork.New()
	encodingConfig := nw.GetEncodingConfig()
	clientCtx := client.Context{}.WithChainID(ChainID).
		WithHeight(1).
		WithTxConfig(encodingConfig.TxConfig).
		WithKeyringDir(clientDir).
		WithKeyring(keyRing).
		WithAccountRetriever(client.TestAccountRetriever{Accounts: accounts}).
		WithClient(mocks.NewClient(suite.T()))

	allowUnprotectedTxs := false
	idxer := indexer.NewKVIndexer(dbm.NewMemDB(), ctx.Logger, clientCtx)

	suite.backend = NewBackend(ctx, ctx.Logger, clientCtx, allowUnprotectedTxs, idxer)
	suite.backend.cfg.JSONRPC.GasCap = 0
	suite.backend.cfg.JSONRPC.EVMTimeout = 0
	suite.backend.cfg.JSONRPC.AllowInsecureUnlock = true
	suite.backend.queryClient.QueryClient = mocks.NewEVMQueryClient(suite.T())
	suite.backend.queryClient.FeeMarket = mocks.NewFeeMarketQueryClient(suite.T())
	suite.backend.ctx = rpctypes.ContextWithHeight(1)

	// Add codec
	suite.backend.clientCtx.Codec = encodingConfig.Codec
}

// buildEthereumTx returns an example legacy Ethereum transaction
func (suite *BackendTestSuite) buildEthereumTx() (*evmtypes.MsgEthereumTx, []byte) {
	ethTxParams := evmtypes.EvmTxArgs{
		ChainID:  suite.backend.chainID,
		Nonce:    uint64(0),
		To:       &common.Address{},
		Amount:   big.NewInt(0),
		GasLimit: 100000,
		GasPrice: big.NewInt(1),
	}
	msgEthereumTx := evmtypes.NewTx(&ethTxParams)

	// A valid msg should have empty `From`
	msgEthereumTx.From = suite.from.Hex()

	txBuilder := suite.backend.clientCtx.TxConfig.NewTxBuilder()
	err := txBuilder.SetMsgs(msgEthereumTx)
	suite.Require().NoError(err)

	bz, err := suite.backend.clientCtx.TxConfig.TxEncoder()(txBuilder.GetTx())
	suite.Require().NoError(err)
	return msgEthereumTx, bz
}

// buildFormattedBlock returns a formatted block for testing
func (suite *BackendTestSuite) buildFormattedBlock(
	blockRes *cmtrpctypes.ResultBlockResults,
	resBlock *cmtrpctypes.ResultBlock,
	fullTx bool,
	tx *evmtypes.MsgEthereumTx,
	validator sdk.AccAddress,
	baseFee *big.Int,
) map[string]interface{} {
	header := resBlock.Block.Header
	gasLimit := int64(^uint32(0))                                             // for `MaxGas = -1` (DefaultConsensusParams)
	gasUsed := new(big.Int).SetUint64(uint64(blockRes.TxsResults[0].GasUsed)) //nolint:gosec // G115 // won't exceed uint64

	root := common.Hash{}.Bytes()
	receipt := ethtypes.NewReceipt(root, false, gasUsed.Uint64())
	bloom := ethtypes.CreateBloom(ethtypes.Receipts{receipt})

	ethRPCTxs := []interface{}{}
	if tx != nil {
		if fullTx {
			rpcTx, err := rpctypes.NewRPCTransaction(
				tx.AsTransaction(),
				common.BytesToHash(header.Hash()),
				uint64(header.Height), //nolint:gosec // G115 // won't exceed uint64
				uint64(0),
				baseFee,
				suite.backend.chainID,
			)
			suite.Require().NoError(err)
			ethRPCTxs = []interface{}{rpcTx}
		} else {
			ethRPCTxs = []interface{}{common.HexToHash(tx.Hash)}
		}
	}

	return rpctypes.FormatBlock(
		header,
		resBlock.Block.Size(),
		gasLimit,
		gasUsed,
		ethRPCTxs,
		bloom,
		common.BytesToAddress(validator.Bytes()),
		baseFee,
	)
}

func (suite *BackendTestSuite) generateTestKeyring(clientDir string) (keyring.Keyring, error) {
	buf := bufio.NewReader(os.Stdin)
	encCfg := encoding.MakeConfig()
	return keyring.New(sdk.KeyringServiceName(), keyring.BackendTest, clientDir, buf, encCfg.Codec, []keyring.Option{hd.EthSecp256k1Option()}...)
}

// buildAndEncodeMultiMsgEthTx builds a single EVM Cosmos tx containing multiple
// MsgEthereumTx messages. Each message is signed with a fresh ephemeral key (so
// their hashes are unique even when underlying tx params are identical) and From is
// cleared to "" as BuildTx does. Callers should compute hashes via
// msg.AsTransaction().Hash() after this call returns.
func (suite *BackendTestSuite) buildAndEncodeMultiMsgEthTx(msgs ...*evmtypes.MsgEthereumTx) []byte {
	ethSigner := ethtypes.LatestSigner(suite.backend.ChainConfig())
	for _, msg := range msgs {
		from, priv := utiltx.NewAddrKey()
		signer := utiltx.NewSigner(priv)
		msg.From = from.String()
		suite.Require().NoError(msg.Sign(ethSigner, signer))
		msg.From = ""
	}

	extBuilder, ok := suite.backend.clientCtx.TxConfig.NewTxBuilder().(authtx.ExtensionOptionsTxBuilder)
	suite.Require().True(ok)

	option, err := codectypes.NewAnyWithValue(&evmtypes.ExtensionOptionsEthereumTx{})
	suite.Require().NoError(err)
	extBuilder.SetExtensionOptions(option)

	sdkMsgs := make([]sdk.Msg, len(msgs))
	for i, msg := range msgs {
		sdkMsgs[i] = msg
	}
	suite.Require().NoError(extBuilder.SetMsgs(sdkMsgs...))

	bz, err := suite.backend.clientCtx.TxConfig.TxEncoder()(extBuilder.GetTx())
	suite.Require().NoError(err)
	return bz
}

func (suite *BackendTestSuite) signAndEncodeEthTx(msgEthereumTx *evmtypes.MsgEthereumTx) []byte {
	from, priv := utiltx.NewAddrKey()
	signer := utiltx.NewSigner(priv)

	ethSigner := ethtypes.LatestSigner(suite.backend.ChainConfig())
	msgEthereumTx.From = from.String()
	err := msgEthereumTx.Sign(ethSigner, signer)
	suite.Require().NoError(err)

	evmDenom := evmtypes.GetEVMCoinDenom()
	tx, err := msgEthereumTx.BuildTx(suite.backend.clientCtx.TxConfig.NewTxBuilder(), evmDenom)
	suite.Require().NoError(err)

	txEncoder := suite.backend.clientCtx.TxConfig.TxEncoder()
	txBz, err := txEncoder(tx)
	suite.Require().NoError(err)

	return txBz
}
