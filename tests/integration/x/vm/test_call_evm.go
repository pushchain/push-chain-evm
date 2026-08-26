package vm

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"github.com/cosmos/evm/contracts"
	testconstants "github.com/cosmos/evm/testutil/constants"
	utiltx "github.com/cosmos/evm/testutil/tx"
	"github.com/cosmos/evm/x/erc20/types"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	storetypes "cosmossdk.io/store/types"
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

// TestCallEVMWithDataFailedCallChargesParentGas is the CallEVMWithData half of
// F-2026-18824. Like DerivedEVMCallWithData, this function returned on
// res.Failed() before reaching ctx.GasMeter().ConsumeGas, so a reverting call
// performed real EVM work for free at the Cosmos meter. Unlike the derived path
// it needs no clamp: msg.GasLimit here is the hardcoded config.DefaultGasCap, so
// res.GasUsed is already bounded and the caller cannot inflate it.
func (s *KeeperTestSuite) TestCallEVMWithDataFailedCallChargesParentGas() {
	s.SetupTest()

	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext().WithGasMeter(storetypes.NewGasMeter(parentGasMeterLimit))
	keeper := s.Network.App.GetEVMKeeper()

	burner := s.deployRawCode(ctx, gasBurnerRevertCode)
	gasBefore := ctx.GasMeter().GasConsumed()

	res, err := keeper.CallEVMWithData(ctx, from, &burner, nil, true, nil)
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
	gasBefore := ctx.GasMeter().GasConsumed()

	res, err := keeper.CallEVMWithData(ctx, from, &burner, nil, true, nil)
	s.Require().NoError(err)
	s.Require().False(res.Failed())
	s.Require().Greater(res.GasUsed, uint64(2_000_000))

	charged := ctx.GasMeter().GasConsumed() - gasBefore
	s.Require().GreaterOrEqual(charged, res.GasUsed)
	s.Require().LessOrEqual(charged, res.GasUsed+1_000_000,
		"res.GasUsed must be charged exactly once on the success path")
}
