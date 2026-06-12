package vm

import (
	"fmt"
	"math/big"
	"strconv"

	"github.com/ethereum/go-ethereum/common"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/evm/contracts"
	testconstants "github.com/cosmos/evm/testutil/constants"
	utiltx "github.com/cosmos/evm/testutil/tx"
	"github.com/cosmos/evm/x/erc20/types"
	"github.com/cosmos/evm/x/vm/keeper/testdata"
	evmtypes "github.com/cosmos/evm/x/vm/types"
)

func (s *KeeperTestSuite) TestCallEVM() {
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
		s.SetupTest() // reset

		erc20 := contracts.ERC20MinterBurnerDecimalsContract.ABI
		account := utiltx.GenerateAddress()
		res, err := s.Network.App.GetEVMKeeper().CallEVM(s.Network.GetContext(), erc20, types.ModuleAddress, wcosmosEVMContract, false, nil, tc.method, account)
		if tc.expPass {
			s.Require().IsTypef(&evmtypes.MsgEthereumTxResponse{}, res, tc.name)
			s.Require().NoError(err)
		} else {
			s.Require().Error(err)
		}
	}
}

func (s *KeeperTestSuite) TestCallEVMWithData() {
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
				params := s.Network.App.GetEVMKeeper().GetParams(s.Network.GetContext())
				params.AccessControl.Create = evmtypes.AccessControlType{
					AccessType: evmtypes.AccessTypeRestricted,
				}
				_ = s.Network.App.GetEVMKeeper().SetParams(s.Network.GetContext(), params)
				ctorArgs, _ := contracts.ERC20MinterBurnerDecimalsContract.ABI.Pack("", "test", "test", uint8(18))
				data := append(contracts.ERC20MinterBurnerDecimalsContract.Bin, ctorArgs...) //nolint:gocritic
				return data
			},
			true,
			false,
		},
	}

	for _, tc := range testCases {
		s.Run(fmt.Sprintf("Case %s", tc.name), func() {
			s.SetupTest() // reset

			data := tc.malleate()
			var res *evmtypes.MsgEthereumTxResponse
			var err error

			if tc.deploy {
				res, err = s.Network.App.GetEVMKeeper().CallEVMWithData(s.Network.GetContext(), tc.from, nil, data, true, nil)
			} else {
				res, err = s.Network.App.GetEVMKeeper().CallEVMWithData(s.Network.GetContext(), tc.from, &wcosmosEVMContract, data, false, nil)
			}

			if tc.expPass {
				s.Require().IsTypef(&evmtypes.MsgEthereumTxResponse{}, res, tc.name)
				s.Require().NoError(err)
			} else {
				s.Require().Error(err)
			}
		})
	}
}

