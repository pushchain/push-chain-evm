package erc20

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/mock"
	"go.uber.org/mock/gomock"

	"github.com/cosmos/evm/contracts"
	"github.com/cosmos/evm/testutil/integration/base/factory"
	"github.com/cosmos/evm/x/erc20/keeper"
	"github.com/cosmos/evm/x/erc20/types"
	erc20mocks "github.com/cosmos/evm/x/erc20/types/mocks"
	"github.com/cosmos/evm/x/vm/statedb"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
)

// TestConvertERC20 tests the ConvertERC20 msg server method,
// focusing on message validation and address parsing
func (s *KeeperTestSuite) TestConvertERC20() {
	testCases := []struct {
		name    string
		setup   func() *types.MsgConvertERC20
		expPass bool
	}{
		{
			"pass - valid message with proper addresses",
			func() *types.MsgConvertERC20 {
				contractAddr, err := s.setupRegisterERC20Pair(contractMinterBurner)
				s.Require().NoError(err)

				sender := s.keyring.GetAccAddr(0)
				senderHex := s.keyring.GetAddr(0)

				_, err = s.MintERC20Token(contractAddr, senderHex, big.NewInt(100))
				s.Require().NoError(err)

				return types.NewMsgConvertERC20(
					math.NewInt(10),
					sender,
					contractAddr,
					senderHex,
				)
			},
			true,
		},
		{
			"fail - invalid receiver bech32 address format",
			func() *types.MsgConvertERC20 {
				contractAddr, err := s.setupRegisterERC20Pair(contractMinterBurner)
				s.Require().NoError(err)

				sender := s.keyring.GetAccAddr(0)
				senderHex := s.keyring.GetAddr(0)

				_, err = s.MintERC20Token(contractAddr, senderHex, big.NewInt(100))
				s.Require().NoError(err)

				msg := types.NewMsgConvertERC20(
					math.NewInt(10),
					sender,
					contractAddr,
					senderHex,
				)
				// Create invalid bech32 address with valid length but invalid format
				// Using wrong prefix or invalid checksum
				msg.Receiver = "cosmos100000000000000000000000000000000"
				return msg
			},
			false,
		},
		{
			"fail - invalid sender hex address format",
			func() *types.MsgConvertERC20 {
				contractAddr, err := s.setupRegisterERC20Pair(contractMinterBurner)
				s.Require().NoError(err)

				sender := s.keyring.GetAccAddr(0)
				senderHex := s.keyring.GetAddr(0)

				_, err = s.MintERC20Token(contractAddr, senderHex, big.NewInt(100))
				s.Require().NoError(err)

				msg := types.NewMsgConvertERC20(
					math.NewInt(10),
					sender,
					contractAddr,
					senderHex,
				)
				// Create invalid hex address - not a valid hex string
				msg.Sender = "0xZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"
				return msg
			},
			false,
		},
		{
			"fail - invalid contract hex address format",
			func() *types.MsgConvertERC20 {
				sender := s.keyring.GetAccAddr(0)
				senderHex := s.keyring.GetAddr(0)

				msg := types.NewMsgConvertERC20(
					math.NewInt(10),
					sender,
					common.HexToAddress("0x0"),
					senderHex,
				)
				// Create invalid hex address - not a valid hex string
				msg.ContractAddress = "0xGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGG"
				return msg
			},
			false,
		},
	}

	for _, tc := range testCases {
		s.Run(tc.name, func() {
			s.mintFeeCollector = true
			defer func() { s.mintFeeCollector = false }()

			s.SetupTest()
			msg := tc.setup()

			if tc.expPass {
				_, err := s.network.App.GetErc20Keeper().ConvertERC20(s.network.GetContext(), msg)
				s.Require().NoError(err, tc.name)
			} else {
				_, err := s.network.App.GetErc20Keeper().ConvertERC20(s.network.GetContext(), msg)
				s.Require().Error(err, tc.name)
			}
		})
	}
}

