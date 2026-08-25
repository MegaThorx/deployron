package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/MegaThorx/deployron/common"
)

type serviceCall struct {
	identifier string
	parameter  string
}

func setupTest(t *testing.T) *[]serviceCall {
	t.Helper()

	config = &common.Config{
		Deployments: []common.Deployment{
			{Name: "test", Secret: "s3cret"},
		},
	}
	authFailures = newAuthThrottle()
	trustedProxies = nil

	var calls []serviceCall
	callService = func(identifier string, parameter string) (*common.Message, error) {
		calls = append(calls, serviceCall{identifier: identifier, parameter: parameter})
		switch identifier {
		case "EXC_DEPLOY":
			return &common.Message{Identifier: "DEPLOY_ACK", Parameter: "1"}, nil
		case "GET_STATUS":
			return statusReply(common.DeployStatus{State: "idle", Last: "none"}), nil
		default:
			return &common.Message{Identifier: "DEPLOY_ERR", Parameter: "unknown command"}, nil
		}
	}
	t.Cleanup(func() { callService = callServiceOverSocket })

	return &calls
}

func statusReply(status common.DeployStatus) *common.Message {
	payload, err := json.Marshal(status)
	if err != nil {
		panic(err)
	}
	return &common.Message{Identifier: "STATUS", Parameter: string(payload)}
}

type requestOptions struct {
	secret     string
	remoteAddr string
	forwarded  string
}

func doRequest(method, target string, opts requestOptions) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if opts.secret != "" {
		req.Header.Set("X-API-Secret", opts.secret)
	}
	if opts.remoteAddr != "" {
		req.RemoteAddr = opts.remoteAddr
	}
	if opts.forwarded != "" {
		req.Header.Set("X-Forwarded-For", opts.forwarded)
	}
	res := httptest.NewRecorder()
	newRouter().ServeHTTP(res, req)
	return res
}

func TestDeployHappyPath(t *testing.T) {
	calls := setupTest(t)

	res := doRequest(http.MethodPost, "/deploy/test", requestOptions{secret: "s3cret"})

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", res.Code, http.StatusOK, res.Body.String())
	}
	if len(*calls) != 1 || (*calls)[0] != (serviceCall{identifier: "EXC_DEPLOY", parameter: "test"}) {
		t.Errorf("service calls = %+v, want one EXC_DEPLOY for \"test\"", *calls)
	}
	if !strings.Contains(res.Body.String(), `"status":"queued"`) || !strings.Contains(res.Body.String(), `"run":1`) {
		t.Errorf("body = %s, want queued status with run ID", res.Body.String())
	}
}

func TestDeployRejectsQueryParameterSecret(t *testing.T) {
	calls := setupTest(t)

	res := doRequest(http.MethodPost, "/deploy/test?APISecret=s3cret", requestOptions{})

	if res.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", res.Code, http.StatusNotFound)
	}
	if len(*calls) != 0 {
		t.Errorf("made %d service calls, want 0", len(*calls))
	}
}

func TestDeployRejectsGet(t *testing.T) {
	calls := setupTest(t)

	res := doRequest(http.MethodGet, "/deploy/test", requestOptions{secret: "s3cret"})

	if res.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", res.Code, http.StatusMethodNotAllowed)
	}
	if len(*calls) != 0 {
		t.Errorf("made %d service calls, want 0", len(*calls))
	}
}

func TestDeployWrongSecret(t *testing.T) {
	calls := setupTest(t)

	res := doRequest(http.MethodPost, "/deploy/test", requestOptions{secret: "wrong"})

	if res.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", res.Code, http.StatusNotFound)
	}
	if len(*calls) != 0 {
		t.Errorf("made %d service calls, want 0", len(*calls))
	}
}

