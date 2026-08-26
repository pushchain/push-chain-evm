package vm

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"github.com/cosmos/evm/contracts"
	testconstants "github.com/cosmos/evm/testutil/constants"
	utiltx "github.com/cosmos/evm/testutil/tx"
	"github.com/cosmos/evm/x/erc20/types"
	"github.com/cosmos/evm/x/vm/statedb"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	storetypes "cosmossdk.io/store/types"
)

func (s *KeeperTestSuite) TestCallEVM() {
	wcosmosEVMContract := common.HexToAddress(testconstants.WEVMOSContractMainnet)
	testCases := []struct {
		name     string
		method   string
		stateDB  *statedb.StateDB
		expPass  bool
		expError string
	}{
		{
			"unknown method",
			"",
			nil,
			false,
			"",
		},
		{
			"pass",
			"balanceOf",
			nil,
			true,
			"",
		},
		{
			"fail with nil statedb",
			"balanceOf",
			nil,
			false,
			"stateDB cannot be nil",
		},
	}
	for _, tc := range testCases {
		s.SetupTest() // reset

		erc20 := contracts.ERC20MinterBurnerDecimalsContract.ABI
		account := utiltx.GenerateAddress()

		var stateDB *statedb.StateDB
		if tc.stateDB == nil && tc.name != "fail with nil statedb" {
			stateDB = statedb.New(s.Network.GetContext(), s.Network.App.GetEVMKeeper(), statedb.NewEmptyTxConfig())
		} else {
			stateDB = tc.stateDB
		}

		res, err := s.Network.App.GetEVMKeeper().CallEVM(s.Network.GetContext(), stateDB, erc20, types.ModuleAddress, wcosmosEVMContract, false, false, nil, tc.method, account)
		if tc.expPass {
			s.Require().IsTypef(&evmtypes.MsgEthereumTxResponse{}, res, tc.name)
			s.Require().NoError(err)
		} else {
			s.Require().Error(err)
			if tc.expError != "" {
				s.Require().Contains(err.Error(), tc.expError)
			}
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
		useNilDB bool
		expPass  bool
		expError string
	}{
		{
			name: "pass with unknown method",
			from: types.ModuleAddress,
			malleate: func() []byte {
				account := utiltx.GenerateAddress()
				data, _ := erc20.Pack("", account)
				return data
			},
			deploy:   false,
			useNilDB: false,
			expPass:  true,
			expError: "",
		},
		{
			name: "pass",
			from: types.ModuleAddress,
			malleate: func() []byte {
				account := utiltx.GenerateAddress()
				data, _ := erc20.Pack("balanceOf", account)
				return data
			},
			deploy:   false,
			useNilDB: false,
			expPass:  true,
			expError: "",
		},
		{
			name: "pass with empty data",
			from: types.ModuleAddress,
			malleate: func() []byte {
				return []byte{}
			},
			deploy:   false,
			useNilDB: false,
			expPass:  true,
			expError: "",
		},
		{
			name: "fail empty sender",
			from: common.Address{},
			malleate: func() []byte {
				return []byte{}
			},
			deploy:   false,
			useNilDB: false,
			expPass:  false,
			expError: "",
		},
		{
			name: "fail with nil statedb",
			from: types.ModuleAddress,
			malleate: func() []byte {
				account := utiltx.GenerateAddress()
				data, _ := erc20.Pack("balanceOf", account)
				return data
			},
			deploy:   false,
			useNilDB: true,
			expPass:  false,
			expError: "stateDB cannot be nil",
		},
		{
			name: "deploy",
			from: types.ModuleAddress,
			malleate: func() []byte {
				ctorArgs, _ := contracts.ERC20MinterBurnerDecimalsContract.ABI.Pack("", "test", "test", uint8(18))
				data := append(contracts.ERC20MinterBurnerDecimalsContract.Bin, ctorArgs...) //nolint:gocritic
				return data
			},
			deploy:   true,
			useNilDB: false,
			expPass:  true,
			expError: "",
		},
		{
			name: "fail deploy",
			from: types.ModuleAddress,
			malleate: func() []byte {
				params := s.Network.App.GetEVMKeeper().GetParams(s.Network.GetContext())
				params.AccessControl.Create = evmtypes.AccessControlType{
					AccessType: evmtypes.AccessTypeRestricted,
				}
				_ = s.Network.App.GetEVMKeeper().SetParams(s.Network.GetContext(), params)
				ctorArgs, _ := contracts.ERC20MinterBurnerDecimalsContract.ABI.Pack("", "test", "test", uint8(18))
				data := append(contracts.ERC20MinterBurnerDecimalsContract.Bin, ctorArgs...) //nolint:gocritic
				return data
			},
			deploy:   true,
			useNilDB: false,
			expPass:  false,
			expError: "",
		},
		{
			name: "fail deploy with nil statedb",
			from: types.ModuleAddress,
			malleate: func() []byte {
				ctorArgs, _ := contracts.ERC20MinterBurnerDecimalsContract.ABI.Pack("", "test", "test", uint8(18))
				data := append(contracts.ERC20MinterBurnerDecimalsContract.Bin, ctorArgs...) //nolint:gocritic
				return data
			},
			deploy:   true,
			useNilDB: true,
			expPass:  false,
			expError: "stateDB cannot be nil",
		},
	}

	for _, tc := range testCases {
		s.Run(fmt.Sprintf("Case %s", tc.name), func() {
			s.SetupTest() // reset

			data := tc.malleate()
			var res *evmtypes.MsgEthereumTxResponse
			var err error

			var stateDB *statedb.StateDB
			if !tc.useNilDB {
				stateDB = statedb.New(s.Network.GetContext(), s.Network.App.GetEVMKeeper(), statedb.NewEmptyTxConfig())
			}

			if tc.deploy {
				res, err = s.Network.App.GetEVMKeeper().CallEVMWithData(s.Network.GetContext(), stateDB, tc.from, nil, data, true, false, nil)
			} else {
				res, err = s.Network.App.GetEVMKeeper().CallEVMWithData(s.Network.GetContext(), stateDB, tc.from, &wcosmosEVMContract, data, false, false, nil)
			}

			if tc.expPass {
				s.Require().IsTypef(&evmtypes.MsgEthereumTxResponse{}, res, tc.name)
				s.Require().NoError(err)
			} else {
				s.Require().Error(err)
				if tc.expError != "" {
					s.Require().Contains(err.Error(), tc.expError)
				}
			}
		})
	}
}

// TestCallEVMWithDataFailedCallChargesParentGas is the CallEVMWithData half of
// F-2026-18824. Like DerivedEVMCallWithData, this function returned on
// res.Failed() before reaching ctx.GasMeter().ConsumeGas, so a reverting call
// performed real EVM work for free at the Cosmos meter. Unlike the derived path
// it needs no clamp: msg.GasLimit here is config.DefaultGasCap, which a
// caller-supplied gasCap can only narrow (F-2026-18818), so res.GasUsed is
// already bounded and the caller cannot inflate it.
func (s *KeeperTestSuite) TestCallEVMWithDataFailedCallChargesParentGas() {
	s.SetupTest()

	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext().WithGasMeter(storetypes.NewGasMeter(parentGasMeterLimit))
	keeper := s.Network.App.GetEVMKeeper()

	burner := s.deployRawCode(ctx, gasBurnerRevertCode)
	// v0.6.0: the caller supplies the StateDB. Build it after deployRawCode so it
	// sees the burner's code.
	stateDB := statedb.New(ctx, keeper, statedb.NewEmptyTxConfig())
	gasBefore := ctx.GasMeter().GasConsumed()

	res, err := keeper.CallEVMWithData(ctx, stateDB, from, &burner, nil, true, false, nil)
	s.Require().Error(err, "the burner reverts, so the call must surface an error")
	s.Require().NotNil(res)
	s.Require().True(res.Failed())
	s.Require().Greater(res.GasUsed, uint64(2_000_000))

	charged := ctx.GasMeter().GasConsumed() - gasBefore
	s.Require().GreaterOrEqual(charged, res.GasUsed,
		"a failed CallEVMWithData must charge its EVM gas to the parent Cosmos meter (F-2026-18824)")
	s.Require().LessOrEqual(charged, res.GasUsed+1_000_000,
		"res.GasUsed must be charged once, not doubled")
}

// TestCallEVMWithDataSuccessChargesGasUsed pins the CallEVMWithData happy path:
// res.GasUsed is charged exactly once, unchanged by the failure-path fix.
func (s *KeeperTestSuite) TestCallEVMWithDataSuccessChargesGasUsed() {
	s.SetupTest()

	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext().WithGasMeter(storetypes.NewGasMeter(parentGasMeterLimit))
	keeper := s.Network.App.GetEVMKeeper()

	burner := s.deployRawCode(ctx, gasBurnerSuccessCode)
	// v0.6.0: the caller supplies the StateDB. Build it after deployRawCode so it
	// sees the burner's code.
	stateDB := statedb.New(ctx, keeper, statedb.NewEmptyTxConfig())
	gasBefore := ctx.GasMeter().GasConsumed()

	res, err := keeper.CallEVMWithData(ctx, stateDB, from, &burner, nil, true, false, nil)
	s.Require().NoError(err)
	s.Require().False(res.Failed())
	s.Require().Greater(res.GasUsed, uint64(2_000_000))

	charged := ctx.GasMeter().GasConsumed() - gasBefore
	s.Require().GreaterOrEqual(charged, res.GasUsed)
	s.Require().LessOrEqual(charged, res.GasUsed+1_000_000,
		"res.GasUsed must be charged exactly once on the success path")
}
