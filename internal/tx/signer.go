package tx

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/cosmos/cosmos-sdk/client"
	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	signing "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
)

// signMode uses SIGN_MODE_DIRECT, which requires the account number even for unordered transactions.
const signMode = signing.SignMode_SIGN_MODE_DIRECT

// Signer holds the oracle key and codec; the raw key is kept in memory only.
type Signer struct {
	privKey  cryptotypes.PrivKey
	pubKey   cryptotypes.PubKey
	address  sdk.AccAddress
	chainID  string
	txConfig client.TxConfig
}

// NewSigner creates a Signer from a hex-encoded secp256k1 private key.
func NewSigner(privHex, chainID string, txConfig client.TxConfig) (*Signer, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(privHex), "0x")
	keyBz, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode private key hex: %w", err)
	}
	if len(keyBz) != 32 {
		return nil, fmt.Errorf("private key must be 32 bytes, got %d", len(keyBz))
	}
	priv := &secp256k1.PrivKey{Key: keyBz}
	pub := priv.PubKey()
	return &Signer{
		privKey:  priv,
		pubKey:   pub,
		address:  sdk.AccAddress(pub.Address()),
		chainID:  chainID,
		txConfig: txConfig,
	}, nil
}

// Address returns the bech32 address derived from the key.
func (s *Signer) Address() string { return s.address.String() }

// SimOrdered encodes an unsigned ORDERED tx for the CalculateTxFees query. [Task 1]
func (s *Signer) SimOrdered(msg sdk.Msg) ([]byte, error) {
	b, err := s.newSimBuilder(msg)
	if err != nil {
		return nil, err
	}
	return s.encode(b)
}

// BuildOrdered signs an ORDERED tx, replay-protected by the account sequence.
func (s *Signer) BuildOrdered(ctx context.Context, msg sdk.Msg, fees sdk.Coins, gas, accNum, seq uint64) ([]byte, error) {
	b := s.txConfig.NewTxBuilder()
	if err := b.SetMsgs(msg); err != nil {
		return nil, fmt.Errorf("set msgs: %w", err)
	}
	b.SetFeeAmount(fees)
	b.SetGasLimit(gas)
	if err := s.sign(ctx, b, accNum, seq); err != nil {
		return nil, err
	}
	return s.encode(b)
}

// SimUnordered builds an unsigned unordered tx for fee calculation and simulation.
func (s *Signer) SimUnordered(msg sdk.Msg, timeout time.Duration) ([]byte, error) {
	b, err := s.newSimBuilder(msg)
	if err != nil {
		return nil, err
	}
	b.SetUnordered(true)
	b.SetTimeoutTimestamp(time.Now().UTC().Add(timeout))
	return s.encode(b)
}

// BuildUnordered signs an UNORDERED tx, replay-protected by timeout_timestamp.
func (s *Signer) BuildUnordered(ctx context.Context, msg sdk.Msg, fees sdk.Coins, gas, accNum uint64, timeout time.Duration) ([]byte, error) {
	b := s.txConfig.NewTxBuilder()
	if err := b.SetMsgs(msg); err != nil {
		return nil, fmt.Errorf("set msgs: %w", err)
	}
	b.SetFeeAmount(fees)
	b.SetGasLimit(gas)
	b.SetUnordered(true)
	b.SetTimeoutTimestamp(time.Now().UTC().Add(timeout))
	if err := s.sign(ctx, b, accNum, 0); err != nil {
		return nil, err
	}
	return s.encode(b)
}

// newSimBuilder creates a minimal tx builder with message and dummy signature for fee estimation.
func (s *Signer) newSimBuilder(msg sdk.Msg) (client.TxBuilder, error) {
	b := s.txConfig.NewTxBuilder()
	if err := b.SetMsgs(msg); err != nil {
		return nil, fmt.Errorf("set msgs: %w", err)
	}
	sig := signing.SignatureV2{
		PubKey: s.pubKey,
		Data:   &signing.SingleSignatureData{SignMode: signMode},
	}
	if err := b.SetSignatures(sig); err != nil {
		return nil, fmt.Errorf("set sim signature: %w", err)
	}
	return b, nil
}

// sign performs the two-pass SIGN_MODE_DIRECT signing: an empty signature to fix
// the SignerInfo, then the real signature over the canonical sign bytes.
func (s *Signer) sign(ctx context.Context, b client.TxBuilder, accNum, seq uint64) error {
	empty := signing.SignatureV2{
		PubKey:   s.pubKey,
		Data:     &signing.SingleSignatureData{SignMode: signMode},
		Sequence: seq,
	}
	if err := b.SetSignatures(empty); err != nil {
		return fmt.Errorf("set empty signature: %w", err)
	}
	signerData := authsigning.SignerData{
		ChainID:       s.chainID,
		AccountNumber: accNum,
		Sequence:      seq,
		PubKey:        s.pubKey,
		Address:       s.address.String(),
	}
	sig, err := clienttx.SignWithPrivKey(ctx, signMode, signerData, b, s.privKey, s.txConfig, seq)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	if err := b.SetSignatures(sig); err != nil {
		return fmt.Errorf("set final signature: %w", err)
	}
	return nil
}

func (s *Signer) encode(b client.TxBuilder) ([]byte, error) {
	bz, err := s.txConfig.TxEncoder()(b.GetTx())
	if err != nil {
		return nil, fmt.Errorf("encode tx: %w", err)
	}
	return bz, nil
}