// TestConvertERC20MaliciousApprovalKeepsEscrowInvariant asserts the escrow-and-mint invariant
// for the ERC20 -> Coin direction when the registered token hides an allowance grant inside its
// `transfer`. The module's balance check alone is satisfied by such a token - the escrow only
// drains later, when the third party spends the allowance - so the conversion has to be rejected
// on the unexpected `Approval` event. Asserting the error is not enough: the point of the fix is
// that no coins get minted against an escrow that can be emptied afterwards.
func (s *KeeperTestSuite) TestConvertERC20MaliciousApprovalKeepsEscrowInvariant() {
	s.mintFeeCollector = true
	defer func() {
		s.mintFeeCollector = false
	}()
	s.SetupTest()

	contractAddr, err := s.setupRegisterERC20Pair(contractMaliciousDelayed)
	s.Require().NoError(err)
	s.Require().NotEqual(common.Address{}, contractAddr)

	var (
		erc20ABI  = contracts.ERC20MinterBurnerDecimalsContract.ABI
		sender    = s.keyring.GetAccAddr(0)
		senderHex = s.keyring.GetAddr(0)
		amount    = math.NewInt(10)
		denom     = types.CreateDenom(contractAddr.String())
	)

	_, err = s.MintERC20Token(contractAddr, senderHex, amount.BigInt())
	s.Require().NoError(err)

	ctx := s.network.GetContext()
	erc20Keeper := s.network.App.GetErc20Keeper()
	bankKeeper := s.network.App.GetBankKeeper()

	escrowBefore := erc20Keeper.BalanceOf(ctx, erc20ABI, contractAddr, types.ModuleAddress)
	s.Require().NotNil(escrowBefore)
	senderTokensBefore := erc20Keeper.BalanceOf(ctx, erc20ABI, contractAddr, senderHex)
	s.Require().NotNil(senderTokensBefore)
	supplyBefore := bankKeeper.GetSupply(ctx, denom)
	coinsBefore := bankKeeper.GetBalance(ctx, sender, denom)
	s.Require().True(supplyBefore.IsZero(), "no coins should exist for the pair yet")

	convertMsg := types.NewMsgConvertERC20(amount, sender, contractAddr, senderHex)

	// Direct keeper call on a throwaway branch, to pin the rejection reason.
	cacheCtx, _ := ctx.CacheContext()
	_, err = erc20Keeper.ConvertERC20(cacheCtx, convertMsg)
	s.Require().Error(err)
	s.Require().ErrorContains(err, "unexpected Approval event")

	// Same message through a real tx, so the failed message's writes are rolled back
	// exactly as they would be on chain. The gas limit is set explicitly because the
	// factory would otherwise simulate the tx, and the simulation fails for the same reason.
	gasLimit := uint64(1_000_000)
	res, err := s.factory.CommitCosmosTx(s.keyring.GetPrivKey(0), factory.CosmosTxArgs{
		Gas:  &gasLimit,
		Msgs: []sdk.Msg{convertMsg},
	})
	s.Require().NoError(err)
	s.Require().NotEqual(uint32(0), res.Code, "expected the conversion tx to fail, got log: %s", res.Log)
	s.Require().Contains(res.Log, "unexpected Approval event")

	// Invariant: nothing escrowed, nothing minted.
	ctx = s.network.GetContext()

	escrowAfter := erc20Keeper.BalanceOf(ctx, erc20ABI, contractAddr, types.ModuleAddress)
	s.Require().NotNil(escrowAfter)
	s.Require().Zero(escrowBefore.Cmp(escrowAfter), "escrow moved: before %s, after %s", escrowBefore, escrowAfter)

	senderTokensAfter := erc20Keeper.BalanceOf(ctx, erc20ABI, contractAddr, senderHex)
	s.Require().NotNil(senderTokensAfter)
	s.Require().Zero(senderTokensBefore.Cmp(senderTokensAfter), "sender tokens moved: before %s, after %s", senderTokensBefore, senderTokensAfter)

	supplyAfter := bankKeeper.GetSupply(ctx, denom)
	s.Require().True(supplyAfter.IsZero(), "unbacked coins were minted: %s", supplyAfter)
	s.Require().Equal(supplyBefore, supplyAfter)
	s.Require().Equal(coinsBefore, bankKeeper.GetBalance(ctx, sender, denom))
}

