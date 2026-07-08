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

// SetSDKConfig sets the global bech32 prefixes for the given account prefix and seals the config.
func SetSDKConfig(hrp string) {
	c := sdk.GetConfig()
	c.SetBech32PrefixForAccount(hrp, hrp+"pub")
	c.SetBech32PrefixForValidator(hrp+"valoper", hrp+"valoperpub")
	c.SetBech32PrefixForConsensusNode(hrp+"valcons", hrp+"valconspub")
	c.Seal()
}

// SetChainConfig configures Provenance bech32 prefixes and coin type; must run before any address parsing.
func SetChainConfig(testnet bool) {
	hrp := prefixMainNet
	if testnet {
		hrp = prefixTestNet
	}
	SetSDKConfig(hrp)
}

// SetChainConfigFromAddress derives the hrp from an account address and sets the sdk config accordingly.
func SetChainConfigFromAddress(addr string) error {
	hrp, _, err := bech32.DecodeAndConvert(addr)
	if err != nil {
		return fmt.Errorf("decode oracle address %q: %w", addr, err)
	}
	SetSDKConfig(hrp)
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
