package keeper_test

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/evm/contracts"
	testconstants "github.com/cosmos/evm/testutil/constants"
	utiltx "github.com/cosmos/evm/testutil/tx"
	"github.com/cosmos/evm/x/erc20/types"
	"github.com/cosmos/evm/x/vm/keeper/testdata"
	evmtypes "github.com/cosmos/evm/x/vm/types"
)

func (suite *KeeperTestSuite) TestCallEVM() {
	wcosmosEVMContract := common.HexToAddress(testconstants.WEVMOSContractMainnet)
	testCases := []struct {
		name    string
		method  string
		expPass bool
	}{
		{
			"unknown method",
			"",
			false,
		},
		{
			"pass",
			"balanceOf",
			true,
		},
	}
	for _, tc := range testCases {
		suite.SetupTest() // reset

		erc20 := contracts.ERC20MinterBurnerDecimalsContract.ABI
		account := utiltx.GenerateAddress()
		res, err := suite.network.App.EVMKeeper.CallEVM(suite.network.GetContext(), erc20, types.ModuleAddress, wcosmosEVMContract, false, tc.method, account)
		if tc.expPass {
			suite.Require().IsTypef(&evmtypes.MsgEthereumTxResponse{}, res, tc.name)
			suite.Require().NoError(err)
		} else {
			suite.Require().Error(err)
		}
	}
}

func (suite *KeeperTestSuite) TestCallEVMWithData() {
	erc20 := contracts.ERC20MinterBurnerDecimalsContract.ABI
	wcosmosEVMContract := common.HexToAddress(testconstants.WEVMOSContractMainnet)
	testCases := []struct {
		name     string
		from     common.Address
		malleate func() []byte
		deploy   bool
		expPass  bool
	}{
		{
			"pass with unknown method",
			types.ModuleAddress,
			func() []byte {
				account := utiltx.GenerateAddress()
				data, _ := erc20.Pack("", account)
				return data
			},
			false,
			true,
		},
		{
			"pass",
			types.ModuleAddress,
			func() []byte {
				account := utiltx.GenerateAddress()
				data, _ := erc20.Pack("balanceOf", account)
				return data
			},
			false,
			true,
		},
		{
			"pass with empty data",
			types.ModuleAddress,
			func() []byte {
				return []byte{}
			},
			false,
			true,
		},

		{
			"fail empty sender",
			common.Address{},
			func() []byte {
				return []byte{}
			},
			false,
			false,
		},
		{
			"deploy",
			types.ModuleAddress,
			func() []byte {
				ctorArgs, _ := contracts.ERC20MinterBurnerDecimalsContract.ABI.Pack("", "test", "test", uint8(18))
				data := append(contracts.ERC20MinterBurnerDecimalsContract.Bin, ctorArgs...) //nolint:gocritic
				return data
			},
			true,
			true,
		},
		{
			"fail deploy",
			types.ModuleAddress,
			func() []byte {
				params := suite.network.App.EVMKeeper.GetParams(suite.network.GetContext())
				params.AccessControl.Create = evmtypes.AccessControlType{
					AccessType: evmtypes.AccessTypeRestricted,
				}
				_ = suite.network.App.EVMKeeper.SetParams(suite.network.GetContext(), params)
				ctorArgs, _ := contracts.ERC20MinterBurnerDecimalsContract.ABI.Pack("", "test", "test", uint8(18))
				data := append(contracts.ERC20MinterBurnerDecimalsContract.Bin, ctorArgs...) //nolint:gocritic
				return data
			},
			true,
			false,
		},
	}

	for _, tc := range testCases {
		suite.Run(fmt.Sprintf("Case %s", tc.name), func() {
			suite.SetupTest() // reset

			data := tc.malleate()
			var res *evmtypes.MsgEthereumTxResponse
			var err error

			if tc.deploy {
				res, err = suite.network.App.EVMKeeper.CallEVMWithData(suite.network.GetContext(), tc.from, nil, data, true)
			} else {
				res, err = suite.network.App.EVMKeeper.CallEVMWithData(suite.network.GetContext(), tc.from, &wcosmosEVMContract, data, false)
			}

			if tc.expPass {
				suite.Require().IsTypef(&evmtypes.MsgEthereumTxResponse{}, res, tc.name)
				suite.Require().NoError(err)
			} else {
				suite.Require().Error(err)
			}
		})
	}
}

// derivedTransfer issues a single ERC20 transfer through DerivedEVMCall (commit=true)
// on the shared ctx and returns the resulting error (non-nil when the call reverts).
func (suite *KeeperTestSuite) derivedTransfer(ctx sdk.Context, from, contract, recipient common.Address) error {
	erc20Contract, err := testdata.LoadERC20Contract()
	suite.Require().NoError(err)
	_, err = suite.network.App.EVMKeeper.DerivedEVMCall(
		ctx,
		erc20Contract.ABI,
		from,
		contract,
		big.NewInt(0),      // value
		big.NewInt(200000), // gasLimit (explicit so a reverting call still reaches
		//                     execution + event emission instead of failing in gas estimation)
		true, // commit
		false,         // gasless
		false,         // isModuleSender
		nil,           // manualNonce
		"transfer",
		recipient, big.NewInt(100),
	)
	return err
}

