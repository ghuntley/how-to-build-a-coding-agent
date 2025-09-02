package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	stripe "github.com/stripe/stripe-go/v76"
	portal "github.com/stripe/stripe-go/v76/billingportal/session"
	checkout "github.com/stripe/stripe-go/v76/checkout/session"
	"github.com/stripe/stripe-go/v76/customer"
	"github.com/stripe/stripe-go/v76/webhook"
)

func (s *Server) initStripeFromEnv() {
	key := strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY"))
	if key != "" {
		stripe.Key = key
	}
}

func (s *Server) handleCheckout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	user, err := s.getUserFromRequest(r)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
		return
	}
	priceID := strings.TrimSpace(os.Getenv("STRIPE_PRICE_ID"))
	if priceID == "" {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "STRIPE_PRICE_ID not configured"})
		return
	}
	publicURL := strings.TrimSpace(os.Getenv("PUBLIC_URL"))
	if publicURL == "" { publicURL = "/" }
	customerID := user.StripeCustomerID
	if customerID == "" {
		params := &stripe.CustomerParams{Email: stripe.String(user.Email)}
		cust, err := customer.New(params)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("create customer failed: %v", err)})
			return
		}
		customerID = cust.ID
		_ = s.userStore.SetStripeCustomerID(user.Email, customerID)
	}
	cs, err := checkout.New(&stripe.CheckoutSessionParams{
		Mode:       stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		Customer:   stripe.String(customerID),
		LineItems:  []*stripe.CheckoutSessionLineItemParams{{Price: stripe.String(priceID), Quantity: stripe.Int64(1)}},
		SuccessURL: stripe.String(publicURL + "?checkout=success"),
		CancelURL:  stripe.String(publicURL + "?checkout=cancel"),
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("checkout failed: %v", err)})
		return
	}
	writeJSON(w, map[string]string{"url": cs.URL})
}

func (s *Server) handlePortal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	user, err := s.getUserFromRequest(r)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
		return
	}
	if user.StripeCustomerID == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "no Stripe customer"})
		return
	}
	publicURL := strings.TrimSpace(os.Getenv("PUBLIC_URL"))
	if publicURL == "" { publicURL = "/" }
	ps, err := portal.New(&stripe.BillingPortalSessionParams{
		Customer:  stripe.String(user.StripeCustomerID),
		ReturnURL: stripe.String(publicURL),
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("portal failed: %v", err)})
		return
	}
	writeJSON(w, map[string]string{"url": ps.URL})
}

func (s *Server) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(r.Body)
	if err != nil { w.WriteHeader(http.StatusBadRequest); return }
	endpointSecret := strings.TrimSpace(os.Getenv("STRIPE_WEBHOOK_SECRET"))
	var event stripe.Event
	if endpointSecret == "" {
		if err := json.Unmarshal(payload, &event); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	} else {
		sig := r.Header.Get("Stripe-Signature")
		event, err = webhook.ConstructEvent(payload, sig, endpointSecret)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	switch event.Type {
	case "checkout.session.completed":
		var cs stripe.CheckoutSession
		if err := json.Unmarshal(event.Data.Raw, &cs); err == nil {
			email := strings.TrimSpace(cs.CustomerEmail)
			if email == "" && cs.Customer != nil {
				if u := s.userStore.FindByStripeCustomerID(cs.Customer.ID); u != nil { email = u.Email }
			}
			if email != "" { _ = s.userStore.SetSubscription(email, true) }
		}
	case "customer.subscription.created", "customer.subscription.updated", "invoice.paid":
		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err == nil {
			u := s.userStore.FindByStripeCustomerID(sub.Customer.ID)
			if u != nil {
				ok := sub.Status == stripe.SubscriptionStatusActive || sub.Status == stripe.SubscriptionStatusTrialing
				_ = s.userStore.SetSubscription(u.Email, ok)
			}
		}
	case "customer.subscription.deleted", "invoice.payment_failed":
		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err == nil {
			u := s.userStore.FindByStripeCustomerID(sub.Customer.ID)
			if u != nil { _ = s.userStore.SetSubscription(u.Email, false) }
		}
	}
	w.WriteHeader(http.StatusOK)
}