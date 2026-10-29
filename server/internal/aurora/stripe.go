package aurora

import (
	"context"
	"fmt"
	"time"

	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/webhook"
)

// Event is the minimal Stripe event shape handlers depend on. Created orders
// lifecycle updates; Raw carries the type-specific object JSON (checkout session
// or subscription), which the handler unmarshals for that event.
type Event struct {
	ID      string
	Type    string
	Created int64
	Raw     []byte
}

type SubscriptionState struct {
	Status            string
	CustomerID        string
	CurrentPeriodEnd  time.Time
	CancelAtPeriodEnd bool
}

// PaymentProvider is the narrow Stripe surface Aurora needs. Handlers depend
// on this interface; tests use a fake. The real implementation wraps stripe-go.
type PaymentProvider interface {
	CreateSubscriptionCheckout(ctx context.Context, priceID, successURL, cancelURL, userID, tier, checkoutID string) (url string, err error)
	CreateTopupCheckout(ctx context.Context, priceID, successURL, cancelURL, userID, topupID string) (url string, err error)
	CreateBillingPortalSession(ctx context.Context, customerID, returnURL string) (url string, err error)
	GetSubscriptionState(ctx context.Context, subscriptionID string) (SubscriptionState, error)
	ConstructEvent(payload []byte, sigHeader string) (Event, error)
}

// StripeProvider implements PaymentProvider via stripe-go. ConstructEvent
// verifies the webhook signature before anything else touches the payload.
//
// The API key lives on the client rather than in stripe's package-global Key,
// so a test that builds a provider with a dummy key cannot re-point a provider
// another test is using.
type StripeProvider struct {
	client        *stripe.Client
	webhookSecret string
}

// NewStripeProvider returns nil unless both keys are configured — handlers
// treat a nil provider as "payments disabled" and fail closed with 503.
func NewStripeProvider(secretKey, webhookSecret string) *StripeProvider {
	if secretKey == "" || webhookSecret == "" {
		return nil
	}
	return &StripeProvider{
		client:        stripe.NewClient(secretKey),
		webhookSecret: webhookSecret,
	}
}

func (p *StripeProvider) CreateSubscriptionCheckout(ctx context.Context, priceID, successURL, cancelURL, userID, tier, checkoutID string) (string, error) {
	metadata := map[string]string{"userId": userID, "tier": tier, "checkoutId": checkoutID}
	params := &stripe.CheckoutSessionCreateParams{
		Mode:       stripe.String(stripe.CheckoutSessionModeSubscription),
		LineItems:  []*stripe.CheckoutSessionCreateLineItemParams{{Price: stripe.String(priceID), Quantity: stripe.Int64(1)}},
		SuccessURL: stripe.String(successURL),
		CancelURL:  stripe.String(cancelURL),
		// Session metadata attributes checkout completion; SubscriptionData copies
		// it onto lifecycle events, allowing an out-of-order update/delete event to
		// reconcile the pending local intent without waiting for completion first.
		Metadata:         metadata,
		SubscriptionData: &stripe.CheckoutSessionCreateSubscriptionDataParams{Metadata: metadata},
	}
	params.SetIdempotencyKey(checkoutID)
	session, err := p.client.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return "", err
	}
	return session.URL, nil
}

func (p *StripeProvider) CreateTopupCheckout(ctx context.Context, priceID, successURL, cancelURL, userID, topupID string) (string, error) {
	session, err := p.client.V1CheckoutSessions.Create(ctx, &stripe.CheckoutSessionCreateParams{
		Mode:       stripe.String(stripe.CheckoutSessionModePayment),
		LineItems:  []*stripe.CheckoutSessionCreateLineItemParams{{Price: stripe.String(priceID), Quantity: stripe.Int64(1)}},
		SuccessURL: stripe.String(successURL),
		CancelURL:  stripe.String(cancelURL),
		// topupId selects the credit amount from the catalog; the amount is
		// never read back from Stripe, so a tampered id can only pick another
		// listed pack, never mint credits.
		Metadata: map[string]string{"userId": userID, "topupId": topupID},
	})
	if err != nil {
		return "", err
	}
	return session.URL, nil
}

// CreateBillingPortalSession opens Stripe's hosted Customer Portal for one
// customer. The portal is where a subscriber cancels, changes plan, or updates
// a payment method without support; the return URL is where Stripe sends them
// back afterwards.
func (p *StripeProvider) CreateBillingPortalSession(ctx context.Context, customerID, returnURL string) (string, error) {
	session, err := p.client.V1BillingPortalSessions.Create(ctx, &stripe.BillingPortalSessionCreateParams{
		Customer:  stripe.String(customerID),
		ReturnURL: stripe.String(returnURL),
	})
	if err != nil {
		return "", err
	}
	return session.URL, nil
}

// GetSubscriptionState reads Stripe's canonical lifecycle and billing period.
// Stripe moved current_period_end off the subscription onto its items (a
// subscription can now bill several items on different cycles), so the latest
// item period is the subscription's period. Aurora sells exactly one item, and
// taking the max remains correct if that changes.
func (p *StripeProvider) GetSubscriptionState(ctx context.Context, subscriptionID string) (SubscriptionState, error) {
	sub, err := p.client.V1Subscriptions.Retrieve(ctx, subscriptionID, &stripe.SubscriptionRetrieveParams{})
	if err != nil {
		return SubscriptionState{}, err
	}
	var end int64
	if sub.Items != nil {
		for _, item := range sub.Items.Data {
			if item.CurrentPeriodEnd > end {
				end = item.CurrentPeriodEnd
			}
		}
	}
	if end == 0 {
		// A period-less subscription would otherwise be stored as the epoch,
		// which the monthly grant scan reads as "long expired" — a silent
		// downgrade instead of a visible failure.
		return SubscriptionState{}, fmt.Errorf("subscription %s has no current period end", subscriptionID)
	}
	state := SubscriptionState{
		Status:            string(sub.Status),
		CurrentPeriodEnd:  time.Unix(end, 0).UTC(),
		CancelAtPeriodEnd: sub.CancelAtPeriodEnd,
	}
	if sub.Customer != nil {
		state.CustomerID = sub.Customer.ID
	}
	return state, nil
}

// ConstructEvent verifies the webhook signature and returns the minimal event
// the handler routes on. An unverified payload never leaves this method.
func (p *StripeProvider) ConstructEvent(payload []byte, sigHeader string) (Event, error) {
	event, err := webhook.ConstructEvent(payload, sigHeader, p.webhookSecret)
	if err != nil {
		return Event{}, err
	}
	var raw []byte
	if event.Data != nil {
		raw = event.Data.Raw
	}
	return Event{ID: event.ID, Type: string(event.Type), Created: event.Created, Raw: raw}, nil
}
