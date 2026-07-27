//
// The config package provides a convenient way to modify x/evm params and values.
// Its primary purpose is to be used during application initialization.

//go:build test

package types

import (
	"errors"
	"fmt"
	"sync"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// testingEvmCoinInfo hold the information of the coin used in the EVM as gas token. It
// can only be set via `EVMConfigurator` before starting the app.
var testingEvmCoinInfo *EvmCoinInfo

// testingEvmCoinInfoMu protects concurrent access to testingEvmCoinInfo
var testingEvmCoinInfoMu sync.RWMutex

// SetDefaultEvmCoinInfo sets the default EVM coin info to be used as fallback.
// This should be called during keeper initialization.
func SetDefaultEvmCoinInfo(coinInfo EvmCoinInfo) {
	testingEvmCoinInfoMu.Lock()
	defer testingEvmCoinInfoMu.Unlock()
	testingEvmCoinInfo = &coinInfo
}

// setEVMCoinDecimals allows to define the decimals used in the representation
// of the EVM coin.
func setEVMCoinDecimals(d Decimals) error {
	if err := d.Validate(); err != nil {
		return fmt.Errorf("setting EVM coin decimals: %w", err)
	}

	testingEvmCoinInfo.Decimals = d.Uint32()
	return nil
}

// setEVMCoinDenom allows to define the denom of the coin used in the EVM.
func setEVMCoinDenom(denom string) error {
	if err := sdk.ValidateDenom(denom); err != nil {
		return err
	}
	testingEvmCoinInfo.Denom = denom
	return nil
}

// setEVMCoinExtendedDenom allows to define the extended denom of the coin used in the EVM.
func setEVMCoinExtendedDenom(extendedDenom string) error {
	if err := sdk.ValidateDenom(extendedDenom); err != nil {
		return err
	}
	testingEvmCoinInfo.ExtendedDenom = extendedDenom
	return nil
}

func setDisplayDenom(displayDenom string) error {
	if err := sdk.ValidateDenom(displayDenom); err != nil {
		return fmt.Errorf("setting EVM coin display denom: %w", err)
	}
	testingEvmCoinInfo.DisplayDenom = displayDenom
	return nil
}

// GetCoinInfo returns EvmCoinInfo if set, otherwise panics.
func GetCoinInfo() EvmCoinInfo {
	testingEvmCoinInfoMu.RLock()
	defer testingEvmCoinInfoMu.RUnlock()
	if testingEvmCoinInfo == nil {
		panic("global testingEvmCoinInfo is not set yet!")
	}
	return *testingEvmCoinInfo
}

// getTestingEvmCoinInfo returns a copy of the configured testing coin info,
// falling back to a zero value when it is momentarily nil.
//
// NOTE (push-chain): the configurator resets the global to nil (resetEVMCoinInfo)
// before re-setting it between test cases, creating a brief window in which a
// leaked background goroutine — e.g. the legacypool reorg loop of a torn-down
// test network — can read it and panic. This mirrors the production
// getEvmCoinInfo() nil-guard in denom_config.go (added for the same class of
// "coin info accessed before initialization" issue). Test-only (build tag test).
// The read lock is upstream v0.7.0's race fix; the nil fallback is push-chain's.
func getTestingEvmCoinInfo() EvmCoinInfo {
	testingEvmCoinInfoMu.RLock()
	defer testingEvmCoinInfoMu.RUnlock()
	if testingEvmCoinInfo == nil {
		return EvmCoinInfo{}
	}
	return *testingEvmCoinInfo
}

// GetEVMCoinDecimals returns the decimals used in the representation of the EVM
// coin.
func GetEVMCoinDecimals() Decimals {
	return Decimals(getTestingEvmCoinInfo().Decimals)
}

// GetEVMCoinDenom returns the denom used for the EVM coin.
func GetEVMCoinDenom() string {
	return getTestingEvmCoinInfo().Denom
}

// GetEVMCoinExtendedDenom returns the extended denom used for the EVM coin.
func GetEVMCoinExtendedDenom() string {
	return getTestingEvmCoinInfo().ExtendedDenom
}

// GetEVMCoinDisplayDenom returns the display denom used for the EVM coin.
func GetEVMCoinDisplayDenom() string {
	return getTestingEvmCoinInfo().DisplayDenom
}

// setTestingEVMCoinInfo allows to define denom and decimals of the coin used in the EVM.
func setTestingEVMCoinInfo(eci EvmCoinInfo) error {
	testingEvmCoinInfoMu.Lock()
	defer testingEvmCoinInfoMu.Unlock()

	if testingEvmCoinInfo != nil {
		return errors.New("testing EVM coin info already set. Make sure you run the configurator's ResetTestConfig before trying to set a new evm coin info")
	}

	if eci.Decimals == EighteenDecimals.Uint32() {
		if eci.Denom != eci.ExtendedDenom {
			return errors.New("EVM coin denom and extended denom must be the same for 18 decimals")
		}
	}

	testingEvmCoinInfo = new(EvmCoinInfo)

	if err := setEVMCoinDenom(eci.Denom); err != nil {
		return err
	}
	if err := setEVMCoinExtendedDenom(eci.ExtendedDenom); err != nil {
		return err
	}
	if err := setDisplayDenom(eci.DisplayDenom); err != nil {
		return err
	}
	return setEVMCoinDecimals(Decimals(eci.Decimals))
}

// resetEVMCoinInfo resets to nil the testingEVMCoinInfo
func resetEVMCoinInfo() {
	testingEvmCoinInfoMu.Lock()
	defer testingEvmCoinInfoMu.Unlock()
	testingEvmCoinInfo = nil
}