// countEthTxAndLogEvents returns how many ethereum_tx and tx_log events are present.
func countEthTxAndLogEvents(events []sdk.Event) (ethTx, txLog int) {
	for _, e := range events {
		switch e.Type {
		case evmtypes.EventTypeEthereumTx:
			ethTx++
		case evmtypes.EventTypeTxLog:
			txLog++
		}
	}
	return ethTx, txLog
}

// TestDerivedEVMCallEthTxLogEventsStayPaired is a regression test for F-2026-17738.
// Every derived ethereum_tx event must be paired with exactly one tx_log event —
// even on failure, where the tx_log is empty. The JSON-RPC log builder matches logs
// to txs positionally, so a missing tx_log on a failed derived tx would desync logs
// across the other derived txs in the same block.
func (suite *KeeperTestSuite) TestDerivedEVMCallEthTxLogEventsStayPaired() {
	suite.SetupTest()

	owner := suite.keyring.GetAddr(0) // holds the supply
	broke := suite.keyring.GetAddr(1) // holds 0 tokens -> transfer reverts
	recipient := utiltx.GenerateAddress()

	contractAddr := suite.DeployTestContract(suite.T(), suite.network.GetContext(), owner, big.NewInt(1_000_000))
	// Fresh event manager so only the calls below are counted (not the deploy).
	ctx := suite.network.GetContext().WithEventManager(sdk.NewEventManager())

	// Interleave success / failure / success so a dropped tx_log on the middle
	// (failed) call would leave the counts unequal.
	suite.Require().NoError(suite.derivedTransfer(ctx, owner, contractAddr, recipient))
	suite.Require().Error(suite.derivedTransfer(ctx, broke, contractAddr, recipient))
	suite.Require().NoError(suite.derivedTransfer(ctx, owner, contractAddr, recipient))

	ethTx, txLog := countEthTxAndLogEvents(ctx.EventManager().Events())
	suite.Require().Equal(3, ethTx, "each derived call must emit exactly one ethereum_tx event")
	suite.Require().Equal(ethTx, txLog,
		"every ethereum_tx must be paired with a tx_log event (empty on failure) to preserve positional log alignment")
}

// TestDerivedEVMCallFailedExecutionNoBloomSideEffect is a regression test for
// F-2026-17738: a reverted derived execution must not contribute to the block bloom
// or log size, while still emitting the ethereum_tx + (empty) tx_log pair.
func (suite *KeeperTestSuite) TestDerivedEVMCallFailedExecutionNoBloomSideEffect() {
	suite.SetupTest()

	owner := suite.keyring.GetAddr(0)
	broke := suite.keyring.GetAddr(1) // 0 tokens -> transfer reverts
	recipient := utiltx.GenerateAddress()

	contractAddr := suite.DeployTestContract(suite.T(), suite.network.GetContext(), owner, big.NewInt(1_000_000))
	ctx := suite.network.GetContext().WithEventManager(sdk.NewEventManager())

	bloomBefore := new(big.Int).Set(suite.network.App.EVMKeeper.GetBlockBloomTransient(ctx))
	logSizeBefore := suite.network.App.EVMKeeper.GetLogSizeTransient(ctx)

	// reverting transfer (broke has no tokens)
	suite.Require().Error(suite.derivedTransfer(ctx, broke, contractAddr, recipient))

	suite.Require().Equal(0, bloomBefore.Cmp(suite.network.App.EVMKeeper.GetBlockBloomTransient(ctx)),
		"failed derived tx must not mutate the block bloom")
	suite.Require().Equal(logSizeBefore, suite.network.App.EVMKeeper.GetLogSizeTransient(ctx),
		"failed derived tx must not mutate the log size")

	ethTx, txLog := countEthTxAndLogEvents(ctx.EventManager().Events())
	suite.Require().Equal(1, ethTx, "failed derived tx still emits its ethereum_tx receipt")
	suite.Require().Equal(1, txLog, "failed derived tx still emits an (empty) tx_log to preserve alignment")

	// The tx_log emitted on failure MUST carry no log attributes — otherwise the fix
	// would publish phantom logs for state that was never committed.
	suite.Require().Equal(0, txLogAttrCount(ctx.EventManager().Events()),
		"a reverted derived tx must not emit any log attributes")

	// Sanity: a successful transfer DOES produce a non-empty tx_log (ERC20 Transfer
	// event), so the empty-on-failure result above is not trivially always-empty.
	okCtx := suite.network.GetContext().WithEventManager(sdk.NewEventManager())
	suite.Require().NoError(suite.derivedTransfer(okCtx, owner, contractAddr, recipient))
	suite.Require().Positive(txLogAttrCount(okCtx.EventManager().Events()),
		"a successful derived tx must emit its logs")
}

// txLogAttrCount returns the total number of tx_log attributes across all tx_log events.
func txLogAttrCount(events []sdk.Event) int {
	n := 0
	for _, e := range events {
		if e.Type == evmtypes.EventTypeTxLog {
			n += len(e.Attributes)
		}
	}
	return n
}
