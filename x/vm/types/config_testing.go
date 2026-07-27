//
// The config package provides a convenient way to modify x/evm params and values.
// Its primary purpose is to be used during application initialization.

//go:build test

package types

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/core/vm"
	geth "github.com/ethereum/go-ethereum/params"
)

// testChainConfig is the chain configuration used in the EVM to defined which
// opcodes are active based on Ethereum upgrades.
var testChainConfig *ChainConfig

// testChainConfigMu protects concurrent access to testChainConfig
var testChainConfigMu sync.RWMutex

// Configure applies the changes to the virtual machine configuration.
func (ec *EVMConfigurator) Configure() error {
	// If Configure method has been already used in the object, return
	// an error to avoid overriding configuration.
	if ec.sealed {
		return fmt.Errorf("error configuring EVMConfigurator: already sealed and cannot be modified")
	}

	if err := setTestingEVMCoinInfo(ec.evmCoinInfo); err != nil {
		return err
	}

	if err := extendDefaultExtraEIPs(ec.extendedDefaultExtraEIPs); err != nil {
		return err
	}

	if err := vm.ExtendActivators(ec.extendedEIPs); err != nil {
		return err
	}

	// After applying modifications, the configurator is sealed. This way, it is not possible
	// to call the configure method twice.
	ec.sealed = true

	return nil
}

func (ec *EVMConfigurator) ResetTestConfig() {
	vm.ResetActivators()
	resetEVMCoinInfo()
	testChainConfigMu.Lock()
	testChainConfig = nil
	testChainConfigMu.Unlock()
}

func setTestChainConfig(cc *ChainConfig) error {
	testChainConfigMu.Lock()
	defer testChainConfigMu.Unlock()

	if testChainConfig != nil {
		return errors.New("chainConfig already set. Cannot set again the chainConfig. Call the configurators ResetTestConfig method before configuring a new chain.")
	}
	config := DefaultChainConfig(0)
	if cc != nil {
		config = cc
	}
	if err := config.Validate(); err != nil {
		return err
	}
	testChainConfig = config
	return nil
}

// SetChainConfig allows to set the `chainConfig` variable modifying the
// default values. The method is private because it should only be called once
// in the EVMConfigurator.
func SetChainConfig(cc *ChainConfig) error {
	testChainConfigMu.Lock()
	defer testChainConfigMu.Unlock()

	if chainConfig != nil && chainConfig.ChainId != DefaultEVMChainID {
		return errors.New("chainConfig already set. Cannot set again the chainConfig")
	}
	config := DefaultChainConfig(0)
	if cc != nil {
		config = cc
	}
	if err := config.Validate(); err != nil {
		return err
	}
	testChainConfig = config

	return nil
}

// getTestChainConfig returns the configured test chain config, falling back to
// the default when it is momentarily nil.
//
// NOTE (push-chain): ResetTestConfig sets the global to nil between test cases,
// creating a brief window in which a leaked background goroutine — e.g. the
// legacypool reorg loop of a torn-down test network, which reaches
// GetEthChainConfig via TxEncoder.EVMTxToCosmosTx — can read it and panic on the
// nil dereference. This mirrors the same nil-guard applied to the testing coin
// info in denom_config_testing.go. Test-only (build tag test).
func getTestChainConfig() *ChainConfig {
	testChainConfigMu.RLock()
	defer testChainConfigMu.RUnlock()
	if testChainConfig == nil {
		return DefaultChainConfig(0)
	}
	return testChainConfig
}

// GetEthChainConfig returns the `chainConfig` used in the EVM (geth type).
func GetEthChainConfig() *geth.ChainConfig {
	return getTestChainConfig().EthereumConfig(nil)
}

// GetChainConfig returns the `chainConfig`.
func GetChainConfig() *ChainConfig {
	return getTestChainConfig()
}
