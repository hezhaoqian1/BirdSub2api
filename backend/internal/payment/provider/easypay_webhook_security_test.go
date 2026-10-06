package provider

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

// TestEasyPayNotificationRejectsReturnURLSignatureSmuggling reproduces the
// forged "payment success" callback described in the EasyPay popup-mode
// disclosure and asserts the provider now rejects it.
//
// In popup/submit.php mode the browser is handed a fully pkey-signed
// create-order URL. Because easyPaySign concatenates key=value pairs without
// delimiting the values, an attacker-controlled return_url whose value ends in
// "&trade_status=TRADE_SUCCESS" yields the identical signed byte string whether
// that suffix belongs to the return_url value or to a standalone trade_status
// parameter. The attacker replays the create-order signature, re-partitioning
// the one field into two, to forge a success notification without knowing pkey.
func TestEasyPayNotificationRejectsReturnURLSignatureSmuggling(t *testing.T) {
	t.Parallel()

	const pkey = "merchant_secret_key"
	provider := &EasyPay{config: map[string]string{"pid": "1001", "pkey": pkey}}

	// return_url exactly as the server builds it for a popup-mode order: the
	// attacker-supplied "?...&trade_status=TRADE_SUCCESS" survives
	// canonicalisation, and query.Encode() sorts trade_status to the tail.
	returnURLBase := "https://victim.example/payment/result?order_id=123&out_trade_no=ORDER1&status=success"
	smuggledReturnURL := returnURLBase + "&trade_status=TRADE_SUCCESS"

	// The merchant-signed create-order parameters exposed to the browser.
	createParams := map[string]string{
		"pid":          "1001",
		"type":         "alipay",
		"out_trade_no": "ORDER1",
		"notify_url":   "https://victim.example/api/v1/payment/webhook/easypay",
		"return_url":   smuggledReturnURL,
		"name":         "Recharge",
		"money":        "60.00",
	}
	createSign := easyPaySign(createParams, pkey)

	// The attacker re-partitions return_url: the "&trade_status=TRADE_SUCCESS"
	// tail becomes a standalone parameter while the signature is reused verbatim.
	forged := map[string]string{
		"pid":          "1001",
		"type":         "alipay",
		"out_trade_no": "ORDER1",
		"notify_url":   createParams["notify_url"],
		"return_url":   returnURLBase,
		"name":         "Recharge",
		"money":        "60.00",
		"trade_status": "TRADE_SUCCESS",
		"sign":         createSign,
		"sign_type":    signTypeMD5,
	}

	// Sanity check: the signature genuinely still validates after the split.
	// This is the ambiguity the fix defends against — if this assertion ever
	// fails the underlying sign scheme changed and this test needs revisiting.
	if !easyPayVerifySign(forged, pkey, createSign) {
		t.Fatal("precondition: re-partitioned params should reuse the create-order signature")
	}

	body := url.Values{}
	for k, v := range forged {
		body.Set(k, v)
	}

	_, err := provider.VerifyNotification(context.Background(), body.Encode(), nil)
	if err == nil {
		t.Fatal("forged notification with smuggled trade_status must be rejected")
	}
	if !strings.Contains(err.Error(), "return_url") {
		t.Fatalf("expected rejection to cite the request-only parameter, got: %v", err)
	}
}

// TestEasyPayNotificationAcceptsGenuineCallback ensures the hardening does not
// break a legitimate asynchronous notification (which never carries return_url
// or notify_url).
func TestEasyPayNotificationAcceptsGenuineCallback(t *testing.T) {
	t.Parallel()

	const pkey = "merchant_secret_key"
	provider := &EasyPay{config: map[string]string{"pid": "1001", "pkey": pkey}}

	params := map[string]string{
		"pid":          "1001",
		"trade_no":     "20260101000000",
		"out_trade_no": "ORDER1",
		"type":         "alipay",
		"name":         "Recharge",
		"money":        "60.00",
		"trade_status": "TRADE_SUCCESS",
	}
	sign := easyPaySign(params, pkey)
	params["sign"] = sign
	params["sign_type"] = signTypeMD5

	body := url.Values{}
	for k, v := range params {
		body.Set(k, v)
	}

	notification, err := provider.VerifyNotification(context.Background(), body.Encode(), nil)
	if err != nil {
		t.Fatalf("genuine notification should verify, got: %v", err)
	}
	if notification.Status != payment.ProviderStatusSuccess {
		t.Fatalf("status = %v, want success", notification.Status)
	}
	if notification.OrderID != "ORDER1" || notification.TradeNo != "20260101000000" {
		t.Fatalf("unexpected notification fields: %+v", notification)
	}
	if notification.Amount != 60.00 {
		t.Fatalf("amount = %v, want 60.00", notification.Amount)
	}
}