// TestConvertCoinMaliciousApprovalKeepsEscrowInvariant covers the Coin -> ERC20 direction of the
// same guard. Here the hidden allowance is granted over the receiver rather than the module, so
// the unescrow must be rejected before any token leaves escrow and before the coins are burned.
//
// The whole exercise runs on a throwaway branch of the context: the malicious pair can never
// reach this state through committed txs, because the ERC20 -> Coin leg that would normally mint
// the coins is itself rejected by the same guard.
func (s *KeeperTestSuite) TestConvertCoinMaliciousApprovalKeepsEscrowInvariant() {
	s.mintFeeCollector = true
	defer func() {
		s.mintFeeCollector = false
	}()
	s.SetupTest()

	contractAddr, err := s.setupRegisterERC20Pair(contractMaliciousDelayed)
	s.Require().NoError(err)
	s.Require().NotEqual(common.Address{}, contractAddr)

	var (
		erc20ABI    = contracts.ERC20MinterBurnerDecimalsContract.ABI
		sender      = s.keyring.GetAccAddr(0)
		receiverHex = s.keyring.GetAddr(1)
		amount      = math.NewInt(10)
		denom       = types.CreateDenom(contractAddr.String())
	)

	// Fund the escrow directly: `mint` does not route through the malicious `transfer`.
	_, err = s.MintERC20Token(contractAddr, types.ModuleAddress, amount.BigInt())
	s.Require().NoError(err)

	erc20Keeper := s.network.App.GetErc20Keeper()
	bankKeeper := s.network.App.GetBankKeeper()

	ctx, _ := s.network.GetContext().CacheContext()

	// Give the sender the matching coins, as a successful ERC20 -> Coin leg would have.
	coins := sdk.Coins{sdk.Coin{Denom: denom, Amount: amount}}
	s.Require().NoError(bankKeeper.MintCoins(ctx, types.ModuleName, coins))
	s.Require().NoError(bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, sender, coins))

	escrowBefore := erc20Keeper.BalanceOf(ctx, erc20ABI, contractAddr, types.ModuleAddress)
	s.Require().NotNil(escrowBefore)
	s.Require().Zero(escrowBefore.Cmp(amount.BigInt()), "escrow should hold the minted tokens")
	receiverTokensBefore := erc20Keeper.BalanceOf(ctx, erc20ABI, contractAddr, receiverHex)
	s.Require().NotNil(receiverTokensBefore)
	supplyBefore := bankKeeper.GetSupply(ctx, denom)

	// baseapp runs every message on its own branch and drops it when the message errors;
	// mirror that here so the assertions below see what the chain would have kept.
	msgCtx, _ := ctx.CacheContext()
	convertMsg := types.NewMsgConvertCoin(coins[0], receiverHex, sender)
	_, err = erc20Keeper.ConvertCoin(msgCtx, convertMsg)
	s.Require().Error(err)
	s.Require().ErrorContains(err, "unexpected Approval event")

	// Invariant: escrow untouched, receiver got nothing, coins not burned.
	escrowAfter := erc20Keeper.BalanceOf(ctx, erc20ABI, contractAddr, types.ModuleAddress)
	s.Require().NotNil(escrowAfter)
	s.Require().Zero(escrowBefore.Cmp(escrowAfter), "escrow moved: before %s, after %s", escrowBefore, escrowAfter)

	receiverTokensAfter := erc20Keeper.BalanceOf(ctx, erc20ABI, contractAddr, receiverHex)
	s.Require().NotNil(receiverTokensAfter)
	s.Require().Zero(receiverTokensBefore.Cmp(receiverTokensAfter), "receiver tokens moved: before %s, after %s", receiverTokensBefore, receiverTokensAfter)

	s.Require().Equal(supplyBefore, bankKeeper.GetSupply(ctx, denom), "coins were burned without releasing tokens")
}

