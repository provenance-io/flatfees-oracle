package tx

import (
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/address"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/std"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/gogoproto/proto"

	txsigning "cosmossdk.io/x/tx/signing"

	flatfeestypes "github.com/provenance-io/provenance/x/flatfees/types"
)

const (
	prefixMainNet = "pb"
	prefixTestNet = "tp"
	coinTypeMain  = uint32(505)
	coinTypeTest  = uint32(1)
	purpose       = 44
)

// SetChainConfig configures Provenance bech32 prefixes and coin type; must run before any address parsing.
func SetChainConfig(testnet bool) {
	prefix := prefixMainNet
	coinType := coinTypeMain
	if testnet {
		prefix = prefixTestNet
		coinType = coinTypeTest
	}
	c := sdk.GetConfig()
	c.SetCoinType(coinType)
	c.SetPurpose(purpose)
	c.SetBech32PrefixForAccount(prefix, prefix+"pub")
	c.SetBech32PrefixForValidator(prefix+"valoper", prefix+"valoperpub")
	c.SetBech32PrefixForConsensusNode(prefix+"valcons", prefix+"valconspub")
	c.Seal()
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
