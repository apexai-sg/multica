// antom-inquiry reads one Antom subscription's live state. It is the manual
// reconciliation step for a period whose notification never landed in the
// database; there is deliberately no admin route, because an authenticated
// read of another merchant's payment data is a bigger surface than an operator
// running one command on the host.
//
// The client is built from the same ANTOM_* variables the API server reads, so
// the command runs beside the deployment's environment instead of taking
// credentials on the command line, where they would land in shell history.
// Every inquiry carries the merchant private key's signature, which is why the
// gateway origin is checked against the same allowlist the server uses.
//
// The command is read-only: it never writes the database, and it cannot replay
// a notification. A mismatch it reports has to be reconciled by hand.
//
// Usage:
//
//	go run ./cmd/antom-inquiry -subscription-id <antom subscription id>
//
// It exits non-zero when Antom answers anything other than a success result;
// InquirySubscription already turns a non-"S" envelope and a response without
// the subscription object into an error, so the separation is: exit 0 means
// Antom holds this subscription and reported its status.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/aurora"
)

// inquiryTimeout bounds one HTTP round trip plus the gateway's own latency.
// AntomClient's http.Client already caps a request at 30s; this is the wall
// clock an operator waits for.
const inquiryTimeout = 60 * time.Second

// antomDefaultAPIBase mirrors antomDefaultAPIBase in internal/aurora, which is
// unexported and AntomClient keeps its resolved config private. The command
// needs it only to show the gateway a run actually hit; the value is not a
// secret and NewAntomClient has already validated whatever the operator set.
const antomDefaultAPIBase = "https://open-sea-global.alipay.com"

func main() {
	subscriptionID := flag.String("subscription-id", "", "Antom subscription id to inquire")
	flag.Parse()

	id := strings.TrimSpace(*subscriptionID)
	if id == "" {
		fmt.Fprintln(os.Stderr, "antom-inquiry: -subscription-id is required")
		os.Exit(2)
	}
	if err := run(id); err != nil {
		fmt.Fprintln(os.Stderr, "antom-inquiry:", err)
		os.Exit(1)
	}
}

func run(subscriptionID string) error {
	// Same names and the same trimming as server/cmd/server/router.go. Currency
	// is not normalised here (the server upper-cases it and then refuses anything
	// but the catalog's currency) because this command never charges: the
	// inquiry body is only the subscription id.
	apiBase := strings.TrimRight(strings.TrimSpace(os.Getenv("ANTOM_API_BASE")), "/")
	client, err := aurora.NewAntomClient(aurora.AntomConfig{
		ClientID:           strings.TrimSpace(os.Getenv("ANTOM_CLIENT_ID")),
		MerchantPrivateKey: strings.TrimSpace(os.Getenv("ANTOM_MERCHANT_PRIVATE_KEY")),
		AntomPublicKey:     strings.TrimSpace(os.Getenv("ANTOM_PUBLIC_KEY")),
		KeyVersion:         strings.TrimSpace(os.Getenv("ANTOM_KEY_VERSION")),
		APIBase:            apiBase,
		Currency:           strings.TrimSpace(os.Getenv("ANTOM_CURRENCY")),
	})
	if err != nil {
		return err
	}

	// Printed first so the operator reads every following line in the context of
	// the gateway it came from: the sandbox and production are separate accounts,
	// and an inquiry answered by the wrong one looks like a plausible result.
	if apiBase == "" {
		fmt.Printf("api_base: %s (default; ANTOM_API_BASE unset)\n", antomDefaultAPIBase)
	} else {
		fmt.Printf("api_base: %s\n", apiBase)
	}

	ctx, cancel := context.WithTimeout(context.Background(), inquiryTimeout)
	defer cancel()
	state, err := client.InquirySubscription(ctx, subscriptionID)
	if err != nil {
		return err
	}

	fmt.Printf("subscription_id: %s\n", state.SubscriptionID)
	fmt.Printf("status: %s\n", state.Status)
	if state.CurrentPeriodEnd.IsZero() {
		// Antom's inquiry may omit currentPeriodEndTime; that is a missing field
		// to report, not an error to hide behind the zero time.
		fmt.Println("current_period_end: (not reported)")
		return nil
	}
	fmt.Printf("current_period_end: %s\n", state.CurrentPeriodEnd.UTC().Format(time.RFC3339))
	return nil
}