func TestDeployUnknownNameMatchesWrongSecretResponse(t *testing.T) {
	setupTest(t)

	unknown := doRequest(http.MethodPost, "/deploy/nosuch", requestOptions{secret: "s3cret"})
	wrongSecret := doRequest(http.MethodPost, "/deploy/test", requestOptions{secret: "wrong"})

	if unknown.Code != http.StatusNotFound {
		t.Errorf("unknown name status = %d, want %d", unknown.Code, http.StatusNotFound)
	}
	if unknown.Code != wrongSecret.Code || unknown.Body.String() != wrongSecret.Body.String() {
		t.Errorf("unknown name response (%d, %q) differs from wrong secret response (%d, %q)",
			unknown.Code, unknown.Body.String(), wrongSecret.Code, wrongSecret.Body.String())
	}
}

func TestDeployThrottlesRepeatedFailures(t *testing.T) {
	setupTest(t)

	attacker := "203.0.113.7:4444"
	for i := 0; i < maxAuthFailures; i++ {
		if res := doRequest(http.MethodPost, "/deploy/test", requestOptions{secret: "wrong", remoteAddr: attacker}); res.Code != http.StatusNotFound {
			t.Fatalf("attempt %d: status = %d, want %d", i, res.Code, http.StatusNotFound)
		}
	}

	if res := doRequest(http.MethodPost, "/deploy/test", requestOptions{secret: "s3cret", remoteAddr: attacker}); res.Code != http.StatusTooManyRequests {
		t.Errorf("throttled request status = %d, want %d", res.Code, http.StatusTooManyRequests)
	}

	// Other addresses are unaffected.
	if res := doRequest(http.MethodPost, "/deploy/test", requestOptions{secret: "s3cret", remoteAddr: "198.51.100.1:1234"}); res.Code != http.StatusOK {
		t.Errorf("unthrottled request status = %d, want %d", res.Code, http.StatusOK)
	}
}

func TestStatusEndpoint(t *testing.T) {
	calls := setupTest(t)
	callService = func(identifier string, parameter string) (*common.Message, error) {
		*calls = append(*calls, serviceCall{identifier: identifier, parameter: parameter})
		return statusReply(common.DeployStatus{State: "idle", Last: "success", LastRun: 3, DurationMS: 1200}), nil
	}

	res := doRequest(http.MethodGet, "/deploy/test/status", requestOptions{secret: "s3cret"})

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", res.Code, http.StatusOK, res.Body.String())
	}
	if len(*calls) != 1 || (*calls)[0].identifier != "GET_STATUS" {
		t.Errorf("service calls = %+v, want one GET_STATUS", *calls)
	}
	if !strings.Contains(res.Body.String(), `"last":"success"`) || !strings.Contains(res.Body.String(), `"last_run":3`) {
		t.Errorf("body = %s, want service status passed through", res.Body.String())
	}
}

func TestStatusEndpointRequiresSecret(t *testing.T) {
	calls := setupTest(t)

	res := doRequest(http.MethodGet, "/deploy/test/status", requestOptions{secret: "wrong"})

	if res.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", res.Code, http.StatusNotFound)
	}
	if len(*calls) != 0 {
		t.Errorf("made %d service calls, want 0", len(*calls))
	}
}

// shrinkWaitTimings makes wait-mode tests fast and restores the defaults.
func shrinkWaitTimings(t *testing.T, timeout time.Duration) {
	t.Helper()
	oldTimeout, oldInterval := waitTimeout, waitPollInterval
	waitTimeout, waitPollInterval = timeout, time.Millisecond
	t.Cleanup(func() { waitTimeout, waitPollInterval = oldTimeout, oldInterval })
}

// waitStub acks a deploy as run 5, reports "running" for the first status
// polls, then reports run 5 finished with the given outcome.
func waitStub(calls *[]serviceCall, pollsUntilDone int, outcome string) func(string, string) (*common.Message, error) {
	polls := 0
	return func(identifier string, parameter string) (*common.Message, error) {
		*calls = append(*calls, serviceCall{identifier: identifier, parameter: parameter})
		if identifier == "EXC_DEPLOY" {
			return &common.Message{Identifier: "DEPLOY_ACK", Parameter: "5"}, nil
		}
		polls++
		if polls <= pollsUntilDone {
			return statusReply(common.DeployStatus{State: "running", Run: 5, Last: "success", LastRun: 4}), nil
		}
		return statusReply(common.DeployStatus{State: "idle", Last: outcome, LastRun: 5, DurationMS: 40}), nil
	}
}

