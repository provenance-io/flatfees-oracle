package tx

import (
	"context"
	"strings"
	"testing"
	"time"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/client"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"

	flatfeestypes "github.com/provenance-io/provenance/x/flatfees/types"
)

// A fixed 32-byte test key.
const testKeyHex = "1111111111111111111111111111111111111111111111111111111111111111"

// init sets the bech32 config for the tx package's tests. seal=false so
// additional test files can call SetChainConfig* without tripping the SDK
// config seal.
func init() { SetChainConfig(true, false) } // testnet → "tp" prefix

func testMsg(authority string) sdk.Msg {
	return &flatfeestypes.MsgUpdateConversionFactorRequest{
		Authority: authority,
		ConversionFactor: flatfeestypes.ConversionFactor{
			DefinitionAmount: sdk.NewCoin("musd", math.NewInt(12)),
			ConvertedAmount:  sdk.NewCoin("nhash", math.NewInt(1_000_000_000)),
		},
	}
}

func newTestSigner(t *testing.T) (*Signer, client.TxConfig) {
	t.Helper()
	_, txConfig, err := NewEncoding()
	if err != nil {
		t.Fatalf("NewEncoding: %v", err)
	}
	s, err := NewSigner(testKeyHex, "testing-1", txConfig)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s, txConfig
}

func TestAddressDerivation(t *testing.T) {
	s, _ := newTestSigner(t)
	if !strings.HasPrefix(s.Address(), "tp") {
		t.Errorf("expected testnet (tp) address, got %s", s.Address())
	}
}

func TestNewSignerRejectsBadKeys(t *testing.T) {
	_, txConfig, err := NewEncoding()
	if err != nil {
		t.Fatalf("NewEncoding: %v", err)
	}
	cases := map[string]string{
		"empty":     "",
		"not-hex":   "zzzz",
		"too-short": "1111",
		"too-long":  strings.Repeat("11", 33),
	}
	for name, k := range cases {
		if _, err := NewSigner(k, "testing-1", txConfig); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestSimBytesDecode(t *testing.T) {
	s, txConfig := newTestSigner(t)
	msg := testMsg(s.Address())

	ordered, err := s.SimOrdered(msg)
	if err != nil {
		t.Fatalf("SimOrdered: %v", err)
	}
	if _, err := txConfig.TxDecoder()(ordered); err != nil {
		t.Fatalf("decode ordered sim: %v", err)
	}

	unordered, err := s.SimUnordered(msg, 2*time.Minute)
	if err != nil {
		t.Fatalf("SimUnordered: %v", err)
	}
	if _, err := txConfig.TxDecoder()(unordered); err != nil {
		t.Fatalf("decode unordered sim: %v", err)
	}
}

func TestBuildOrderedSignsOnce(t *testing.T) {
	s, txConfig := newTestSigner(t)
	fees := sdk.NewCoins(sdk.NewCoin("nhash", math.NewInt(1_000_000)))

	bz, err := s.BuildOrdered(context.Background(), testMsg(s.Address()), fees, 200_000, 7, 3)
	if err != nil {
		t.Fatalf("BuildOrdered: %v", err)
	}
	decoded, err := txConfig.TxDecoder()(bz)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	sigTx := decoded.(authsigning.SigVerifiableTx)
	sigs, err := sigTx.GetSignaturesV2()
	if err != nil {
		t.Fatalf("GetSignaturesV2: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("want 1 signature, got %d", len(sigs))
	}
}

func TestBuildUnorderedSetsTimeoutAndZeroSequence(t *testing.T) {
	s, txConfig := newTestSigner(t)
	fees := sdk.NewCoins(sdk.NewCoin("nhash", math.NewInt(1_000_000)))

	bz, err := s.BuildUnordered(context.Background(), testMsg(s.Address()), fees, 200_000, 7, 2*time.Minute)
	if err != nil {
		t.Fatalf("BuildUnordered: %v", err)
	}
	decoded, err := txConfig.TxDecoder()(bz)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	uTx, ok := decoded.(sdk.TxWithUnordered)
	if !ok || !uTx.GetUnordered() {
		t.Fatal("expected an unordered tx")
	}
	if uTx.GetTimeoutTimeStamp().IsZero() {
		t.Error("expected a non-zero timeout_timestamp")
	}

	sigs, err := decoded.(authsigning.SigVerifiableTx).GetSignaturesV2()
	if err != nil {
		t.Fatalf("GetSignaturesV2: %v", err)
	}
	if len(sigs) != 1 || sigs[0].Sequence != 0 {
		t.Errorf("unordered tx must have one sig with sequence 0, got %+v", sigs)
	}
}