// derivedTransfer issues a single ERC20 transfer through DerivedEVMCall (commit=true)
// on the given ctx and returns any execution error (non-nil when the call reverts).
func (s *KeeperTestSuite) derivedTransfer(ctx sdk.Context, from, contract, recipient common.Address) error {
	erc20Contract, err := testdata.LoadERC20Contract()
	s.Require().NoError(err)
	_, err = s.Network.App.GetEVMKeeper().DerivedEVMCall(
		ctx,
		erc20Contract.ABI,
		from,
		contract,
		big.NewInt(0),      // value
		big.NewInt(200000), // explicit gasLimit so a reverting call still reaches execution
		true,               // commit
		false,              // gasless
		false,              // isModuleSender
		nil,                // manualNonce
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

// txLogAttrCount returns the total number of attributes across all tx_log events.
func txLogAttrCount(events []sdk.Event) int {
	n := 0
	for _, e := range events {
		if e.Type == evmtypes.EventTypeTxLog {
			n += len(e.Attributes)
		}
	}
	return n
}

// TestDerivedEVMCallCommitFlag is a regression test for F-2026-17736: a derived
// call with commit=false must not persist any state changes, while commit=true must
// persist them.
func (s *KeeperTestSuite) TestDerivedEVMCallCommitFlag() {
	testCases := []struct {
		name      string
		commit    bool
		expChange bool
	}{
		{"commit=false must NOT persist state", false, false},
		{"commit=true must persist state", true, true},
	}

	for _, tc := range testCases {
		s.Run(tc.name, func() {
			s.SetupTest()

			erc20Contract, err := testdata.LoadERC20Contract()
			s.Require().NoError(err)
			erc20ABI := erc20Contract.ABI

			owner := s.Keyring.GetAddr(0)
			recipient := utiltx.GenerateAddress()
			amount := big.NewInt(1000)

			contractAddr := s.DeployTestContract(s.T(), s.Network.GetContext(), owner, big.NewInt(1_000_000))

			// balanceOf reads through CallEVM (read-only path independent of the function
			// under test) so the assertion reflects committed store state.
			balanceOf := func(addr common.Address) *big.Int {
				res, err := s.Network.App.GetEVMKeeper().CallEVM(
					s.Network.GetContext(), erc20ABI, types.ModuleAddress, contractAddr, false, nil, "balanceOf", addr,
				)
				s.Require().NoError(err)
				out, err := erc20ABI.Unpack("balanceOf", res.Ret)
				s.Require().NoError(err)
				return out[0].(*big.Int)
			}

			s.Require().Equal(int64(0), balanceOf(recipient).Int64(), "recipient must start at zero balance")

			_, err = s.Network.App.GetEVMKeeper().DerivedEVMCall(
				s.Network.GetContext(),
				erc20ABI,
				owner,
				contractAddr,
				big.NewInt(0),
				nil,
				tc.commit,
				false,
				false,
				nil,
				"transfer",
				recipient, amount,
			)
			s.Require().NoError(err)

			got := balanceOf(recipient)
			if tc.expChange {
				s.Require().Equal(amount.Int64(), got.Int64(), "commit=true: transfer must be persisted")
			} else {
				s.Require().Equal(int64(0), got.Int64(), "commit=false: state must NOT be persisted")
			}
		})
	}
}

// TestDerivedEVMCallEthTxLogEventsStayPaired is a regression test for F-2026-17738.
// Every derived ethereum_tx event must be paired with exactly one tx_log event —
// even on failure, where the tx_log is empty. The JSON-RPC log builder matches logs
// to txs positionally, so a missing tx_log on a failed derived tx would desync logs
// across the other derived txs in the same block.
func (s *KeeperTestSuite) TestDerivedEVMCallEthTxLogEventsStayPaired() {
	s.SetupTest()

	owner := s.Keyring.GetAddr(0)
	broke := s.Keyring.GetAddr(1)
	recipient := utiltx.GenerateAddress()

	contractAddr := s.DeployTestContract(s.T(), s.Network.GetContext(), owner, big.NewInt(1_000_000))
	// Fresh event manager so only the calls below are counted (not the deploy).
	ctx := s.Network.GetContext().WithEventManager(sdk.NewEventManager())

	// Interleave success / failure / success so a dropped tx_log on the middle
	// (failed) call would leave the counts unequal.
	s.Require().NoError(s.derivedTransfer(ctx, owner, contractAddr, recipient))
	s.Require().Error(s.derivedTransfer(ctx, broke, contractAddr, recipient))
	s.Require().NoError(s.derivedTransfer(ctx, owner, contractAddr, recipient))

	ethTx, txLog := countEthTxAndLogEvents(ctx.EventManager().Events())
	s.Require().Equal(3, ethTx, "each derived call must emit exactly one ethereum_tx event")
	s.Require().Equal(ethTx, txLog,
		"every ethereum_tx must be paired with a tx_log event (empty on failure) to preserve positional log alignment")
}

// TestDerivedEVMCallFailedExecutionNoBloomSideEffect is a regression test for
// F-2026-17738: a reverted derived execution must not contribute to the block bloom
// or log size, while still emitting the ethereum_tx + (empty) tx_log pair.
func (s *KeeperTestSuite) TestDerivedEVMCallFailedExecutionNoBloomSideEffect() {
	s.SetupTest()

	owner := s.Keyring.GetAddr(0)
	broke := s.Keyring.GetAddr(1)
	recipient := utiltx.GenerateAddress()

	contractAddr := s.DeployTestContract(s.T(), s.Network.GetContext(), owner, big.NewInt(1_000_000))
	ctx := s.Network.GetContext().WithEventManager(sdk.NewEventManager())

	bloomBefore := new(big.Int).Set(s.Network.App.GetEVMKeeper().GetBlockBloomTransient(ctx))
	logSizeBefore := s.Network.App.GetEVMKeeper().GetLogSizeTransient(ctx)

	s.Require().Error(s.derivedTransfer(ctx, broke, contractAddr, recipient))

	s.Require().Equal(0, bloomBefore.Cmp(s.Network.App.GetEVMKeeper().GetBlockBloomTransient(ctx)),
		"failed derived tx must not mutate the block bloom")
	s.Require().Equal(logSizeBefore, s.Network.App.GetEVMKeeper().GetLogSizeTransient(ctx),
		"failed derived tx must not mutate the log size")

	ethTx, txLog := countEthTxAndLogEvents(ctx.EventManager().Events())
	s.Require().Equal(1, ethTx, "failed derived tx still emits its ethereum_tx receipt")
	s.Require().Equal(1, txLog, "failed derived tx still emits an (empty) tx_log to preserve alignment")

	s.Require().Equal(0, txLogAttrCount(ctx.EventManager().Events()),
		"a reverted derived tx must not emit any log attributes")

	// Sanity: a successful transfer DOES produce a non-empty tx_log.
	okCtx := s.Network.GetContext().WithEventManager(sdk.NewEventManager())
	s.Require().NoError(s.derivedTransfer(okCtx, owner, contractAddr, recipient))
	s.Require().Positive(txLogAttrCount(okCtx.EventManager().Events()),
		"a successful derived tx must emit its logs")
}

// TestDerivedEVMCallAssignsUniqueTxIndex is a regression test for F-2026-17745:
// each derived tx in a block must get a unique, monotonically increasing eth tx
// index (not the legacy constant 9999 shared by all derived txs).
func (s *KeeperTestSuite) TestDerivedEVMCallAssignsUniqueTxIndex() {
	s.SetupTest()

	owner := s.Keyring.GetAddr(0)
	recipient := utiltx.GenerateAddress()

	contractAddr := s.DeployTestContract(s.T(), s.Network.GetContext(), owner, big.NewInt(1_000_000))
	ctx := s.Network.GetContext().WithEventManager(sdk.NewEventManager())

	const n = 3
	for i := 0; i < n; i++ {
		s.Require().NoError(s.derivedTransfer(ctx, owner, contractAddr, recipient))
	}

	// Collect the txIndex attribute emitted on each ethereum_tx event.
	var indices []uint64
	for _, e := range ctx.EventManager().Events() {
		if e.Type != evmtypes.EventTypeEthereumTx {
			continue
		}
		for _, a := range e.Attributes {
			if a.Key == evmtypes.AttributeKeyTxIndex {
				v, err := strconv.ParseUint(a.Value, 10, 64)
				s.Require().NoError(err)
				indices = append(indices, v)
			}
		}
	}

	s.Require().Len(indices, n, "one txIndex per derived tx")
	s.Require().NotEqual(uint64(9999), indices[0], "must not use the legacy constant DerivedTxIndex")
	for i := 1; i < len(indices); i++ {
		s.Require().Equal(indices[0]+uint64(i), indices[i],
			"derived txs must get unique, monotonically increasing indices")
	}
}