func TestDeployWaitSuccess(t *testing.T) {
	calls := setupTest(t)
	shrinkWaitTimings(t, time.Second)
	callService = waitStub(calls, 2, "success")

	res := doRequest(http.MethodPost, "/deploy/test?wait=true", requestOptions{secret: "s3cret"})

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", res.Code, http.StatusOK, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"status":"success"`) || !strings.Contains(res.Body.String(), `"run":5`) {
		t.Errorf("body = %s, want success for run 5", res.Body.String())
	}
}

func TestDeployWaitFailure(t *testing.T) {
	calls := setupTest(t)
	shrinkWaitTimings(t, time.Second)
	callService = waitStub(calls, 1, "failed")

	res := doRequest(http.MethodPost, "/deploy/test?wait=true", requestOptions{secret: "s3cret"})

	if res.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d (body: %s)", res.Code, http.StatusBadGateway, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"status":"failed"`) {
		t.Errorf("body = %s, want failed status", res.Body.String())
	}
}

func TestDeployWaitTimeout(t *testing.T) {
	calls := setupTest(t)
	shrinkWaitTimings(t, 10*time.Millisecond)
	callService = waitStub(calls, 1<<30, "success") // never finishes

	res := doRequest(http.MethodPost, "/deploy/test?wait=true", requestOptions{secret: "s3cret"})

	if res.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d (body: %s)", res.Code, http.StatusGatewayTimeout, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"status":"timeout"`) {
		t.Errorf("body = %s, want timeout status", res.Body.String())
	}
}

func TestThrottleUsesForwardedForOnlyFromTrustedProxy(t *testing.T) {
	setupTest(t)
	trustedProxies = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}

	proxy := "192.0.2.10:5000"
	for i := 0; i < maxAuthFailures; i++ {
		doRequest(http.MethodPost, "/deploy/test", requestOptions{secret: "wrong", remoteAddr: proxy, forwarded: "203.0.113.50"})
	}

	// The forwarded client is throttled, a different client behind the same proxy is not.
	if res := doRequest(http.MethodPost, "/deploy/test", requestOptions{secret: "s3cret", remoteAddr: proxy, forwarded: "203.0.113.50"}); res.Code != http.StatusTooManyRequests {
		t.Errorf("forwarded client status = %d, want %d", res.Code, http.StatusTooManyRequests)
	}
	if res := doRequest(http.MethodPost, "/deploy/test", requestOptions{secret: "s3cret", remoteAddr: proxy, forwarded: "203.0.113.51"}); res.Code != http.StatusOK {
		t.Errorf("other client behind proxy status = %d, want %d", res.Code, http.StatusOK)
	}
}

func TestThrottleIgnoresForwardedForFromUntrustedPeer(t *testing.T) {
	setupTest(t)
	trustedProxies = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}

	// An untrusted peer spoofing X-Forwarded-For must be throttled by its own
	// address, and must not be able to shift blame or evade the limit.
	attacker := "198.51.100.9:1234"
	for i := 0; i < maxAuthFailures; i++ {
		spoofed := fmt.Sprintf("10.0.0.%d", i)
		doRequest(http.MethodPost, "/deploy/test", requestOptions{secret: "wrong", remoteAddr: attacker, forwarded: spoofed})
	}

	if res := doRequest(http.MethodPost, "/deploy/test", requestOptions{secret: "s3cret", remoteAddr: attacker}); res.Code != http.StatusTooManyRequests {
		t.Errorf("untrusted peer status = %d, want %d", res.Code, http.StatusTooManyRequests)
	}
}
