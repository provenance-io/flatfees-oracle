// Command oracle runs once: fetch the HASH price, compute the flatfees
// conversion factor, and (unless DRY_RUN) submit an on-chain update if it changed.
// It is designed to run as a Kubernetes CronJob; it exits 0 on success (including
// a no-op skip) and non-zero on failure.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"os"
	"time"

	// Embed the timezone database so America/New_York resolves without tzdata on
	// the (distroless) runtime image.
	_ "time/tzdata"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/provenance-io/flatfees-oracle/internal/chain"
	"github.com/provenance-io/flatfees-oracle/internal/config"
	"github.com/provenance-io/flatfees-oracle/internal/convert"
	"github.com/provenance-io/flatfees-oracle/internal/logging"
	"github.com/provenance-io/flatfees-oracle/internal/price"
	"github.com/provenance-io/flatfees-oracle/internal/tx"
)

var (
	errUnauthorized = errors.New("oracle address not in module oracle_addresses")
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		// Logger isn't configured yet; emit a minimal structured line.
		logging.New("error", "unknown").Error("config load failed", "error", err.Error())
		return err
	}

	log := logging.New(cfg.LogLevel, cfg.Env)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// 1. Fetch price (VWAP over the trailing window).
	pc := price.New()
	if cfg.PriceBaseURL != "" {
		pc.BaseURL = cfg.PriceBaseURL
	}
	pc.HTTP.Timeout = cfg.HTTPTimeout

	res, err := pc.GetPrice(ctx)
	if err != nil {
		log.Error("price fetch failed", "error", err.Error(), "outcome", "failed")
		return err
	}
	log.Info("price fetched",
		"price_usd_per_hash", res.PriceUSDPerHASH.FloatString(12),
		"trades", res.Trades,
		"window_start", res.WindowStart.Format(time.RFC3339),
		"window_end", res.WindowEnd.Format(time.RFC3339),
	)

	// 2. Compute the conversion factor.
	cf, err := convert.Compute(res.PriceUSDPerHASH)
	if err != nil {
		log.Error("factor computation failed", "error", err.Error(), "outcome", "failed")
		return err
	}
	modFactor, err := chain.ToModuleFactor(cf)
	if err != nil {
		log.Error("factor mapping failed", "error", err.Error(), "outcome", "failed")
		return err
	}
	log.Info("factor computed",
		"tier", string(cf.Tier),
		"definition_amount", modFactor.DefinitionAmount.String(),
		"converted_amount", modFactor.ConvertedAmount.String(),
	)

	if cfg.DryRun {
		log.Info("dry run; not submitting", "outcome", "skipped")
		return nil
	}

	// Set the bech32 prefix from the oracle address (only needed when signing).
	if err := tx.SetChainConfigFromAddress(cfg.OracleAddress); err != nil {
		log.Error("invalid oracle address", "error", err.Error(), "outcome", "failed")
		return err
	}
	// 3. Connect to the node.
	// TLS by default (system root CAs, min TLS 1.2). GRPC_INSECURE=true is an
	// explicit opt-out for in-cluster / localhost endpoints on a trusted network.
	var creds credentials.TransportCredentials
	if cfg.GRPCInsecure {
		creds = insecure.NewCredentials()
		log.Warn("using insecure gRPC transport", "endpoint", cfg.GRPCEndpoint)
	} else {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
		log.Info("using secure gRPC transport", "endpoint", cfg.GRPCEndpoint)
	}
	conn, err := grpc.NewClient(cfg.GRPCEndpoint, grpc.WithTransportCredentials(creds))
	if err != nil {
		log.Error("grpc connect failed", "error", err.Error(), "endpoint", cfg.GRPCEndpoint, "outcome", "failed")
		return err
	}
	defer conn.Close() //nolint:errcheck // There's nothing we can do with an error from this.
	reader := chain.NewReader(conn)

	// 4. Read current params: authorization + skip-if-unchanged.
	params, err := reader.CurrentParams(ctx)
	if err != nil {
		log.Error("read params failed", "error", err.Error(), "outcome", "failed")
		return err
	}
	if !chain.IsAuthorizedOracle(params, cfg.OracleAddress) {
		log.Error("oracle address not authorized", "oracle_address", cfg.OracleAddress, "outcome", "failed")
		return errUnauthorized
	}
	if chain.SameFactor(params.ConversionFactor, modFactor) {
		log.Info("conversion factor unchanged; skipping submit", "outcome", "skipped")
		return nil
	}

	cdc, txConfig, err := tx.NewEncoding()
	if err != nil {
		log.Error("encoding setup failed", "error", err.Error(), "outcome", "failed")
		return err
	}

	signer, err := tx.NewSigner(cfg.PrivateKeyHex, cfg.ChainID, txConfig)
	if err != nil {
		log.Error("signer init failed", "error", err.Error(), "outcome", "failed")
		return err
	}
	if signer.Address() != cfg.OracleAddress {
		log.Error("key/address mismatch", "derived", signer.Address(), "configured", cfg.OracleAddress, "outcome", "failed")
		return errors.New("derived address does not match ORACLE_ADDRESS")
	}

	// 5. Build the update message and estimate gas/fees.
	msg := chain.BuildUpdateMsg(cfg.OracleAddress, modFactor)

	submitter := &tx.Submitter{
		Signer:      signer,
		Estimator:   reader, // *chain.Reader satisfies tx.Estimator
		Broadcaster: tx.NewBroadcaster(conn),
		Account: func(ctx context.Context, addr string) (uint64, uint64, error) {
			return chain.AccountInfo(ctx, conn, cdc, addr)
		},
		GasAdjustment: cfg.GasAdjustment,
		Logger:        log,
	}

	// Submit under a FRESH timeout.
	submitCtx, submitCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer submitCancel()

	var hash string
	if cfg.Unordered {
		hash, err = submitter.SubmitUnordered(submitCtx, msg, cfg.AccountNumber, cfg.UnorderedTimeout)
	} else {
		hash, err = submitter.SubmitOrdered(submitCtx, msg)
	}
	if err != nil {
		log.Error("submit failed", "unordered", cfg.Unordered, "tx_hash", hash, "error", err.Error(), "outcome", "failed")
		return err
	}
	log.Info("conversion factor updated", "tx_hash", hash, "unordered", cfg.Unordered, "outcome", "submitted")

	return nil
}
