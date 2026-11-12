package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

func TestNormalizeEasyPayAPIBase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{input: "https://zpayz.cn", want: "https://zpayz.cn"},
		{input: "https://zpayz.cn/", want: "https://zpayz.cn"},
		{input: "https://zpayz.cn/mapi.php", want: "https://zpayz.cn"},
		{input: "https://zpayz.cn/submit.php", want: "https://zpayz.cn"},
		{input: "https://zpayz.cn/api.php", want: "https://zpayz.cn"},
		{input: "https://zpayz.cn/api.php?act=refund", want: "https://zpayz.cn"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			if got := normalizeEasyPayAPIBase(tt.input); got != tt.want {
				t.Fatalf("normalizeEasyPayAPIBase(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestEasyPayRefundNormalizesAPIBaseAndSendsOutTradeNoOnly(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotQuery url.Values
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1,"msg":"ok"}`))
	}))
	defer server.Close()

	provider := newTestEasyPay(t, server.URL+"/mapi.php")
	resp, err := provider.Refund(context.Background(), payment.RefundRequest{
		TradeNo: "trade-123",
		OrderID: "out-456",
		Amount:  "1.50",
	})
	if err != nil {
		t.Fatalf("Refund returned error: %v", err)
	}
	if resp == nil || resp.Status != payment.ProviderStatusSuccess {
		t.Fatalf("Refund response = %+v, want success", resp)
	}
	if gotPath != "/api.php" {
		t.Fatalf("refund path = %q, want /api.php", gotPath)
	}
	if gotQuery.Get("act") != "refund" {
		t.Fatalf("refund act query = %q, want refund", gotQuery.Get("act"))
	}
	for key, want := range map[string]string{
		"pid":          "pid-1",
		"key":          "pkey-1",
		"out_trade_no": "out-456",
		"money":        "1.50",
	} {
		if got := gotForm.Get(key); got != want {
			t.Fatalf("form[%s] = %q, want %q (form=%v)", key, got, want, gotForm)
		}
	}
	if got := gotForm.Get("trade_no"); got != "" {
		t.Fatalf("form[trade_no] = %q, want empty (form=%v)", got, gotForm)
	}
}

func TestEasyPayRefundRetriesWithTradeNoWhenOutTradeNoNotFound(t *testing.T) {
	t.Parallel()

	var gotForms []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php" {
			t.Errorf("refund path = %q, want /api.php", r.URL.Path)
		}
		if r.URL.Query().Get("act") != "refund" {
			t.Errorf("refund act query = %q, want refund", r.URL.Query().Get("act"))
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		gotForms = append(gotForms, r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		if len(gotForms) == 1 {
			_, _ = w.Write([]byte(`{"code":-1,"msg":"当前订单不存在！"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"退款成功！退款金额¥1.50"}`))
	}))
	defer server.Close()

	provider := newTestEasyPay(t, server.URL+"/mapi.php")
	resp, err := provider.Refund(context.Background(), payment.RefundRequest{
		TradeNo: "trade-123",
		OrderID: "out-456",
		Amount:  "1.50",
	})
	if err != nil {
		t.Fatalf("Refund returned error: %v", err)
	}
	if resp == nil || resp.Status != payment.ProviderStatusSuccess || resp.RefundID != "trade-123" {
		t.Fatalf("Refund response = %+v, want success with trade refund id", resp)
	}
	if len(gotForms) != 2 {
		t.Fatalf("refund attempts = %d, want 2", len(gotForms))
	}
	if got := gotForms[0].Get("out_trade_no"); got != "out-456" {
		t.Fatalf("first form[out_trade_no] = %q, want out-456 (form=%v)", got, gotForms[0])
	}
	if got := gotForms[0].Get("trade_no"); got != "" {
		t.Fatalf("first form[trade_no] = %q, want empty (form=%v)", got, gotForms[0])
	}
	if got := gotForms[1].Get("trade_no"); got != "trade-123" {
		t.Fatalf("second form[trade_no] = %q, want trade-123 (form=%v)", got, gotForms[1])
	}
	if got := gotForms[1].Get("out_trade_no"); got != "" {
		t.Fatalf("second form[out_trade_no] = %q, want empty (form=%v)", got, gotForms[1])
	}
}

func TestEasyPayRefundSuccessResponseCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "numeric one", body: `{"code":1,"msg":"ok"}`},
		{name: "string one", body: `{"code":"1","msg":"ok"}`},
		{name: "numeric zero with refund success", body: `{"code":0,"msg":"退款成功！退款金额¥10.00"}`},
		{name: "string zero with refund success", body: `{"code":"0","msg":"退款成功！退款金额¥10.00"}`},
		{name: "zero with plain refund success", body: `{"code":0,"msg":"退款成功"}`},
		{name: "zero with whitespace", body: `{"code":" 0 ","msg":" 退款成功！退款金额¥10.00 "}`},
		{name: "zero with english success", body: `{"code":0,"msg":"SUCCESS"}`},
		{name: "zero with ok", body: `{"code":0,"msg":"ok"}`},
		{name: "zero without message", body: `{"code":0}`},
		{name: "upstream refund response", body: `{"code":0,"refund_no":"refund-789","out_refund_no":"refund-789","trade_no":"trade-123","out_trade_no":"out-456","uid":1001,"money":"10.00","reducemoney":"10.00","msg":"退款成功！退款金额¥10.00"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			provider := newTestEasyPay(t, server.URL)
			resp, err := provider.Refund(context.Background(), payment.RefundRequest{
				OrderID: "out-456",
				TradeNo: "trade-123",
				Amount:  "10.00",
			})
			if err != nil {
				t.Fatalf("Refund returned error: %v", err)
			}
			if resp == nil || resp.Status != payment.ProviderStatusSuccess || resp.RefundID != "out-456" {
				t.Fatalf("Refund response = %+v, want success with merchant order id", resp)
			}
			if attempts != 1 {
				t.Fatalf("refund attempts = %d, want 1", attempts)
			}
		})
	}
}

func TestEasyPayRefundResponseErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       string
		want       string
	}{
		{name: "html response", statusCode: http.StatusOK, body: "<html>bad config</html>", want: "non-JSON response (HTTP 200): <html>bad config</html>"},
		{name: "non json response", statusCode: http.StatusOK, body: "not json", want: "non-JSON response (HTTP 200): not json"},
		{name: "non 2xx response", statusCode: http.StatusBadGateway, body: "bad gateway", want: "HTTP 502: bad gateway"},
		{name: "empty response", statusCode: http.StatusOK, body: "", want: "empty response (HTTP 200): <empty>"},
		{name: "upstream refund failure", statusCode: http.StatusOK, body: `{"code":-1,"msg":"退款失败：商户余额不足"}`, want: "failed (HTTP 200): 退款失败：商户余额不足"},
		{name: "upstream refund disabled", statusCode: http.StatusOK, body: `{"code":-2,"msg":"商户未开启订单退款API接口"}`, want: "failed (HTTP 200): 商户未开启订单退款API接口"},
		{name: "upstream invalid merchant key", statusCode: http.StatusOK, body: `{"code":-3,"msg":"商户密钥错误"}`, want: "failed (HTTP 200): 商户密钥错误"},
		{name: "upstream self service refund disabled", statusCode: http.StatusOK, body: `{"code":-4,"msg":"未开启商户后台自助退款"}`, want: "failed (HTTP 200): 未开启商户后台自助退款"},
		{name: "already refunded is not a new success", statusCode: http.StatusOK, body: `{"code":-1,"msg":"该订单已全额退款！"}`, want: "failed (HTTP 200): 该订单已全额退款！"},
		{name: "missing code", statusCode: http.StatusOK, body: `{"msg":"退款成功"}`, want: "failed (HTTP 200)"},
		{name: "null code", statusCode: http.StatusOK, body: `{"code":null,"msg":"退款成功"}`, want: "failed (HTTP 200)"},
		{name: "boolean code", statusCode: http.StatusOK, body: `{"code":false,"msg":"退款成功"}`, want: "failed (HTTP 200)"},
		{name: "negative code", statusCode: http.StatusOK, body: `{"code":-1,"msg":"退款成功"}`, want: "failed (HTTP 200)"},
		{name: "unknown code", statusCode: http.StatusOK, body: `{"code":2,"msg":"退款成功"}`, want: "failed (HTTP 200)"},
		{name: "fractional zero code", statusCode: http.StatusOK, body: `{"code":0.5,"msg":"退款成功"}`, want: "failed (HTTP 200)"},
		{name: "fractional one code", statusCode: http.StatusOK, body: `{"code":1.5,"msg":"退款成功"}`, want: "failed (HTTP 200)"},
		{name: "http failure with success body", statusCode: http.StatusBadGateway, body: `{"code":0,"msg":"退款成功"}`, want: "HTTP 502"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			provider := newTestEasyPay(t, server.URL)
			_, err := provider.Refund(context.Background(), payment.RefundRequest{
				OrderID: "out-456",
				Amount:  "1.50",
			})
			if err == nil {
				t.Fatal("Refund returned nil error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Refund error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestSummarizeEasyPayResponsePreservesUTF8(t *testing.T) {
	t.Parallel()

	summary := summarizeEasyPayResponse([]byte(strings.Repeat("错", 171)))
	if !utf8.ValidString(summary) {
		t.Fatalf("summarizeEasyPayResponse returned invalid UTF-8: %q", summary)
	}
	if !strings.HasSuffix(summary, "...") {
		t.Fatalf("summarizeEasyPayResponse() = %q, want truncated suffix", summary)
	}
}

func TestEasyPayCustomMethodsUseConfiguredUpstreamType(t *testing.T) {
	t.Parallel()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":           "pid-1",
		"pkey":          "pkey-1",
		"apiBase":       "https://pay.example.com",
		"notifyUrl":     "https://example.com/notify",
		"returnUrl":     "https://example.com/return",
		"paymentMode":   paymentModePopup,
		"customMethods": `[{"type":"ldc","upstreamType":"epay","displayName":"LDC"},{"type":"usdt_trc20","upstreamType":"usdt.trc20","displayName":"USDT-TRC20"}]`,
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}

	resp, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID:     "sub2-custom-1",
		Amount:      "1.00",
		PaymentType: "usdt_trc20",
		Subject:     "Custom EasyPay",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	payURL, err := url.Parse(resp.PayURL)
	if err != nil {
		t.Fatalf("parse pay url: %v", err)
	}
	if got := payURL.Query().Get("type"); got != "usdt.trc20" {
		t.Fatalf("pay url type = %q, want usdt.trc20 (%s)", got, resp.PayURL)
	}
}

func TestEasyPayCustomMethodsResolveCIDFromConfiguredUpstreamType(t *testing.T) {
	t.Parallel()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":           "pid-1",
		"pkey":          "pkey-1",
		"apiBase":       "https://pay.example.com",
		"notifyUrl":     "https://example.com/notify",
		"returnUrl":     "https://example.com/return",
		"paymentMode":   paymentModePopup,
		"cidAlipay":     "cid-alipay",
		"cidWxpay":      "cid-wxpay",
		"customMethods": `[{"type":"ldc","upstreamType":"alipay","displayName":"LDC"}]`,
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}

	resp, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID:     "sub2-custom-cid",
		Amount:      "1.00",
		PaymentType: "ldc",
		Subject:     "Custom EasyPay CID",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	payURL, err := url.Parse(resp.PayURL)
	if err != nil {
		t.Fatalf("parse pay url: %v", err)
	}
	if got := payURL.Query().Get("type"); got != "alipay" {
		t.Fatalf("pay url type = %q, want alipay (%s)", got, resp.PayURL)
	}
	if got := payURL.Query().Get("cid"); got != "cid-alipay" {
		t.Fatalf("pay url cid = %q, want cid-alipay (%s)", got, resp.PayURL)
	}
}

func TestEasyPaySupportedTypesIncludeCustomMethods(t *testing.T) {
	t.Parallel()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":           "pid-1",
		"pkey":          "pkey-1",
		"apiBase":       "https://pay.example.com",
		"notifyUrl":     "https://example.com/notify",
		"returnUrl":     "https://example.com/return",
		"customMethods": `[{"type":"ldc","upstreamType":"epay","displayName":"LDC"},{"type":"usdt_trc20","upstreamType":"usdt","displayName":"USDT-TRC20"}]`,
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}

	got := strings.Join(provider.SupportedTypes(), ",")
	for _, want := range []string{"alipay", "wxpay", "ldc", "usdt_trc20"} {
		if !strings.Contains(got, want) {
			t.Fatalf("SupportedTypes() = %q, want it to include %q", got, want)
		}
	}
}

func newTestEasyPay(t *testing.T, apiBase string) *EasyPay {
	t.Helper()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":       "pid-1",
		"pkey":      "pkey-1",
		"apiBase":   apiBase,
		"notifyUrl": "https://example.com/notify",
		"returnUrl": "https://example.com/return",
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}
	return provider
}
