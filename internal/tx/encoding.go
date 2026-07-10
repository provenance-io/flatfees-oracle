package tx

import (
	"fmt"

	txsigning "cosmossdk.io/x/tx/signing"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/address"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/std"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/gogoproto/proto"

	flatfeestypes "github.com/provenance-io/provenance/x/flatfees/types"
)

const (
	prefixMainNet = "pb"
	prefixTestNet = "tp"
)

// SetSDKConfig sets the global bech32 prefixes for the given account prefix.
// When seal is true, it also seals the cosmos-sdk config to prevent further
// mutation — any subsequent call to any Set* on the config panics.
//
// Invariant: at most one caller per process should pass seal=true, and it
// should be the last touch of the SDK config for the run's lifetime.
// Production calls it once from main.go (seal=true). Tests call it with
// seal=false so multiple test files can share the same config without
// tripping the seal.
func SetSDKConfig(hrp string, seal bool) {
	c := sdk.GetConfig()
	c.SetBech32PrefixForAccount(hrp, hrp+"pub")
	c.SetBech32PrefixForValidator(hrp+"valoper", hrp+"valoperpub")
	c.SetBech32PrefixForConsensusNode(hrp+"valcons", hrp+"valconspub")
	if seal {
		c.Seal()
	}
}

// SetChainConfig configures Provenance bech32 prefixes and coin type; must run before any address parsing.
// When seal is true, it also seals the cosmos-sdk config to prevent further
// mutation — any subsequent call to any Set* on the config panics.
//
// Invariant: at most one caller per process should pass seal=true, and it
// should be the last touch of the SDK config for the run's lifetime.
// Production calls it once from main.go (seal=true). Tests call it with
// seal=false so multiple test files can share the same config without
// tripping the seal.
func SetChainConfig(testnet bool, seal bool) {
	hrp := prefixMainNet
	if testnet {
		hrp = prefixTestNet
	}
	SetSDKConfig(hrp, seal)
}

// SetChainConfigFromAddress derives the hrp from an account address and sets the sdk config accordingly.
// When seal is true, it also seals the cosmos-sdk config to prevent further
// mutation — any subsequent call to any Set* on the config panics.
//
// Invariant: at most one caller per process should pass seal=true, and it
// should be the last touch of the SDK config for the run's lifetime.
// Production calls it once from main.go (seal=true). Tests call it with
// seal=false so multiple test files can share the same config without
// tripping the seal.
func SetChainConfigFromAddress(addr string, seal bool) error {
	hrp, _, err := bech32.DecodeAndConvert(addr)
	if err != nil {
		return fmt.Errorf("decode oracle address %q: %w", addr, err)
	}
	SetSDKConfig(hrp, seal)
	return nil
}

// NewEncoding creates codec and TxConfig for client message and account types; requires SetChainConfig first.
func NewEncoding() (codec.Codec, client.TxConfig, error) {
	cfg := sdk.GetConfig()
	signingOptions := txsigning.Options{
		AddressCodec:          address.Bech32Codec{Bech32Prefix: cfg.GetBech32AccountAddrPrefix()},
		ValidatorAddressCodec: address.Bech32Codec{Bech32Prefix: cfg.GetBech32ValidatorAddrPrefix()},
	}
	registry, err := codectypes.NewInterfaceRegistryWithOptions(codectypes.InterfaceRegistryOptions{
		ProtoFiles:     proto.HybridResolver,
		SigningOptions: signingOptions,
	})
	if err != nil {
		return nil, nil, err
	}
	std.RegisterInterfaces(registry)
	cryptocodec.RegisterInterfaces(registry)
	authtypes.RegisterInterfaces(registry)
	flatfeestypes.RegisterInterfaces(registry)

	cdc := codec.NewProtoCodec(registry)
	txConfig, err := authtx.NewTxConfigWithOptions(cdc, authtx.ConfigOptions{
		EnabledSignModes: authtx.DefaultSignModes,
		SigningOptions:   &signingOptions,
	})
	if err != nil {
		return nil, nil, err
	}
	return cdc, txConfig, nil
}