func (s *KeeperTestSuite) TestConvertNativeERC20ToEVMERC20() {
	var (
		contractAddr common.Address
		coinName     string
	)
	testCases := []struct {
		name           string
		mint           int64
		transfer       int64
		malleate       func(common.Address)
		extra          func()
		contractType   int
		expPass        bool
		selfdestructed bool
	}{
		{
			"ok - sufficient funds",
			100,
			10,
			func(common.Address) {},
			func() {},
			contractMinterBurner,
			true,
			false,
		},
		{
			"ok - equal funds",
			10,
			10,
			func(common.Address) {},
			func() {},
			contractMinterBurner,
			true,
			false,
		},
		{
			"fail - negative transfer of coins",
			10,
			-10,
			func(common.Address) {},
			func() {},
			contractMinterBurner,
			false,
			false,
		},
		{
			"fail - force evm fail",
			100,
			10,
			func(common.Address) {},
			func() {
				mockEVMKeeper := &erc20mocks.EVMKeeper{}
				transferKeeper := s.network.App.GetTransferKeeper()
				erc20Keeper := keeper.NewKeeper(
					s.network.App.GetKey("erc20"), s.network.App.AppCodec(),
					authtypes.NewModuleAddress(govtypes.ModuleName), s.network.App.GetAccountKeeper(),
					s.network.App.GetBankKeeper(), mockEVMKeeper, s.network.App.GetStakingKeeper(),
					&transferKeeper,
				)
				s.network.App.SetErc20Keeper(erc20Keeper)

				existingAcc := &statedb.Account{Nonce: uint64(1), Balance: uint256.NewInt(1)}
				balance := make([]uint8, 32)
				mockEVMKeeper.On("EstimateGasInternal", mock.Anything, mock.Anything, mock.Anything).Return(&evmtypes.EstimateGasResponse{Gas: uint64(200)}, nil)
				mockEVMKeeper.On("CallEVM", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
					mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(&evmtypes.MsgEthereumTxResponse{Ret: balance}, fmt.Errorf("forced ApplyMessage error")).Once()
				mockEVMKeeper.On("CallEVMWithData", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
					mock.Anything, mock.Anything, mock.Anything).Return(nil, fmt.Errorf("forced ApplyMessage error"))
				mockEVMKeeper.On("GetAccountWithoutBalance", mock.Anything, mock.Anything).Return(existingAcc, nil)
				mockEVMKeeper.On("IsContract", mock.Anything, mock.Anything).Return(true)
			},
			contractMinterBurner,
			false,
			false,
		},
		{
			"fail - force get balance fail",
			100,
			10,
			func(common.Address) {},
			func() {
				mockEVMKeeper := &erc20mocks.EVMKeeper{}
				transferKeeper := s.network.App.GetTransferKeeper()
				erc20Keeper := keeper.NewKeeper(
					s.network.App.GetKey("erc20"), s.network.App.AppCodec(),
					authtypes.NewModuleAddress(govtypes.ModuleName), s.network.App.GetAccountKeeper(),
					s.network.App.GetBankKeeper(), mockEVMKeeper, s.network.App.GetStakingKeeper(),
					&transferKeeper,
				)
				s.network.App.SetErc20Keeper(erc20Keeper)

				existingAcc := &statedb.Account{Nonce: uint64(1), Balance: uint256.NewInt(1)}
				balance := make([]uint8, 32)
				balance[31] = uint8(1)
				mockEVMKeeper.On("EstimateGasInternal", mock.Anything, mock.Anything, mock.Anything).Return(&evmtypes.EstimateGasResponse{Gas: uint64(200)}, nil)
				mockEVMKeeper.On("CallEVM", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
					mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(&evmtypes.MsgEthereumTxResponse{Ret: balance}, nil).Times(3)
				mockEVMKeeper.On("CallEVM", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
					mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(&evmtypes.MsgEthereumTxResponse{Ret: balance}, nil).Maybe()
				mockEVMKeeper.On("CallEVMWithData", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, fmt.Errorf("forced balance error"))
				mockEVMKeeper.On("GetAccountWithoutBalance", mock.Anything, mock.Anything).Return(existingAcc, nil)
				mockEVMKeeper.On("IsContract", mock.Anything, mock.Anything).Return(true)
			},
			contractMinterBurner,
			false,
			false,
		},
		{
			"fail - force transfer unpack fail",
			100,
			10,
			func(common.Address) {},
			func() {
				mockEVMKeeper := &erc20mocks.EVMKeeper{}
				transferKeeper := s.network.App.GetTransferKeeper()
				erc20Keeper := keeper.NewKeeper(
					s.network.App.GetKey("erc20"), s.network.App.AppCodec(),
					authtypes.NewModuleAddress(govtypes.ModuleName), s.network.App.GetAccountKeeper(),
					s.network.App.GetBankKeeper(), mockEVMKeeper, s.network.App.GetStakingKeeper(),
					&transferKeeper,
				)
				s.network.App.SetErc20Keeper(erc20Keeper)

				existingAcc := &statedb.Account{Nonce: uint64(1), Balance: uint256.NewInt(1)}
				balance := make([]uint8, 32)
				mockEVMKeeper.On("EstimateGasInternal", mock.Anything, mock.Anything, mock.Anything).Return(&evmtypes.EstimateGasResponse{Gas: uint64(200)}, nil)
				mockEVMKeeper.On("CallEVM", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
					mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(&evmtypes.MsgEthereumTxResponse{Ret: balance}, nil).Twice()
				mockEVMKeeper.On("CallEVMWithData", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(&evmtypes.MsgEthereumTxResponse{}, nil)
				mockEVMKeeper.On("GetAccountWithoutBalance", mock.Anything, mock.Anything).Return(existingAcc, nil)
				mockEVMKeeper.On("IsContract", mock.Anything, mock.Anything).Return(true)
			},
			contractMinterBurner,
			false,
			false,
		},

		{
			"fail - force invalid transfer fail",
			100,
			10,
			func(common.Address) {},
			func() {
				mockEVMKeeper := &erc20mocks.EVMKeeper{}
				transferKeeper := s.network.App.GetTransferKeeper()
				erc20Keeper := keeper.NewKeeper(
					s.network.App.GetKey("erc20"), s.network.App.AppCodec(),
					authtypes.NewModuleAddress(govtypes.ModuleName), s.network.App.GetAccountKeeper(),
					s.network.App.GetBankKeeper(), mockEVMKeeper, s.network.App.GetStakingKeeper(),
					&transferKeeper,
				)
				s.network.App.SetErc20Keeper(erc20Keeper)

				existingAcc := &statedb.Account{Nonce: uint64(1), Balance: uint256.NewInt(1)}
				balance := make([]uint8, 32)
				mockEVMKeeper.On("EstimateGasInternal", mock.Anything, mock.Anything, mock.Anything).Return(&evmtypes.EstimateGasResponse{Gas: uint64(200)}, nil)
				mockEVMKeeper.On("CallEVM", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
					mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(&evmtypes.MsgEthereumTxResponse{Ret: balance}, nil).Twice()
				mockEVMKeeper.On("CallEVMWithData", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
					mock.Anything, mock.Anything).Return(&evmtypes.MsgEthereumTxResponse{Ret: balance}, nil)
				mockEVMKeeper.On("GetAccountWithoutBalance", mock.Anything, mock.Anything).Return(existingAcc, nil)
				mockEVMKeeper.On("IsContract", mock.Anything, mock.Anything).Return(true)
			},
			contractMinterBurner,
			false,
			false,
		},
		{
			"fail - force send fail",
			100,
			10,
			func(common.Address) {},
			func() {
				ctrl := gomock.NewController(s.T())
				mockBankKeeper := erc20mocks.NewMockBankKeeper(ctrl)
				transferKeeper := s.network.App.GetTransferKeeper()
				erc20Keeper := keeper.NewKeeper(
					s.network.App.GetKey("erc20"), s.network.App.AppCodec(),
					authtypes.NewModuleAddress(govtypes.ModuleName), s.network.App.GetAccountKeeper(),
					mockBankKeeper, s.network.App.GetEVMKeeper(), s.network.App.GetStakingKeeper(),
					&transferKeeper,
				)
				s.network.App.SetErc20Keeper(erc20Keeper)

				mockBankKeeper.EXPECT().MintCoins(gomock.Any(), gomock.Any(), gomock.Any()).Return(fmt.Errorf("failed to mint")).AnyTimes()
				mockBankKeeper.EXPECT().SendCoinsFromAccountToModule(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(fmt.Errorf("failed to unescrow")).AnyTimes()
				mockBankKeeper.EXPECT().BlockedAddr(gomock.Any()).Return(false).AnyTimes()
				mockBankKeeper.EXPECT().GetBalance(gomock.Any(), gomock.Any(), gomock.Any()).Return(sdk.Coin{Denom: "coin", Amount: math.OneInt()}).AnyTimes()
				mockBankKeeper.EXPECT().IsSendEnabledCoin(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
			},
			contractMinterBurner,
			false,
			false,
		},
		{
			"fail - burn coins fail",
			100,
			10,
			func(common.Address) {},
			func() {
				ctrl := gomock.NewController(s.T())
				mockBankKeeper := erc20mocks.NewMockBankKeeper(ctrl)
				transferKeeper := s.network.App.GetTransferKeeper()
				erc20Keeper := keeper.NewKeeper(
					s.network.App.GetKey("erc20"), s.network.App.AppCodec(),
					authtypes.NewModuleAddress(govtypes.ModuleName), s.network.App.GetAccountKeeper(),
					mockBankKeeper, s.network.App.GetEVMKeeper(), s.network.App.GetStakingKeeper(),
					&transferKeeper,
				)
				s.network.App.SetErc20Keeper(erc20Keeper)

				mockBankKeeper.EXPECT().SendCoinsFromAccountToModule(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
				mockBankKeeper.EXPECT().BurnCoins(gomock.Any(), gomock.Any(), gomock.Any()).Return(fmt.Errorf("failed to burn")).AnyTimes()
				mockBankKeeper.EXPECT().BlockedAddr(gomock.Any()).Return(false)
				mockBankKeeper.EXPECT().IsSendEnabledCoin(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
			},
			contractMinterBurner,
			false,
			false,
		},
	}
	for _, tc := range testCases {
		s.Run(fmt.Sprintf("Case %s", tc.name), func() {
			var err error
			s.mintFeeCollector = true
			defer func() {
				s.mintFeeCollector = false
			}()
			s.SetupTest()

			contractAddr, err = s.setupRegisterERC20Pair(tc.contractType)
			s.Require().NoError(err)

			tc.malleate(contractAddr)
			s.Require().NotNil(contractAddr)
			// update context with latest committed changes
			sender := s.keyring.GetAccAddr(0)
			senderHex := s.keyring.GetAddr(0)

			// mint tokens to sender
			_, err = s.MintERC20Token(contractAddr, senderHex, big.NewInt(tc.mint))
			s.Require().NoError(err)

			// convert tokens to native first
			convertERC20Msg := types.NewMsgConvertERC20(
				math.NewInt(tc.mint),
				sender,
				contractAddr,
				senderHex,
			)
			_, err = s.factory.CommitCosmosTx(s.keyring.GetPrivKey(0), factory.CosmosTxArgs{Msgs: []sdk.Msg{convertERC20Msg}})
			s.Require().NoError(err)

			tc.extra()

			coinName = types.CreateDenom(contractAddr.String())

			evmTokenBalanceBefore, err := s.BalanceOf(contractAddr, senderHex) // actual: 100, expected: 0
			s.Require().NoError(err)
			s.Require().Equal(big.NewInt(0).Int64(), evmTokenBalanceBefore.(*big.Int).Int64())

			// then convert native tokens back into EVM tokens
			convertNativeMsg := types.NewMsgConvertCoin(sdk.Coin{Denom: coinName, Amount: math.NewInt(tc.transfer)}, senderHex, sender)

			if tc.expPass {
				_, err = s.factory.CommitCosmosTx(s.keyring.GetPrivKey(0), factory.CosmosTxArgs{Msgs: []sdk.Msg{convertNativeMsg}})
				s.Require().NoError(err, tc.name)
				cosmosBalance := s.network.App.GetBankKeeper().GetBalance(s.network.GetContext(), sender, coinName)
				evmTokenBalanceAfter, err := s.BalanceOf(contractAddr, senderHex)
				s.Require().NoError(err)

				acc := s.network.App.GetEVMKeeper().GetAccountWithoutBalance(s.network.GetContext(), contractAddr)
				if tc.selfdestructed {
					s.Require().Nil(acc, "expected contract to be destroyed")
				} else {
					s.Require().NotNil(acc)
				}

				isContract := s.network.App.GetEVMKeeper().IsContract(s.network.GetContext(), contractAddr)
				if tc.selfdestructed || !isContract {
					id := s.network.App.GetErc20Keeper().GetTokenPairID(s.network.GetContext(), contractAddr.String())
					_, found := s.network.App.GetErc20Keeper().GetTokenPair(s.network.GetContext(), id)
					s.Require().False(found)
				} else {
					s.Require().Equal(cosmosBalance.Amount, math.NewInt(tc.mint-tc.transfer))
					s.Require().Equal(evmTokenBalanceAfter.(*big.Int).Int64(), math.NewInt(tc.transfer).Int64())
				}
			} else {
				_, err = s.network.App.GetErc20Keeper().ConvertCoin(s.network.GetContext(), convertNativeMsg)
				s.Require().Error(err, tc.name)
			}
		})
	}
	s.mintFeeCollector = false
}

func (s *KeeperTestSuite) TestUpdateParams() {
	testCases := []struct {
		name      string
		request   *types.MsgUpdateParams
		expectErr bool
	}{
		{
			name:      "fail - invalid authority",
			request:   &types.MsgUpdateParams{Authority: "foobar"},
			expectErr: true,
		},
		{
			name: "pass - valid Update msg",
			request: &types.MsgUpdateParams{
				Authority: authtypes.NewModuleAddress(govtypes.ModuleName).String(),
				Params:    types.DefaultParams(),
			},
			expectErr: false,
		},
	}

	for _, tc := range testCases {
		s.Run("MsgUpdateParams", func() {
			s.SetupTest()
			_, err := s.network.App.GetErc20Keeper().UpdateParams(s.network.GetContext(), tc.request)
			if tc.expectErr {
				s.Require().Error(err)
			} else {
				s.Require().NoError(err)
			}
		})
	}
}
